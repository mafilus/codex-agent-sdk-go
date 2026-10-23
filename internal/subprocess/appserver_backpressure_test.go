package subprocess

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

func TestAppServerPreservesEventsAndRPCResponsesUnderBackpressure(t *testing.T) {
	transport := NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{})
	t.Cleanup(func() { _ = transport.Close() })
	var input strings.Builder
	const count = 300
	for i := range count {
		fmt.Fprintf(&input, "{\"jsonrpc\":\"2.0\",\"method\":\"item/agentMessage/delta\",\"params\":{\"index\":%d}}\n", i)
		fmt.Fprintf(&input, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"item/tool/call\",\"params\":{}}\n", i)
	}
	input.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"turn/completed\",\"params\":{}}\n")
	input.WriteString("{\"jsonrpc\":\"2.0\",\"id\":999,\"result\":{}}\n")
	response := make(chan *RPCResponse, 1)
	transport.pending[999] = response
	transport.stdout = bufio.NewScanner(strings.NewReader(input.String()))
	go transport.readLoop()

	// No event consumer is active yet. RPC responses must still make progress.
	select {
	case <-response:
	case <-time.After(2 * time.Second):
		t.Fatal("event backlog blocked the RPC response")
	}
	select {
	case <-transport.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not finish")
	}

	for i := range count + 1 {
		select {
		case notification, ok := <-transport.Notifications():
			if !ok {
				t.Fatalf("missing notification %d", i)
			}
			if i == count {
				if notification.Method != "turn/completed" {
					t.Fatal("terminal notification lost")
				}
			} else {
				var params struct {
					Index int `json:"index"`
				}
				if err := json.Unmarshal(notification.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params.Index != i {
					t.Fatalf("notification order: got %d, want %d", params.Index, i)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatal("notification queue did not drain")
		}
	}
	for i := range count {
		select {
		case request, ok := <-transport.Requests():
			if !ok || request.ID != int64(i) {
				t.Fatalf("request %d lost or reordered", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("request queue did not drain")
		}
	}
	if _, ok := <-transport.Notifications(); ok {
		t.Fatal("notification channel did not close at EOF")
	}
	if _, ok := <-transport.Requests(); ok {
		t.Fatal("request channel did not close at EOF")
	}
}

func TestAppServerCloseReleasesBlockedEventQueues(t *testing.T) {
	transport := NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{})
	transport.stdout = bufio.NewScanner(strings.NewReader(strings.Repeat(
		"{\"jsonrpc\":\"2.0\",\"method\":\"turn/completed\",\"params\":{}}\n", 300)))
	go transport.readLoop()
	select {
	case <-transport.readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reader stalled")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() {
		for range transport.Notifications() {
		}
		for range transport.Requests() {
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not release event queues")
	}
}
