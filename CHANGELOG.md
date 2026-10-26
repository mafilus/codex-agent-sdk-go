# Changelog

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
