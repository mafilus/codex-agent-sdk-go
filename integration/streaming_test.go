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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var streamEventCount int

	for msg, err := range codexsdk.Query(ctx, "Say 'hello'",
		codexsdk.WithPermissionMode("acceptAll"),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		if _, ok := msg.(*codexsdk.StreamEvent); ok {
			streamEventCount++
		}
	}

	require.Equal(t, 0, streamEventCount,
		"Should not receive StreamEvents when IncludePartialMessages is false")
}
