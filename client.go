package codexsdk

import (
	"context"
	"iter"
)

// Client provides an interactive, stateful interface for multi-turn conversations.
//
// Unlike the one-shot Query() function, Client maintains session state across
// multiple exchanges. It supports interruption and bidirectional communication
// with the Codex CLI.
//
// Lifecycle: Clients are single-use. After Close(), create a new client with NewClient().
//
// Example usage:
//
//	client := NewClient()
//	defer func() {
//	    if err := client.Close(); err != nil {
//	        log.Printf("failed to close client: %v", err)
//	    }
//	}()
//
//	err := client.Start(ctx,
//	    WithLogger(slog.Default()),
//	    WithPermissionMode("acceptEdits"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Send a query
//	err = client.Query(ctx, "What is 2+2?")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Receive all messages for this response (stops at ResultMessage)
//	for msg, err := range client.ReceiveResponse(ctx) {
//	    if err != nil {
//	        log.Fatal(err)
//	    }
//	    // Process message...
//	}
//
//	// Or receive messages indefinitely (for continuous streaming)
//	for msg, err := range client.ReceiveMessages(ctx) {
//	    if err != nil {
//	        break
//	    }
//	    // Process message...
//	}
type Client interface {
	// Start establishes a connection to the Codex CLI.
	// Must be called before any other methods.
	// Returns CLINotFoundError if CLI not found, CLIConnectionError on failure.
	Start(ctx context.Context, opts ...Option) error

	// StartWithPrompt establishes a connection and immediately sends an initial prompt.
	// Equivalent to calling Start() followed by Query(ctx, prompt).
	// The prompt is sent to the "default" session.
	// Returns CLINotFoundError if CLI not found, CLIConnectionError on failure.
	StartWithPrompt(ctx context.Context, prompt string, opts ...Option) error

	// StartWithStream establishes a connection and streams initial messages.
	// Messages are consumed from the iterator and sent via stdin.
	// The iterator runs in a separate goroutine; use context cancellation to abort.
	// EndInput is called automatically when the iterator completes.
	// Returns CLINotFoundError if CLI not found, CLIConnectionError on failure.
	StartWithStream(ctx context.Context, messages iter.Seq[StreamingMessage], opts ...Option) error

	// Query sends a user prompt to the agent.
	// Returns immediately after sending; use ReceiveMessages() or ReceiveResponse() to get responses.
	// Optional sessionID defaults to "default" for multi-session support.
	Query(ctx context.Context, prompt string, sessionID ...string) error

	// ReceiveMessages returns an iterator that yields messages indefinitely.
	// Messages are yielded as they arrive until EOF, an error occurs, or context is cancelled.
	// Unlike ReceiveResponse, this iterator does not stop at ResultMessage.
	// Use iter.Pull2 if you need pull-based iteration instead of range.
	ReceiveMessages(ctx context.Context) iter.Seq2[Message, error]

	// ReceiveResponse returns an iterator that yields messages until a ResultMessage is received.
	// Messages are yielded as they arrive for streaming consumption.
	// The iterator stops after yielding the ResultMessage.
	// Use iter.Pull2 if you need pull-based iteration instead of range.
	// To collect all messages into a slice, use slices.Collect or a simple loop.
	ReceiveResponse(ctx context.Context) iter.Seq2[Message, error]

	// Interrupt sends an interrupt signal to stop the agent's current processing.
	Interrupt(ctx context.Context) error

	// SetPermissionMode changes the permission mode during conversation.
	// Valid modes: "default", "acceptEdits", "plan", "bypassPermissions"
	SetPermissionMode(ctx context.Context, mode string) error

	// SetModel changes the AI model during conversation.
	// Pass nil to use the default model.
	SetModel(ctx context.Context, model *string) error

	// GetServerInfo returns server initialization info including available commands.
	// Returns nil if not connected or not in streaming mode.
	GetServerInfo() map[string]any

	// GetMCPStatus queries the CLI for live MCP server connection status.
	// Returns the status of all configured MCP servers.
	GetMCPStatus(ctx context.Context) (*MCPStatus, error)

	// ListModels queries the CLI for available models.
	// Returns the list of models the CLI can use.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// RewindFiles rewinds tracked files to their state at a specific user message.
	// The userMessageID should be the ID of a previous user message in the conversation.
	RewindFiles(ctx context.Context, userMessageID string) error

	// Close terminates the session and cleans up resources.
	// After Close(), the client cannot be reused. Safe to call multiple times.
	Close() error
}

// NewClient creates a new interactive client.
//
// Call Start() with options to begin a session:
//
//	client := NewClient()
//	err := client.Start(ctx,
//	    WithLogger(slog.Default()),
//	    WithPermissionMode("acceptEdits"),
//	)
func NewClient() Client {
	return newClientImpl()
}
