//go:build integration

package integration

import (
	"context"
	"errors"
	"iter"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func liveToken() string { return "sdkprobe" + strings.ReplaceAll(uuid.NewString(), "-", "") }

// collectLiveResponse asserts terminal success without logging raw messages.
func collectLiveResponse(t *testing.T, messages iter.Seq2[codexsdk.Message, error]) (string, *codexsdk.ResultMessage) {
	t.Helper()
	var text strings.Builder
	var result *codexsdk.ResultMessage
	for msg, err := range messages {
		require.NoError(t, safeLiveError(err))
		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if content, ok := block.(*codexsdk.TextBlock); ok {
					text.WriteString(content.Text)
				}
			}
		case *codexsdk.ResultMessage:
			require.True(t, result == nil, "a request must not emit multiple terminal results")
			require.False(t, m.IsError, "terminal result must indicate success")
			require.True(t, m.Subtype == "success", "terminal subtype must indicate success")
			result = m
			if m.Result != nil {
				text.WriteString(*m.Result)
			}
		}
	}
	require.NotNil(t, result, "a text event alone is not a successful turn")
	return text.String(), result
}

func closeLiveClient(t *testing.T, client *liveClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	process, err := client.ProcessLifecycle()
	if err == nil {
		if stopErr := process.Stop(ctx); stopErr != nil {
			t.Error("owned process cleanup did not complete within its budget")
		}
	} else if !errors.Is(err, codexsdk.ErrProcessNotStarted) {
		t.Error("live client did not expose its owned process")
	}
	done := make(chan error, 1)
	go func() { done <- client.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Error("client cleanup returned an error")
		}
	case <-ctx.Done():
		t.Error("client Close did not join cleanup within 10 seconds")
	}
}

func startLiveClient(t *testing.T, ctx context.Context, opts ...codexsdk.Option) *liveClient {
	t.Helper()
	client := newLiveClient()
	t.Cleanup(func() { closeLiveClient(t, client) })
	require.NoError(t, safeLiveError(client.Start(ctx, opts...)))
	if model, ok := client.GetServerInfo()["model"].(string); ok {
		if regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`).MatchString(model) {
			t.Logf("observed_model=%s", model)
		} else {
			t.Log("observed_model=withheld-invalid-format")
		}
	}
	return client
}

func TestLiveBackendS01Exec(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	token := liveToken()
	text, _ := collectLiveResponse(t, liveQuery(ctx, "Reply exactly with "+token+". Do not use tools."))
	require.True(t, strings.Contains(text, token), "expected sentinel missing from successful exec response")
}

func TestLiveBackendS02AppServer(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	token := liveToken()
	text, result := collectLiveResponse(t, liveQuery(ctx, "Reply exactly with "+token+". Do not use tools.",
		codexsdk.WithSystemPrompt("Return only the requested sentinel; do not use tools.")))
	require.True(t, strings.Contains(text, token), "expected sentinel missing from app-server response")
	require.NotEmpty(t, result.SessionID)
}

func TestLiveBackendS03QueryStream(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithCancel(liveContext(t))
	defer cancel()
	token := liveToken()
	producerDone := make(chan struct{})
	messages := func(yield func(codexsdk.StreamingMessage) bool) {
		defer close(producerDone)
		if yield(codexsdk.NewUserMessage("Reply exactly with " + token + ". Do not use tools.")) {
			<-ctx.Done() // Remain idle until the consumer leaves the query.
		}
	}
	text, _ := collectLiveResponse(t, codexsdk.QueryStream(ctx, messages, liveOptions()...))
	require.True(t, strings.Contains(text, token), "streamed input did not produce expected response")
	cancel()
	select {
	case <-producerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("cooperative input producer did not exit")
	}
}

func TestLiveBackendS04TwoTurns(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	client := startLiveClient(t, ctx)
	token := liveToken()
	require.NoError(t, safeLiveError(client.Query(ctx, "Remember this token for the next turn: "+token+". Reply only OK. Do not use tools.")))
	_, first := collectLiveResponse(t, client.ReceiveResponse(ctx))
	require.NotEmpty(t, first.SessionID)
	require.NoError(t, safeLiveError(client.Query(ctx, "What token did I ask you to remember? Reply with that token only. Do not use tools.")))
	text, second := collectLiveResponse(t, client.ReceiveResponse(ctx))
	require.True(t, first.SessionID == second.SessionID, "both turns must use the same session")
	require.True(t, strings.Contains(text, token), "second turn did not retain the first turn's token")
}

func TestLiveBackendS05SDKTool(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	token := liveToken() // Deliberately absent from the prompt.
	var calls atomic.Int32
	tool := codexsdk.NewTool("sdk_probe", "Returns the secret probe value requested by the test", emptyLiveSchema(),
		func(context.Context, map[string]any) (map[string]any, error) {
			calls.Add(1)
			return map[string]any{"value": token}, nil
		})
	text, _ := collectLiveResponse(t, liveQuery(ctx, "Call sdk_probe and reply with exactly the value it returns. Do not use other tools.", codexsdk.WithSDKTools(tool)))
	require.Positive(t, calls.Load(), "registration alone does not prove execution")
	require.True(t, strings.Contains(text, token), "tool return value was not used")
}

func TestLiveBackendS06MCPTool(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	token := liveToken()
	var calls atomic.Int32
	tool := codexsdk.NewSdkMcpTool("echo", "Returns the requested probe value", codexsdk.SimpleSchema(map[string]string{}),
		func(context.Context, *codexsdk.CallToolRequest) (*codexsdk.CallToolResult, error) {
			calls.Add(1)
			return codexsdk.TextResult(token), nil
		})
	server := codexsdk.CreateSdkMcpServer("probe", "1.0.0", tool)
	text, _ := collectLiveResponse(t, liveQuery(ctx, "Call the echo tool from the probe server and reply with exactly the value it returns. Do not use other tools.",
		codexsdk.WithMCPServers(map[string]codexsdk.MCPServerConfig{"probe": server})))
	require.Positive(t, calls.Load(), "MCP handler must actually execute")
	require.True(t, strings.Contains(text, token), "MCP return value was not used")
}

func TestLiveBackendS07Cancellation(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithCancel(liveContext(t))
	defer cancel()
	entered := make(chan struct{}, 1)
	released := make(chan struct{}, 1)
	tool := codexsdk.NewTool("wait_for_cancel", "Waits for the test to cancel an active turn", emptyLiveSchema(),
		func(ctx context.Context, _ map[string]any) (map[string]any, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-ctx.Done()
			select {
			case released <- struct{}{}:
			default:
			}
			return nil, ctx.Err()
		})
	client := startLiveClient(t, ctx, codexsdk.WithSDKTools(tool))
	process, err := client.ProcessLifecycle()
	require.NoError(t, safeLiveError(err))
	require.NoError(t, safeLiveError(client.Query(ctx, "Call wait_for_cancel immediately. Wait for its result. Do not use other tools.")))
	receiverDone := make(chan struct{})
	var successfulResult atomic.Bool
	go func() {
		defer close(receiverDone)
		for msg, err := range client.ReceiveMessages(ctx) {
			if err != nil {
				return
			}
			if result, ok := msg.(*codexsdk.ResultMessage); ok && !result.IsError {
				successfulResult.Store(true)
			}
		}
	}()
	select {
	case <-entered: // A real server request, not a synthetic initialization event.
	case <-ctx.Done():
		t.Fatal("model did not enter the cancellable tool")
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	require.NoError(t, safeLiveError(process.Stop(stopCtx)))
	_, err = process.Wait(stopCtx)
	require.NoError(t, safeLiveError(err))
	for _, done := range []<-chan struct{}{process.Done(), process.ReaderDone(), receiverDone, released} {
		select {
		case <-done:
		case <-stopCtx.Done():
			t.Fatal("cancellation did not join process, reader, handler and receiver")
		}
	}
	require.False(t, successfulResult.Load(), "a canceled blocked tool turn must not be reported as successful generation")
	// Concurrent Close calls must join the same cleanup.
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- client.Close() }()
	}
	for range 2 {
		select {
		case err := <-closed:
			require.NoError(t, safeLiveError(err))
		case <-stopCtx.Done():
			t.Fatal("concurrent Close did not join cleanup")
		}
	}
}

func TestLiveBackendS08DeniedTools(t *testing.T) {
	requireLiveBackend(t)
	ctx := liveContext(t)
	for _, kind := range []string{"sdk", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			var calls, decisions atomic.Int32
			token := liveToken()
			callback := func(_ context.Context, name string, _ map[string]any, _ *codexsdk.ToolPermissionContext) (codexsdk.PermissionResult, error) {
				if name == "denied_probe" || name == "mcp__probe__denied_probe" {
					decisions.Add(1)
				}
				return &codexsdk.PermissionResultDeny{Message: "probe denied by test policy"}, nil
			}
			opts := []codexsdk.Option{codexsdk.WithCanUseTool(callback)}
			if kind == "sdk" {
				tool := codexsdk.NewTool("denied_probe", "Probe that must be denied by policy", emptyLiveSchema(),
					func(context.Context, map[string]any) (map[string]any, error) {
						calls.Add(1)
						return map[string]any{"value": token}, nil
					})
				opts = append(opts, codexsdk.WithSDKTools(tool))
			} else {
				tool := codexsdk.NewSdkMcpTool("denied_probe", "Probe that must be denied by policy", codexsdk.SimpleSchema(map[string]string{}),
					func(context.Context, *codexsdk.CallToolRequest) (*codexsdk.CallToolResult, error) {
						calls.Add(1)
						return codexsdk.TextResult(token), nil
					})
				opts = append(opts, codexsdk.WithMCPServers(map[string]codexsdk.MCPServerConfig{"probe": codexsdk.CreateSdkMcpServer("probe", "1.0.0", tool)}))
			}
			text, _ := collectLiveResponse(t, liveQuery(ctx, "Call denied_probe once. If policy denies it, report the denial and do not retry or use any other tool.", opts...))
			require.Positive(t, decisions.Load(), "permission callback must observe the actual request")
			require.Zero(t, calls.Load(), "denied handler must not execute")
			require.False(t, strings.Contains(text, token), "denied tool must not return a fabricated successful value")
		})
	}
}

func emptyLiveSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}
