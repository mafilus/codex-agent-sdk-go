package subprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/mafilus/codex-agent-sdk-go/internal/cli"
	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

const appServerScannerBuffer = 1 << 20 // 1 MB

// AppServerTransport manages a codex app-server subprocess communicating via
// JSON-RPC 2.0 over stdin/stdout.
type AppServerTransport struct {
	log  *slog.Logger
	opts *config.Options

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	mu        sync.Mutex
	nextID    int64
	ready     bool
	closing   bool
	pending   map[int64]chan *RPCResponse
	notifyCh  chan *RPCNotification
	requestCh chan *RPCIncomingRequest
	readDone  chan struct{}
	stop      chan struct{}
}

// NewAppServerTransport creates a new app server transport.
func NewAppServerTransport(log *slog.Logger, opts *config.Options) *AppServerTransport {
	return &AppServerTransport{
		log:       log.With(slog.String("component", "appserver")),
		opts:      opts,
		pending:   make(map[int64]chan *RPCResponse, 4),
		notifyCh:  make(chan *RPCNotification, 128),
		requestCh: make(chan *RPCIncomingRequest, 32),
		readDone:  make(chan struct{}),
		stop:      make(chan struct{}),
	}
}

// Start discovers the Codex CLI binary, spawns the app-server subprocess,
// and performs the initialize handshake.
func (t *AppServerTransport) Start(ctx context.Context) error {
	discoverer := cli.NewDiscoverer(&cli.Config{
		CliPath:          t.opts.CliPath,
		SkipVersionCheck: t.opts.SkipVersionCheck,
		Logger:           t.log,
	})

	path, err := discoverer.Discover(ctx)
	if err != nil {
		return err
	}

	args := cli.BuildAppServerArgs(t.opts)

	t.log.InfoContext(ctx, "starting codex app-server",
		slog.String("path", path),
		slog.Any("args", args),
	)

	t.cmd = exec.CommandContext(ctx, path, args...)
	t.cmd.Env = cli.BuildEnvironment(t.opts)

	if t.opts.Cwd != "" {
		t.cmd.Dir = t.opts.Cwd
	}

	t.stdin, err = t.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating stdin pipe: %w", err)
	}

	stdoutPipe, err := t.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}

	t.stdout = bufio.NewScanner(stdoutPipe)
	t.stdout.Buffer(
		make([]byte, 0, appServerScannerBuffer),
		appServerScannerBuffer,
	)

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("starting codex app-server: %w", err)
	}

	t.log.InfoContext(ctx, "codex app-server started",
		slog.Int("pid", t.cmd.Process.Pid),
	)

	go t.readLoop()

	if err := t.initialize(ctx); err != nil {
		_ = t.Close()

		return fmt.Errorf("initialize handshake: %w", err)
	}

	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()

	return nil
}

// initialize sends the initialize request and initialized notification.
func (t *AppServerTransport) initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": "2025-01-01",
		"capabilities": map[string]any{
			"experimentalApi": true,
		},
		"clientInfo": map[string]string{
			"name":    "github.com/mafilus/codex-agent-sdk-go",
			"version": "0.1.0",
		},
	}

	_, err := t.SendRequest(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("sending initialize request: %w", err)
	}

	if err := t.sendNotification("initialized", nil); err != nil {
		return fmt.Errorf("sending initialized notification: %w", err)
	}

	return nil
}

// SendRequest sends a JSON-RPC request and waits for the matching response.
func (t *AppServerTransport) SendRequest(
	ctx context.Context,
	method string,
	params any,
) (*RPCResponse, error) {
	id := atomic.AddInt64(&t.nextID, 1)
	ch := make(chan *RPCResponse, 1)

	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()

		return nil, fmt.Errorf("transport is closing")
	}

	t.pending[id] = ch
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}()

	req := &RPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		ID:      id,
		Params:  params,
	}

	if err := t.writeMessage(req); err != nil {
		return nil, fmt.Errorf("writing request: %w", err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}

		return resp, nil
	case <-t.readDone:
		return nil, fmt.Errorf("transport closed while waiting for response")
	}
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (t *AppServerTransport) sendNotification(method string, params any) error {
	var rawParams json.RawMessage

	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshaling params: %w", err)
		}

		rawParams = data
	}

	notif := &RPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	}

	return t.writeMessage(notif)
}

// Notifications returns the channel of incoming notifications.
func (t *AppServerTransport) Notifications() <-chan *RPCNotification {
	return t.notifyCh
}

// Requests returns the channel of incoming server-to-client requests.
func (t *AppServerTransport) Requests() <-chan *RPCIncomingRequest {
	return t.requestCh
}

// SendResponse sends a JSON-RPC response back to the server for a given request ID.
func (t *AppServerTransport) SendResponse(
	id int64,
	result json.RawMessage,
	rpcErr *RPCError,
) error {
	resp := &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	}

	return t.writeMessage(resp)
}

// IsReady reports whether the transport has completed the initialize handshake.
func (t *AppServerTransport) IsReady() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.ready
}

// Close terminates the app server subprocess.
func (t *AppServerTransport) Close() error {
	t.mu.Lock()
	if t.closing {
		t.mu.Unlock()

		return nil
	}

	t.closing = true
	close(t.stop)
	t.mu.Unlock()

	if t.stdin != nil {
		_ = t.stdin.Close()
	}

	if t.cmd != nil && t.cmd.Process != nil {
		t.log.Debug("killing codex app-server process",
			slog.Int("pid", t.cmd.Process.Pid),
		)

		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}

	return nil
}

// writeMessage marshals and writes a JSON-RPC message to stdin.
func (t *AppServerTransport) writeMessage(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closing {
		return fmt.Errorf("transport is closing")
	}

	data = append(data, '\n')

	if _, err := t.stdin.Write(data); err != nil {
		return fmt.Errorf("writing to stdin: %w", err)
	}

	return nil
}

// readLoop reads JSON-RPC messages from stdout and dispatches them.
func (t *AppServerTransport) readLoop() {
	defer close(t.readDone)
	notifications := make(chan *RPCNotification)
	requests := make(chan *RPCIncomingRequest)
	defer close(notifications)
	defer close(requests)

	// Keep reading RPC responses even while an event consumer is slow. Blocking
	// stdout on event delivery can otherwise deadlock a pending RPC handshake.
	go forwardRPCEvents(notifications, t.notifyCh, t.stop)
	go forwardRPCEvents(requests, t.requestCh, t.stop)

	for t.stdout.Scan() {
		line := t.stdout.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			t.log.Warn("skipping malformed JSON-RPC message",
				slog.String("error", err.Error()),
			)

			continue
		}

		if msg.isResponse() {
			t.mu.Lock()
			ch, ok := t.pending[*msg.ID]
			t.mu.Unlock()

			if ok {
				ch <- msg.toResponse()
			} else {
				t.log.Warn("received response for unknown request",
					slog.Int64("id", *msg.ID),
				)
			}
		} else if msg.isRequest() {
			select {
			case requests <- msg.toIncomingRequest():
			case <-t.stop:
				return
			}
		} else if msg.isNotification() {
			select {
			case notifications <- msg.toNotification():
			case <-t.stop:
				return
			}
		}
	}
}

// forwardRPCEvents preserves FIFO order and drains queued events on EOF.
// Explicit shutdown discards pending events so Close never waits for a reader.
// The queue grows with the backlog; callers should continuously consume events.
func forwardRPCEvents[T any](input <-chan T, output chan<- T, stop <-chan struct{}) {
	defer close(output)

	var queue []T
	for input != nil || len(queue) > 0 {
		var ready chan<- T
		var next T
		if len(queue) > 0 {
			ready = output
			next = queue[0]
		}

		select {
		case value, ok := <-input:
			if !ok {
				input = nil
			} else {
				queue = append(queue, value)
			}
		case ready <- next:
			var zero T
			queue[0] = zero
			queue = queue[1:]
			if len(queue) == 0 {
				queue = nil
			}
		case <-stop:
			return
		}
	}
}
