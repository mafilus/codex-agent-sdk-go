package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/stretchr/testify/require"
)

type connectionTransport struct {
	messages    chan map[string]any
	errs        chan error
	startErr    error
	initErr     error
	closeErr    error
	closeCalls  atomic.Int32
	initializes atomic.Int32
}

func newConnectionTransport() *connectionTransport {
	return &connectionTransport{messages: make(chan map[string]any, 1), errs: make(chan error)}
}

func (t *connectionTransport) Start(context.Context) error { return t.startErr }
func (t *connectionTransport) IsReady() bool               { return true }
func (t *connectionTransport) EndInput() error             { return nil }
func (t *connectionTransport) ReadMessages(context.Context) (<-chan map[string]any, <-chan error) {
	return t.messages, t.errs
}
func (t *connectionTransport) SendMessage(_ context.Context, data []byte) error {
	var request ControlRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return err
	}
	t.initializes.Add(1)
	if t.initErr != nil {
		return t.initErr
	}
	t.messages <- map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"request_id": request.RequestID,
			"subtype":    "success",
			"response":   map[string]any{"ready": true},
		},
	}
	return nil
}
func (t *connectionTransport) Close() error {
	t.closeCalls.Add(1)
	close(t.messages)
	close(t.errs)
	return t.closeErr
}

func TestConnectionInitializationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		force   bool
		options *config.Options
		want    int32
	}{
		{name: "exec", options: &config.Options{}},
		{name: "app-server", force: true, options: &config.Options{}, want: 1},
		{name: "custom-with-tools", options: &config.Options{SDKTools: []*config.DynamicTool{{Name: "probe"}}}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := newConnectionTransport()
			connection := NewConnection(slog.New(slog.NewTextHandler(io.Discard, nil)), transport, tc.options)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			require.NoError(t, connection.Start(ctx, tc.force))
			require.Equal(t, tc.want, transport.initializes.Load())
			if tc.want > 0 {
				require.Equal(t, map[string]any{"ready": true}, connection.Session.GetInitializationResult())
			}
		})
	}
}

func TestConnectionStartupFailureCleansResources(t *testing.T) {
	for _, stage := range []string{"transport", "session"} {
		t.Run(stage, func(t *testing.T) {
			startupErr := errors.New("startup failed")
			cleanupErr := errors.New("cleanup failed")
			transport := newConnectionTransport()
			transport.closeErr = cleanupErr
			if stage == "transport" {
				transport.startErr = startupErr
			} else {
				transport.initErr = startupErr
			}
			connection := NewConnection(slog.New(slog.NewTextHandler(io.Discard, nil)), transport, &config.Options{})
			err := connection.Start(t.Context(), true)
			require.ErrorIs(t, err, startupErr)
			require.ErrorIs(t, err, cleanupErr)
			require.Equal(t, int32(1), transport.closeCalls.Load())
			require.ErrorIs(t, connection.Close(), cleanupErr)
			require.Equal(t, int32(1), transport.closeCalls.Load())
			select {
			case <-connection.Controller.Done():
			default:
				t.Fatal("startup failure left the controller running")
			}
			if stage == "session" {
				_, open := <-connection.Controller.Messages()
				require.False(t, open, "startup cleanup must join the controller reader")
			}
		})
	}
}

func TestConnectionConcurrentCloseSharesCleanup(t *testing.T) {
	transport := newConnectionTransport()
	transport.closeErr = errors.New("cleanup failed")
	connection := NewConnection(slog.New(slog.NewTextHandler(io.Discard, nil)), transport, &config.Options{})
	require.NoError(t, connection.Start(t.Context(), false))
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range cap(results) {
		wg.Go(func() { results <- connection.Close() })
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, transport.closeErr)
	}
	require.Equal(t, int32(1), transport.closeCalls.Load())
}
