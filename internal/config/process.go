package config

import (
	"context"
	"errors"
	"time"
)

var (
	ErrProcessNotStarted  = errors.New("process has not started")
	ErrProcessUnsupported = errors.New("transport does not expose a process lifecycle")
)

// ProcessResult describes OS process termination, not a completed model turn.
// ContextErr is the launch context error observed at Wait, not proof that it
// caused the exit. StopRequested means an explicit kill request was accepted.
// No command, environment, stderr or model payload is included.
type ProcessResult struct {
	ExitCode      int
	Signal        string
	Signaled      bool
	CoreDump      bool
	StopRequested bool
	ContextErr    error
	WaitErr       error
	ExitedAt      time.Time
}

// ProcessLifecycle is an optional capability, independent of Transport.
// Done closes after the one OS Wait. ReaderDone closes after stdout decoding;
// queued notifications can still drain afterwards. Wait returns the immutable
// result even on nonzero exit; its error is only the caller's wait cancellation.
// Stop requests shutdown once and joins process and reader within ctx. A canceled
// waiter does not cancel shutdown or other observers. Explicit stop may discard
// queued events; natural process exit does not discard them.
type ProcessLifecycle interface {
	Done() <-chan struct{}
	ReaderDone() <-chan struct{}
	Wait(context.Context) (ProcessResult, error)
	Stop(context.Context) error
}

// ProcessLifecycleProvider leaves custom transports and existing client fakes
// source-compatible. Before successful process creation, return NotStarted.
type ProcessLifecycleProvider interface {
	ProcessLifecycle() (ProcessLifecycle, error)
}
