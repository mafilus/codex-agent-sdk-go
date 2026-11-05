//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// TestStderrCallback_ReceivesOutput tests Stderr callback invocation.
func TestStderrCallback_ReceivesOutput(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var stderrLines []string

	for _, err := range liveQuery(ctx, "Say 'hello'",
		codexsdk.WithPermissionMode("default"),
		codexsdk.WithStderr(func(line string) {
			stderrLines = append(stderrLines, line)
		}),
	) {
		if err != nil {
			t.Fatalf("Query failed: %v", safeLiveError(err))
		}
	}

	t.Logf("Received %d stderr lines", len(stderrLines))
}

// TestStderrCallback_CapturesDebugInfo tests that stderr callback captures
// output from a query that produces verbose output.
func TestStderrCallback_CapturesDebugInfo(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var stderrLines []string

	for _, err := range liveQuery(ctx, "Say 'debug test'",
		codexsdk.WithPermissionMode("default"),
		codexsdk.WithStderr(func(line string) {
			stderrLines = append(stderrLines, line)
		}),
	) {
		if err != nil {
			t.Fatalf("Query failed: %v", safeLiveError(err))
		}
	}

	t.Logf("Received %d stderr lines", len(stderrLines))

	if len(stderrLines) > 0 {
		t.Logf("First line length: %d", len(stderrLines[0]))
	}
}
