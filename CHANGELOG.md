# Changelog

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
