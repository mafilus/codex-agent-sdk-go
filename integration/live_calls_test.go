package integration

import (
	"context"
	"errors"
	"os"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// safeLiveError preserves errors.Is/As without retaining raw backend diagnostics
// in the printable error. In particular, rejected credential fragments and stderr
// must not become test artifacts through require.NoError or t.Logf.
func safeLiveError(err error) error {
	if err == nil {
		return nil
	}
	return &liveError{cause: err}
}

type liveError struct{ cause error }

func (e *liveError) Error() string {
	switch {
	case errors.Is(e.cause, context.Canceled):
		return "live operation canceled"
	case errors.Is(e.cause, context.DeadlineExceeded):
		return "live operation timed out"
	default:
		return "live operation failed; raw backend diagnostics withheld"
	}
}
func (e *liveError) Unwrap() error { return e.cause }

func liveOptions(opts ...codexsdk.Option) []codexsdk.Option {
	var options codexsdk.CodexAgentOptions
	for _, opt := range opts {
		opt(&options)
	}
	result := append([]codexsdk.Option{}, opts...)
	result = append(result, codexsdk.WithModel(os.Getenv(liveModelVariable)), codexsdk.WithSandbox("read-only"))
	if callback := options.Stderr; callback != nil {
		result = append(result, codexsdk.WithStderr(func(string) { callback("stderr line received; contents withheld") }))
	}
	return result
}
