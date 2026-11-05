//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// TestPartialMessages_DisabledByDefault verifies no StreamEvents when disabled.
func TestPartialMessages_DisabledByDefault(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var streamEventCount int

	for msg, err := range liveQuery(ctx, "Say 'hello'",
		codexsdk.WithPermissionMode("default"),
	) {
		if err != nil {
			t.Fatalf("Query failed: %v", safeLiveError(err))
		}

		if _, ok := msg.(*codexsdk.StreamEvent); ok {
			streamEventCount++
		}
	}

	require.Equal(t, 0, streamEventCount,
		"Should not receive StreamEvents when IncludePartialMessages is false")
}
