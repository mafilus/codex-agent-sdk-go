# Local SDK fork

Created from the Strix dependency `codex-agent-sdk-go` version `v0.0.1`
on 2026-10-23. Original publisher: ethpandaops. The original GPL-3.0
license is retained in `LICENSE`.

The module path and internal imports are now
`github.com/mafilus/codex-agent-sdk-go`. The sole remote is
`git@github.com:mafilus/codex-agent-sdk-go.git`. There is no upstream remote,
and the original module is not a dependency. Other third-party module
dependencies remain in `go.mod`.

Corrections:

- Drain protocol messages when the transport error channel closes normally,
  preserving buffered terminal results.
- Queue app-server notifications and requests rather than dropping them when
  delivery buffers fill. Keep RPC response processing independent of slow event
  consumers. Drain queues on EOF and stop delivery on explicit shutdown.
- Return from `QueryStream` on cancellation or early exit without waiting for
  an arbitrary input iterator. Provide `MessagesFromChannelContext` for
  cooperative channel input. Custom producers must honor their context or
  otherwise unblock themselves to release their goroutine.
- Preserve the cancellation error in `Query` and `QueryStream` when controller
  shutdown races with cancellation during the caller's message callback.

Regression coverage uses local synthetic transports, with no external Codex
backend. Runtime and Live integration are separate validation levels.

Run `make test-offline` for the full default suite with the race detector and
the Python 3 fixture in `scripts/test-fixtures/codex`. The fixture synthesizes
the app-server initialize/thread/turn responses; it does not run a model or
prove compatibility with a real Codex backend.

Validation of the local corrections:

- Full default suite with `make test-offline`: passed, including `-race`, shuffle
  and the synthetic Codex process.
- `go vet ./...` and `go mod verify`: passed.
- Two independent agent reviews; the stream review found the cancellation race
  above, which was patched and counter-reviewed without a remaining finding.
- `make lint` could not run because `golangci-lint` is not installed.

The event queues grow with backlog. Arbitrary input iterators that ignore their
context cannot be forcibly stopped; cancel cooperative producer contexts on
early exit. No real Codex backend or external Live scenario was run.
