package codexsdk

import (
	"context"
	"iter"
)

// MessagesFromSlice creates a MessageStream from a slice of StreamingMessages.
// This is useful for sending a fixed set of messages in streaming mode.
func MessagesFromSlice(msgs []StreamingMessage) iter.Seq[StreamingMessage] {
	return func(yield func(StreamingMessage) bool) {
		for _, msg := range msgs {
			if !yield(msg) {
				return
			}
		}
	}
}

// MessagesFromChannel creates a MessageStream from a channel.
// This is useful for dynamic message generation where messages are produced over time.
// The iterator completes when the channel is closed.
// Use MessagesFromChannelContext when the channel can remain idle and open.
func MessagesFromChannel(ch <-chan StreamingMessage) iter.Seq[StreamingMessage] {
	return func(yield func(StreamingMessage) bool) {
		for msg := range ch {
			if !yield(msg) {
				return
			}
		}
	}
}

// MessagesFromChannelContext creates a channel-backed input iterator that stops
// when ctx is canceled, including while the channel is idle. Pass the query's
// context and cancel it when abandoning a query before its deadline.
func MessagesFromChannelContext(ctx context.Context, ch <-chan StreamingMessage) iter.Seq[StreamingMessage] {
	return func(yield func(StreamingMessage) bool) {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok || !yield(msg) {
					return
				}
			}
		}
	}
}

// SingleMessage creates a MessageStream with a single user message.
// This is a convenience function for simple string prompts in streaming mode.
func SingleMessage(content string) iter.Seq[StreamingMessage] {
	return MessagesFromSlice([]StreamingMessage{NewUserMessage(content)})
}

// NewUserMessage creates a StreamingMessage with type "user".
// This is a convenience constructor for creating user messages.
func NewUserMessage(content string) StreamingMessage {
	return StreamingMessage{
		Type: "user",
		Message: StreamingMessageContent{
			Role:    "user",
			Content: content,
		},
	}
}
