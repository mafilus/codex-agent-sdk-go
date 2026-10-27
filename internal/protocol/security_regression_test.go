package protocol

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/mafilus/codex-agent-sdk-go/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestDynamicToolsEnforcePermissionPolicy(t *testing.T) {
	callbackErr := errors.New("policy unavailable")
	for _, mode := range []string{"disallowed", "not-allowed", "deny", "allow-updated", "callback-error", "invalid-result", "typed-nil"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			policyCalls := 0
			opts := &config.Options{SDKTools: []*config.DynamicTool{{
				Name: "restricted",
				Handler: func(_ context.Context, input map[string]any) (map[string]any, error) {
					calls++
					require.Equal(t, "updated", input["message"])
					return input, nil
				},
			}}}
			switch mode {
			case "disallowed":
				opts.AllowedTools = []string{"restricted"}
				opts.DisallowedTools = []string{"restricted"}
			case "not-allowed":
				opts.AllowedTools = []string{"other"}
			default:
				opts.CanUseTool = func(_ context.Context, name string, _ map[string]any, _ *permission.Context) (permission.Result, error) {
					policyCalls++
					require.Equal(t, "restricted", name)
					switch mode {
					case "deny":
						return &permission.ResultDeny{Message: "blocked"}, nil
					case "allow-updated":
						return &permission.ResultAllow{UpdatedInput: map[string]any{"message": "updated"}}, nil
					case "callback-error":
						return nil, callbackErr
					case "typed-nil":
						var decision *permission.ResultAllow
						return decision, nil
					default:
						return nil, nil //nolint:nilnil // Deliberately invalid callback result exercises fail-closed behavior.
					}
				}
			}
			require.NoError(t, config.ConfigureToolPermissionPolicy(opts))
			session := NewSession(slog.Default(), nil, opts)
			session.RegisterDynamicTools()
			result, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
				Request: map[string]any{"tool": "restricted", "arguments": map[string]any{"message": "original"}},
			})
			switch mode {
			case "allow-updated":
				require.NoError(t, err)
				require.Equal(t, true, result["success"])
				require.Equal(t, 1, calls)
			case "callback-error":
				require.ErrorIs(t, err, callbackErr)
			default:
				if mode == "invalid-result" || mode == "typed-nil" {
					require.ErrorContains(t, err, "invalid result")
				} else {
					require.NoError(t, err)
					require.Equal(t, false, result["success"])
				}
			}
			if mode != "allow-updated" {
				require.Zero(t, calls, "a denied or invalid decision must never execute the handler")
			}
			if mode != "disallowed" && mode != "not-allowed" {
				require.Equal(t, 1, policyCalls)
			}
		})
	}
}

type failingControlTransport struct{}

func (failingControlTransport) ReadMessages(context.Context) (<-chan map[string]any, <-chan error) {
	return nil, nil
}
func (failingControlTransport) SendMessage(context.Context, []byte) error {
	return errors.New("synthetic send failure")
}

func TestControlRequestFailureReleasesPending(t *testing.T) {
	controller := NewController(slog.New(slog.NewTextHandler(io.Discard, nil)), failingControlTransport{})
	for range 50 {
		_, err := controller.SendRequest(context.Background(), "probe", nil, time.Second)
		require.ErrorContains(t, err, "send request")
		_, err = controller.SendRequest(context.Background(), "probe", map[string]any{"invalid": make(chan int)}, time.Second)
		require.ErrorContains(t, err, "marshal request")
	}
	require.Empty(t, controller.pending)
}
