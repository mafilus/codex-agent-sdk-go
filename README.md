# Codex Agent SDK Go

Go SDK for building agentic applications with the [Codex CLI](https://github.com/openai/codex).
It provides:

- `Query()` for one-shot requests
- `QueryStream()` for multi-message streaming input
- `Client` for stateful multi-turn sessions

## Requirements

- Go 1.26+
- [Codex CLI](https://github.com/openai/codex) v0.103.0+ in `PATH`

Compatibility constants are defined in `version.go`:

- `Version`
- `MinimumCLIVersion`

## Installation

This project is a fork with module path `github.com/mafilus/codex-agent-sdk-go`.

```bash
go get github.com/mafilus/codex-agent-sdk-go@main
```

For local development, use a replacement in the consuming project's `go.mod`
(adjust the path):

```go
require github.com/mafilus/codex-agent-sdk-go v0.0.0

replace github.com/mafilus/codex-agent-sdk-go => ../codex-agent-sdk-go
```

Import it as `codexsdk "github.com/mafilus/codex-agent-sdk-go"`.
See [LOCAL-FORK.md](LOCAL-FORK.md) for provenance and local fixes.

Run `make test-offline` for the full default test suite with a synthetic Codex
process, Python 3 and the race detector. This does not use an external backend.

### SDK-owned app-server process

Stateful clients launch the effective `codex app-server --stdio` process. The
SDK owns its protocol pipes and is the only caller of OS Wait, including on
spontaneous exit. There is no second supervisory Codex process.

Process observation is optional, leaving the `Client` interface and custom
transport implementations unchanged:

```go
provider, ok := client.(codexsdk.ProcessLifecycleProvider)
if !ok {
    return codexsdk.ErrProcessUnsupported
}
process, err := provider.ProcessLifecycle() // after client.Start
if err != nil {
    return err
}
result, err := process.Wait(ctx)
if err != nil { // this observer's context, not the process exit code
    return err
}
// Inspect result.ExitCode, Signaled, Signal, WaitErr and ContextErr.
// process.Stop(ctx) requests shutdown and joins the process/reader.
```

Before process creation, observation returns `ErrProcessNotStarted`, including
failed discovery/spawn. Failed initialization after spawn still exposes the
terminated process. Non-process custom transports return `ErrProcessUnsupported`.
`Done` signals OS termination; `ReaderDone` signals stdout decoding completion,
not delivery of all queued events or a successful model turn. Natural exit
preserves buffered terminal events. Explicit Stop/Close may discard pending
events and must not be substituted for consuming a successful turn result.

`Wait(ctx)` returns an immutable process result (including nonzero exit) without
changing the process. Canceling one waiter does not cancel other observers.
`Stop(ctx)` initiates one shared shutdown, then bounds that caller's join;
shutdown continues if the waiter cancels. Do not assume EOF alone stops Codex.
Concurrent `Close` calls join the same cleanup and return its stable error.
Close has no caller deadline; use the optional process Stop capability for a
bounded process/reader join, then Close the client to finish client resources.
`StopRequested` records an accepted explicit kill request, not proof that it
caused an earlier spontaneous exit. `ContextErr` is observed at OS Wait, not a
causal attribution. Results contain no argv, environment, stderr or credentials.

### Streaming input ownership

Use `MessagesFromChannelContext(ctx, input)` for input channels that may remain
open and idle, and cancel that context when leaving a query early. `QueryStream`
returns on cancellation or early exit without waiting for arbitrary input code.
A custom iterator that ignores cancellation remains responsible for releasing
its blocking operations; Go cannot forcibly stop that producer.

App-server events are queued in order instead of dropped when the delivery
channels fill. The queues drain on EOF and are discarded on explicit shutdown.
Their memory use grows with the backlog, so consume events continuously.

Cancelling an RPC while it waits for the writer leaves the transport open.
Cancelling during a write closes the transport and stops its owned process,
because a partially written JSON frame cannot safely be reused. Create a new
client after this kind of cancellation.

Native terminal turns preserve failure and interruption status in
`ResultMessage`: failed turns have subtype `error`, interrupted turns have
subtype `interrupted`, and both set `IsError` to true.

Startup logs omit command argument values. MCP headers and environment values
can still appear in the CLI process arguments; configure host access to process
information accordingly.

## Quick Start

### One-shot query

```go
package main

import (
	"context"
	"fmt"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for msg, err := range codexsdk.Query(ctx, "What is 2 + 2?") {
		if err != nil {
			fmt.Printf("query error: %v\n", err)
			return
		}

		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if text, ok := block.(*codexsdk.TextBlock); ok {
					fmt.Println(text.Text)
				}
			}
		case *codexsdk.ResultMessage:
			if m.Usage != nil {
			fmt.Printf("done — %d input, %d output tokens\n", m.Usage.InputTokens, m.Usage.OutputTokens)
		}
		}
	}
}
```

### Multi-turn client

```go
client := codexsdk.NewClient()
defer func() {
	if err := client.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to close client: %v\n", err)
	}
}()

if err := client.Start(ctx,
	codexsdk.WithLogger(slog.Default()),
	codexsdk.WithPermissionMode("acceptEdits"),
); err != nil {
	return err
}

if err := client.Query(ctx, "What's the capital of France?"); err != nil {
	return err
}
for msg, err := range client.ReceiveResponse(ctx) {
	if err != nil {
		return err
	}
	_ = msg
}

if err := client.Query(ctx, "What's the population of that city?"); err != nil {
	return err
}
for msg, err := range client.ReceiveResponse(ctx) {
	if err != nil {
		return err
	}
	_ = msg
}
```

### WithClient helper

```go
err := codexsdk.WithClient(ctx, func(c codexsdk.Client) error {
	if err := c.Query(ctx, "Hello Codex"); err != nil {
		return err
	}

	for msg, err := range c.ReceiveResponse(ctx) {
		if err != nil {
			return err
		}
		_ = msg
	}

	return nil
},
	codexsdk.WithLogger(slog.Default()),
	codexsdk.WithPermissionMode("acceptEdits"),
)
if err != nil {
	return err
}
```

## API Overview

### Top-level entrypoints

| API | Description |
|---|---|
| `Query(ctx, prompt, opts...)` | One-shot query returning `iter.Seq2[Message, error]` |
| `QueryStream(ctx, messages, opts...)` | Streams `StreamingMessage` input and yields `Message` output |
| `NewClient()` | Creates a stateful client for interactive sessions |
| `WithClient(ctx, fn, opts...)` | Helper that runs `Start()` + callback + `Close()` |
| `StatSession(ctx, sessionID, opts...)` | Read session metadata from local SQLite database |

### `Client` methods

| Method | Description |
|---|---|
| `Start(ctx, opts...)` | Connect and initialize a session |
| `StartWithPrompt(ctx, prompt, opts...)` | Start and immediately send first prompt |
| `StartWithStream(ctx, messages, opts...)` | Start and immediately stream input messages |
| `Query(ctx, prompt, sessionID...)` | Send a user turn |
| `ReceiveResponse(ctx)` | Read messages until `ResultMessage` |
| `ReceiveMessages(ctx)` | Continuous message stream |
| `Interrupt(ctx)` | Stop in-flight generation |
| `SetPermissionMode(ctx, mode)` | Change permission mode during a session |
| `SetModel(ctx, model)` | Change model during a session |
| `GetMCPStatus(ctx)` | Fetch live MCP server connection status |
| `RewindFiles(ctx, userMessageID)` | Rewind tracked files to earlier turn |
| `Close()` | Close and release resources |

### Message and content types

- Message types: `AssistantMessage`, `UserMessage`, `SystemMessage`, `ResultMessage`, `StreamEvent`
- Content blocks: `TextBlock`, `ThinkingBlock`, `ToolUseBlock`, `ToolResultBlock`

### Stream helpers

| Helper | Description |
|---|---|
| `SingleMessage(content)` | Single-message input stream |
| `MessagesFromSlice(msgs)` | Stream from `[]StreamingMessage` |
| `MessagesFromChannel(ch)` | Stream from channel |
| `NewUserMessage(content)` | Convenience `StreamingMessage` constructor |

## Options and Backend Behavior

Options are backend-dependent. Unsupported combinations fail fast with `ErrUnsupportedOption`.

### Backend selection

- `Query(...)` auto-selects backend:
  - Uses `exec` when all selected options are natively supported there.
  - Falls back to `app-server` when needed for selected options.
- `QueryStream(...)` uses app-server semantics unless you inject a custom `WithTransport(...)`.
- `Client.Start(...)` uses app-server transport semantics.

### Common options

| Option | Purpose |
|---|---|
| `WithLogger(logger)` | SDK logs (`*slog.Logger`) |
| `WithModel("o4-mini")` | Model selection |
| `WithCwd("/path")` / `WithCliPath("/path/codex")` / `WithEnv(...)` | Process/runtime setup |
| `WithPermissionMode("acceptEdits")` / `WithSandbox("workspace-write")` | Permission/sandbox behavior |
| `WithSystemPrompt("...")` / `WithSystemPromptPreset(...)` | System instructions |
| `WithImages(...)` / `WithConfig(...)` | Codex-native CLI inputs/config |
| `WithOutputSchema(json)` | Passes `--output-schema` |
| `WithOutputFormat(map[string]any{...})` | Structured output wrapper/schema for app-server flow |
| `WithSkipVersionCheck(true)` | Skip CLI version check |
| `WithInitializeTimeout(d)` | Initialize control request timeout |
| `WithStderr(func(string){...})` | Stderr callback |
| `WithTransport(customTransport)` | Custom transport injection |

### Tool and MCP options

| Option | Purpose |
|---|---|
| `WithHooks(hooks)` | Hook callbacks for tool/session events |
| `WithCanUseTool(callback)` | Per-tool permission callback |
| `WithOnUserInput(callback)` | Handle agent's `requestUserInput` questions (plan mode) |
| `WithTools(...)` / `WithAllowedTools(...)` / `WithDisallowedTools(...)` | Tool allow/block policy |
| `WithPermissionPromptToolName("stdio")` | Permission prompt tool name |
| `WithMCPServers(...)` | Register MCP servers |
| `WithSDKTools(tools...)` | Register SDK-defined tools via `NewTool` (uses `dynamicTools` API) |

MCP permission names currently use `mcp__<server>__<tool>`. Names containing
`__` can collide: server `a` with tool `b__c` and server `a__b` with tool `c`
both become `mcp__a__b__c`. Distinct wire aliases do not resolve this ambiguity
in permission callbacks or allow/block lists. Avoid `__` in registered server
and tool names when relying on per-tool permissions until this limitation is
resolved.

### Session and advanced options

| Option | Purpose |
|---|---|
| `WithResume("session-id")` / `WithForkSession(true)` | Resume/fork sessions |
| `WithContinueConversation(true)` | Continue prior conversation |
| `WithEffort(codexsdk.EffortHigh)` | Extended thinking effort |
| `WithIncludePartialMessages(true)` | Emit streaming deltas as `StreamEvent` |
| `WithAddDirs("/extra/path")` | Additional accessible directories |
| `WithExtraArgs(map[string]*string{...})` | Raw CLI flags |
| `WithCodexHome("/path/.codex")` | Override Codex home directory for `StatSession` |

### Important caveats

- `WithContinueConversation(true)` requires `WithResume(...)` on app-server paths.
- `WithPermissionPromptToolName(...)` only supports `"stdio"` on app-server paths.
- `WithAddDirs(...)` and `WithExtraArgs(...)` are unsupported on app-server paths.
- `WithOutputSchema(...)` and `WithOutputFormat(...)` serve different integration styles; choose one based on how you want structured output surfaced.

## Session Metadata

Read metadata from a local Codex session using `StatSession`:

```go
stat, err := codexsdk.StatSession(ctx, "550e8400-e29b-41d4-a716-446655440000",
	codexsdk.WithCodexHome("/custom/.codex"), // optional
	codexsdk.WithCwd("/home/user/project"),   // optional: filter by project
)
if err != nil {
	if errors.Is(err, codexsdk.ErrSessionNotFound) {
		log.Fatal("session not found")
	}
	log.Fatal(err)
}
fmt.Printf("Session: %s (tokens: %d)\n", stat.Title, stat.TokensUsed)
```

`StatSession` reads from the Codex CLI's local SQLite database (`~/.codex/state_5.sqlite`).
No running CLI instance is required.

## Error Handling

```go
for msg, err := range codexsdk.Query(ctx, prompt) {
	if err != nil {
		var cliErr *codexsdk.CLINotFoundError
		if errors.As(err, &cliErr) {
			log.Fatalf("codex CLI not found: %v", cliErr.SearchedPaths)
		}

		var procErr *codexsdk.ProcessError
		if errors.As(err, &procErr) {
			log.Fatalf("codex failed (exit %d): %s", procErr.ExitCode, procErr.Stderr)
		}

		if errors.Is(err, codexsdk.ErrUnsupportedOption) {
			log.Fatalf("unsupported option combination: %v", err)
		}

		log.Fatal(err)
	}

	_ = msg
}
```

| Error | Description |
|---|---|
| `CLINotFoundError` | Codex CLI binary not found |
| `CLIConnectionError` | Connection/init failure |
| `ProcessError` | CLI process exited with error |
| `MessageParseError` | Failed to parse SDK message payload |
| `CLIJSONDecodeError` | JSON decode failure from CLI output |
| `ErrUnsupportedOption` | Option/backend combination is unsupported |
| `ErrSessionNotFound` | Session not found in local database |

## Examples

| Example | Description |
|---|---|
| [`quick_start`](./examples/quick_start) | Basic `Query()` usage |
| [`client_multi_turn`](./examples/client_multi_turn) | Stateful multi-turn client patterns |
| [`query_stream`](./examples/query_stream) | `QueryStream()` with streaming inputs |
| [`mcp_calculator`](./examples/mcp_calculator) | In-process MCP server tools |
| [`mcp_status`](./examples/mcp_status) | Querying MCP server status |
| [`tool_permission_callback`](./examples/tool_permission_callback) | `WithCanUseTool` permission callback |
| [`user_input_callback`](./examples/user_input_callback) | `WithOnUserInput` user input callback (plan mode) |
| [`tools_option`](./examples/tools_option) | Tool allow/block configuration |
| [`structured_output`](./examples/structured_output) | Structured output patterns |
| [`extended_thinking`](./examples/extended_thinking) | `WithEffort(...)` usage |
| [`sessions`](./examples/sessions) | Resume/fork session behavior |
| [`parallel_queries`](./examples/parallel_queries) | Concurrent one-shot queries |
| [`pipeline`](./examples/pipeline) | Multi-step orchestration flow |
| [`sdk_tools`](./examples/sdk_tools) | SDK-defined tools with `WithSDKTools` |
| [`include_partial_messages`](./examples/include_partial_messages) | Real-time streaming deltas with `WithIncludePartialMessages` |
| [`error_handling`](./examples/error_handling) | Typed error handling |

Run examples:

```bash
go run ./examples/quick_start
go run ./examples/client_multi_turn basic_streaming
go run ./examples/query_stream
```

## Build and Test

```bash
go build ./...
go test ./...
go test -race ./...
go test -tags=integration ./... # requires Codex CLI + working runtime environment
golangci-lint run
```
