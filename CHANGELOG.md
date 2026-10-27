# Changelog

## v0.2.2 - 2026-10-27

### Fixed

- Enforce tool permission policies before executing SDK dynamic tools, including
  explicit denials, callback failures, and updated input.
- Preserve native failed and interrupted turn status instead of reporting success.
- Make waiting RPC writes cancellable; cancellation during a write stops the
  transport to avoid reusing a partially written JSON frame.
- Preserve buffered RPC responses when the reader reaches EOF.
- Release pending control requests after serialization and send failures.
- Confine all memory-example operations and its final display to a pinned
  filesystem root, preserving exact file names and rejecting external symlinks.
- Omit command argument values from startup logs to avoid logging MCP secrets.
- Align the reported SDK and app-server client versions with this release.

### CI and documentation

- Run tests, lint, and typo checks on pushes to main with the latest stable Go.
- Keep correctness-focused lint checks and document the remaining ambiguity of
  MCP permission names containing `__`.
- Publish GitHub releases from version tags using the matching changelog entry.

### Validation and limits

- Offline tests with the race detector, randomized test order, targeted regression
  tests, build, lint, and scope-specific independent reviews passed.
- No live model backend qualification is claimed. MCP secret values can still
  appear in process arguments; only their startup logging is removed.
- MCP names containing `__` can still collide in permission callbacks and lists;
  avoid these names when relying on per-tool permissions.

Update with:

```sh
go get github.com/mafilus/codex-agent-sdk-go@v0.2.2
```

## v0.2.1 - 2026-10-26

### Dependencies

- Update the MCP Go SDK from `v1.3.0` to `v1.8.0`, including upstream
  security fixes and transport hardening.
- Update JSON Schema, Logrus, Testify, SQLite, and their transitive dependencies.
- Update `golang.org/x` dependencies while retaining the Go `1.26.0` minimum.

### Fixed

- Replace the removed Codex CLI `--full-auto` flag with explicit approval and
  sandbox settings, preserving the default workspace-write sandbox.
- Advertise in-process MCP server tools through the app-server dynamic-tool API
  so Codex can discover and call their SDK handlers.
- Fix Windows compilation of process lifecycle observation by using a portable
  zero value for `syscall.WaitStatus`.

Update with:

```sh
go get github.com/mafilus/codex-agent-sdk-go@v0.2.1
```

## v0.2.0 - 2026-10-26

### Fixed

- Completed `file_change` events now emit a `ToolResultBlock` matching the
  `Edit` or `Write` tool-use identifier. Previously, they emitted only a
  `ToolUseBlock`, leaving consumers without a tool completion result.
- File changes with status `failed` or `declined` now produce an error result.
  Started and updated events do not emit a completion result.

Regression tests cover Edit/Write lifecycle events, string and object change
kinds, failure statuses, and payloads without a status.

Update with:

```sh
go get github.com/mafilus/codex-agent-sdk-go@v0.2.0
```
