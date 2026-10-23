package codexsdk

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"
)

func TestQueryCancellationWhileYielding(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Query"
		if streaming {
			name = "QueryStream"
		}
		t.Run(name, func(t *testing.T) {
			for attempt := range 100 {
				ctx, cancel := context.WithCancel(context.Background())
				transport := newResultMessageTransport()
				transport.msgChan <- map[string]any{"type": "system", "subtype": "init"}
				var messages iter.Seq2[Message, error]
				if streaming {
					messages = QueryStream(ctx, MessagesFromChannelContext(ctx, make(chan StreamingMessage)), WithTransport(transport))
				} else {
					messages = Query(ctx, "test", WithTransport(transport))
				}
				var got error
				for _, err := range messages {
					if err != nil {
						got = err
						continue
					}
					cancel()
					// Give the controller time to close its message channel while
					// the public iterator is still executing the caller's callback.
					time.Sleep(time.Millisecond)
				}
				cancel()
				if !errors.Is(got, context.Canceled) {
					t.Fatalf("attempt %d: got %v, want cancellation", attempt, got)
				}
			}
		})
	}
}

func TestQueryStreamCancellationWithIdleInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan StreamingMessage)
	entered := make(chan struct{})
	producerDone := make(chan struct{})
	finished := make(chan error, 1)
	messages := func(yield func(StreamingMessage) bool) {
		defer close(producerDone)
		close(entered)
		MessagesFromChannel(input)(yield)
	}
	go func() {
		var result error
		for _, err := range QueryStream(ctx, messages, WithTransport(newResultMessageTransport())) {
			if err != nil {
				result = err
			}
		}
		finished <- result
	}()
	defer func() {
		close(input) // An arbitrary producer must release its own blocking reads.
		select {
		case <-producerDone:
		case <-time.After(2 * time.Second):
			t.Error("input producer did not finish")
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("input producer did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("query waited for idle input after cancellation")
	}
}

func TestMessagesFromChannelContextCancelsIdleInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan StreamingMessage)
	finished := make(chan struct{})
	go func() {
		for range MessagesFromChannelContext(ctx, input) {
			t.Error("unexpected message")
		}
		close(finished)
	}()
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("idle channel iterator ignored cancellation")
	}
}

func TestQueryStreamEarlyExitCancelsCooperativeProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := make(chan StreamingMessage, 1)
	input <- NewUserMessage("first")
	producerDone := make(chan struct{})
	messages := func(yield func(StreamingMessage) bool) {
		defer close(producerDone)
		MessagesFromChannelContext(ctx, input)(yield)
	}
	finished := make(chan error, 1)
	go func() {
		for msg, err := range QueryStream(ctx, messages, WithTransport(newResultMessageTransport())) {
			if err != nil {
				finished <- err
				return
			}
			if _, ok := msg.(*ResultMessage); !ok {
				finished <- errors.New("expected terminal result")
				return
			}
			break
		}
		finished <- nil
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("early exit waited for idle producer")
	}
	cancel()
	select {
	case <-producerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cooperative producer did not finish")
	}
}
