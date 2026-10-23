package protocol

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestControllerDrainsBufferedResultsOnTransportEOF(t *testing.T) {
	for attempt := range 100 {
		messages := make(chan map[string]any, 1)
		errs := make(chan error)
		messages <- map[string]any{"type": "result"}
		close(messages)
		close(errs)

		controller := NewController(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		controller.wg.Add(1)
		controller.readLoop(context.Background(), messages, errs)

		msg, ok := <-controller.Messages()
		if !ok || msg["type"] != "result" {
			t.Fatalf("attempt %d: buffered terminal result lost on EOF", attempt)
		}
		if _, ok := <-controller.Messages(); ok {
			t.Fatal("message channel must close after draining")
		}
	}
}
