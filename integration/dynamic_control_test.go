//go:build integration

package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// TestDynamicControl_SetPermissionMode tests changing permission mode mid-session.
func TestDynamicControl_SetPermissionMode(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client := newLiveClient()
	defer client.Close()

	err := client.Start(ctx,
		codexsdk.WithPermissionMode("default"),
	)
	if err != nil {
		t.Fatalf("Connect failed: %v", safeLiveError(err))
	}

	// Change approval behavior while retaining the thread's read-only sandbox.
	err = client.SetPermissionMode(ctx, "askAll")
	require.NoError(t, safeLiveError(err), "SetPermissionMode should succeed")

	err = client.Query(ctx, "Say 'permission changed'")
	require.NoError(t, safeLiveError(err), "Query should succeed after SetPermissionMode")

	var messages []codexsdk.Message
	for msg, err := range client.ReceiveResponse(ctx) {
		require.NoError(t, safeLiveError(err), "ReceiveResponse should succeed")
		messages = append(messages, msg)
	}

	require.NotEmpty(t, messages, "Should receive messages")
}

// TestDynamicControl_SetModel reapplies the explicitly selected live model.
func TestDynamicControl_SetModel(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client := newLiveClient()
	defer client.Close()

	err := client.Start(ctx,
		codexsdk.WithPermissionMode("default"),
	)
	if err != nil {
		t.Fatalf("Connect failed: %v", safeLiveError(err))
	}

	err = client.SetModel(ctx, new(os.Getenv(liveModelVariable)))
	require.NoError(t, safeLiveError(err), "SetModel should succeed")

	err = client.Query(ctx, "Say 'model changed'")
	require.NoError(t, safeLiveError(err), "Query should succeed after SetModel")

	var messages []codexsdk.Message
	for msg, err := range client.ReceiveResponse(ctx) {
		require.NoError(t, safeLiveError(err), "ReceiveResponse should succeed")
		messages = append(messages, msg)
	}

	require.NotEmpty(t, messages, "Should receive messages")
}

// TestDynamicControl_Interrupt tests interrupting a long-running query.
func TestDynamicControl_Interrupt(t *testing.T) {
	requireLiveBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client := newLiveClient()
	defer client.Close()

	err := client.Start(ctx,
		codexsdk.WithPermissionMode("default"),
	)
	if err != nil {
		t.Fatalf("Connect failed: %v", safeLiveError(err))
	}

	err = client.Query(ctx,
		"Write a very long essay about the history of computing, including many details.")
	require.NoError(t, safeLiveError(err), "Query should succeed")

	time.Sleep(500 * time.Millisecond)

	err = client.Interrupt(ctx)
	require.NoError(t, safeLiveError(err), "Interrupt should succeed")

	for msg, err := range client.ReceiveMessages(ctx) {
		if err != nil {
			break
		}

		if result, ok := msg.(*codexsdk.ResultMessage); ok {
			t.Logf("Session ended: isError=%v", result.IsError)

			break
		}
	}
}
