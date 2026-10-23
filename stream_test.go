package codexsdk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUserMessage(t *testing.T) {
	msg := NewUserMessage("Hello, Claude!")

	assert.Equal(t, "user", msg.Type)
	assert.Equal(t, "user", msg.Message.Role)
	assert.Equal(t, "Hello, Claude!", msg.Message.Content)
	assert.Nil(t, msg.ParentToolUseID)
	assert.Empty(t, msg.SessionID)
}

func TestMessagesFromSlice_Empty(t *testing.T) {
	msgs := MessagesFromSlice([]StreamingMessage{})

	count := 0

	for range msgs {
		count++
	}

	assert.Equal(t, 0, count)
}

func TestMessagesFromSlice_Single(t *testing.T) {
	input := []StreamingMessage{NewUserMessage("Hello")}
	msgs := MessagesFromSlice(input)
	collected := make([]StreamingMessage, 0, 1)

	for msg := range msgs {
		collected = append(collected, msg)
	}

	require.Len(t, collected, 1)
	assert.Equal(t, "Hello", collected[0].Message.Content)
}

func TestMessagesFromSlice_Multiple(t *testing.T) {
	input := []StreamingMessage{
		NewUserMessage("First"),
		NewUserMessage("Second"),
		NewUserMessage("Third"),
	}
	msgs := MessagesFromSlice(input)
	collected := make([]StreamingMessage, 0, 3)

	for msg := range msgs {
		collected = append(collected, msg)
	}

	require.Len(t, collected, 3)
	assert.Equal(t, "First", collected[0].Message.Content)
	assert.Equal(t, "Second", collected[1].Message.Content)
	assert.Equal(t, "Third", collected[2].Message.Content)
}

func TestMessagesFromSlice_EarlyTermination(t *testing.T) {
	input := []StreamingMessage{
		NewUserMessage("First"),
		NewUserMessage("Second"),
		NewUserMessage("Third"),
	}
	msgs := MessagesFromSlice(input)
	count := 0

	msgs(func(_ StreamingMessage) bool {
		count++

		return count < 2 // Stop after first message
	})

	assert.Equal(t, 2, count)
}

func TestMessagesFromChannel(t *testing.T) {
	ch := make(chan StreamingMessage, 3)

	ch <- NewUserMessage("First")

	ch <- NewUserMessage("Second")

	ch <- NewUserMessage("Third")

	close(ch)

	msgs := MessagesFromChannel(ch)
	collected := make([]StreamingMessage, 0, 3)

	for msg := range msgs {
		collected = append(collected, msg)
	}

	require.Len(t, collected, 3)
	assert.Equal(t, "First", collected[0].Message.Content)
	assert.Equal(t, "Second", collected[1].Message.Content)
	assert.Equal(t, "Third", collected[2].Message.Content)
}

func TestMessagesFromChannel_Empty(t *testing.T) {
	ch := make(chan StreamingMessage)
	close(ch)

	msgs := MessagesFromChannel(ch)
	count := 0

	for range msgs {
		count++
	}

	assert.Equal(t, 0, count)
}

func TestMessagesFromChannel_EarlyTermination(t *testing.T) {
	ch := make(chan StreamingMessage, 3)

	ch <- NewUserMessage("First")

	ch <- NewUserMessage("Second")

	ch <- NewUserMessage("Third")

	close(ch)

	msgs := MessagesFromChannel(ch)
	count := 0

	msgs(func(_ StreamingMessage) bool {
		count++

		return count < 2
	})

	assert.Equal(t, 2, count)
}

func TestSingleMessage(t *testing.T) {
	msgs := SingleMessage("Hello, world!")
	collected := make([]StreamingMessage, 0, 1)

	for msg := range msgs {
		collected = append(collected, msg)
	}

	require.Len(t, collected, 1)
	assert.Equal(t, "user", collected[0].Type)
	assert.Equal(t, "user", collected[0].Message.Role)
	assert.Equal(t, "Hello, world!", collected[0].Message.Content)
}
