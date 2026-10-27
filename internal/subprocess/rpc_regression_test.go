package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/mafilus/codex-agent-sdk-go/internal/mcp"
	"github.com/mafilus/codex-agent-sdk-go/internal/message"
	"github.com/stretchr/testify/require"
)

type rpcWriteFunc func([]byte) (int, error)

func (f rpcWriteFunc) Write(data []byte) (int, error) { return f(data) }
func (rpcWriteFunc) Close() error                     { return nil }

type signaledPipeWriter struct {
	io.WriteCloser
	entered chan struct{}
	once    sync.Once
}

func (w *signaledPipeWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.WriteCloser.Write(data)
}

func newRPCRegressionTransport() *AppServerTransport {
	return NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{})
}

func TestRPCRequestCancellationUnblocksWrite(t *testing.T) {
	transport := newRPCRegressionTransport()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	entered := make(chan struct{})
	transport.stdin = &signaledPipeWriter{WriteCloser: writer, entered: entered}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := transport.SendRequest(ctx, "probe", nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("write did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("request remained blocked after cancellation")
	}
	require.NoError(t, transport.Close())
	transport.mu.Lock()
	defer transport.mu.Unlock()
	require.True(t, transport.closing, "partial writes make the transport unusable")
	require.Empty(t, transport.pending)
}

func TestRPCRequestCancellationWhileWaitingForWriter(t *testing.T) {
	transport := newRPCRegressionTransport()
	transport.writeGate <- struct{}{}
	defer func() { <-transport.writeGate }()
	transport.stdin = rpcWriteFunc(func([]byte) (int, error) {
		t.Error("cancelled request must not write")
		return 0, io.ErrClosedPipe
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := transport.SendRequest(ctx, "probe", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, transport.closing, "cancelling a queued write must not close another request's stream")
	require.Empty(t, transport.pending)
}

func TestRPCResponseWinsOverEOF(t *testing.T) {
	for _, rpcErr := range []*RPCError{nil, {Code: -32000, Message: "terminal RPC error"}} {
		for range 200 {
			transport := newRPCRegressionTransport()
			transport.stdin = rpcWriteFunc(func(data []byte) (int, error) {
				var request RPCRequest
				require.NoError(t, json.Unmarshal(data, &request))
				transport.pending[request.ID] <- &RPCResponse{ID: request.ID, Result: json.RawMessage(`{"ok":true}`), Error: rpcErr}
				close(transport.readDone)
				return len(data), nil
			})
			response, err := transport.SendRequest(context.Background(), "probe", nil)
			if rpcErr != nil {
				require.ErrorIs(t, err, rpcErr)
			} else {
				require.NoError(t, err)
				require.JSONEq(t, `{"ok":true}`, string(response.Result))
			}
		}
	}
	transport := newRPCRegressionTransport()
	transport.stdin = rpcWriteFunc(func(data []byte) (int, error) {
		close(transport.readDone)
		return len(data), nil
	})
	_, err := transport.SendRequest(context.Background(), "probe", nil)
	require.ErrorContains(t, err, "transport closed")
}

func TestMCPStartupLogsOmitConfigValues(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	transport := processTransport(t, "wait")
	transport.log = logger
	transport.opts.MCPServers = map[string]mcp.ServerConfig{
		"http":  &mcp.HTTPServerConfig{URL: "https://example.invalid", Headers: map[string]string{"Authorization": "audit-header-secret"}},
		"stdio": &mcp.StdioServerConfig{Command: "probe", Env: map[string]string{"TOKEN": "audit-env-secret"}},
	}
	// The synthetic process rejects extra argv and exits. The startup log is
	// emitted before its handshake; neither its output nor any real backend is needed.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.Error(t, transport.Start(ctx))
	require.NoError(t, transport.Close())
	require.Contains(t, logs.String(), "starting codex app-server")
	require.NotContains(t, logs.String(), "audit-header-secret")
	require.NotContains(t, logs.String(), "audit-env-secret")
}

func TestNativeTerminalTurnStatus(t *testing.T) {
	for _, status := range []string{"completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			adapter := NewAppServerAdapter(slog.Default(), &config.Options{})
			adapter.threadID = "stale-thread"
			adapter.lastAssistantText = "partial answer"
			payload, err := json.Marshal(map[string]any{
				"threadId": "native-thread", "isError": false,
				"turn": map[string]any{"id": "native-turn", "status": status, "error": map[string]any{"message": "native failure"}},
			})
			require.NoError(t, err)
			event := adapter.translateNotification(&RPCNotification{Method: "turn/completed", Params: payload})
			require.Equal(t, "native-turn", event["turn_id"])
			parsed, err := message.Parse(slog.Default(), event)
			require.NoError(t, err)
			result, ok := parsed.(*message.ResultMessage)
			require.True(t, ok)
			require.Equal(t, "native-thread", result.SessionID)
			require.Equal(t, status != "completed", result.IsError)
			switch status {
			case "failed":
				require.Equal(t, "error", result.Subtype)
				require.Equal(t, "native failure", *result.Result)
			case "interrupted":
				require.Equal(t, "interrupted", result.Subtype)
			default:
				require.Equal(t, "success", result.Subtype)
				require.Equal(t, "partial answer", *result.Result)
			}
		})
	}
}

func TestRPCWriteCancellationStopsOwnedProcess(t *testing.T) {
	transport := processTransport(t, "blocked-input")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, transport.Start(ctx))
	t.Cleanup(func() { require.NoError(t, transport.Close()) })
	select {
	case notification := <-transport.Notifications():
		require.Equal(t, "fixture/blocked", notification.Method)
	case <-ctx.Done():
		t.Fatal("process did not enter stalled state")
	}
	entered := make(chan struct{})
	transport.mu.Lock()
	transport.stdin = &signaledPipeWriter{WriteCloser: transport.stdin, entered: entered}
	transport.mu.Unlock()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	done := make(chan error, 1)
	go func() {
		_, err := transport.SendRequest(requestCtx, "probe", strings.Repeat("x", 2<<20))
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("request write did not start")
	}
	cancelRequest()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-ctx.Done():
		t.Fatal("stalled write did not release")
	}
	result, err := transport.Wait(ctx)
	require.NoError(t, err)
	require.True(t, result.StopRequested)
	require.NoError(t, transport.Stop(ctx))
}
