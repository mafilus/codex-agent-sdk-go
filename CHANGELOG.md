# Changelog

## Unreleased

## v0.2.3 - 2026-11-05

### Integration tests and CI

- Add explicit opt-in live tests using an externally supplied ChatGPT auth file
  and model; skip all live integration tests when credentials are not supplied.
- Add isolated Linux live scenarios for both query backends, streaming, multi-turn
  clients, SDK/MCP tools, cancellation and denied tools, plus offline gate checks.
- Run the integration opt-in boundary checks without credentials in public CI.
- Preserve the read-only sandbox when the live permission-mode test switches to
  `askAll`, with an offline regression check of the emitted RPC options.

### Internal lifecycle

- Remove the unused app-server mode from the exec-only `CLITransport`.
- Share transport/controller/session startup and cleanup across `Query`,
  `QueryStream` and `Client`, preserving their backend and completion rules.
- Release connection resources on startup failure, joining cleanup errors with
  the startup error while preserving optional process lifecycle observation.
- Align the reported SDK and app-server client versions with this release.

### Validation and limits

- The pre-release implementation passed all eight live scenarios, including
  both denied-tool cases, all 22 legacy integration tests, and the harness checks:
  50 tests and subtests passed, with no failures or skips, using the race detector.
- Independent final review accepted the tested candidate and received evidence
  with a bounded freeze verdict (`PASS_GEL_BORNE`).
- Live qualification is limited to Linux, Codex CLI 0.166.0 and `gpt-5.6-luna`,
  with a read-only checkout and hidden personal home. Reasoning effort was not
  overridden or independently observed. The release version metadata update
  follows that campaign; it does not represent another live execution.
- Token refresh behavior, other CLI/model/OS combinations and hosts without the
  external isolation guard are outside that qualification.

Update with:

```sh
go get github.com/mafilus/codex-agent-sdk-go@v0.2.3
```

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
