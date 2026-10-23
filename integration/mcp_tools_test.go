//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// TestMCPTools_Registration tests tool registration with the MCP server.
func TestMCPTools_Registration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	echoTool := codexsdk.NewSdkMcpTool(
		"test_echo",
		"Echoes the input message back",
		codexsdk.SimpleSchema(map[string]string{
			"message": "string",
		}),
		func(_ context.Context, req *codexsdk.CallToolRequest) (*codexsdk.CallToolResult, error) {
			args, err := codexsdk.ParseArguments(req)
			if err != nil {
				return codexsdk.ErrorResult(err.Error()), nil
			}

			msg, _ := args["message"].(string)

			return codexsdk.TextResult(fmt.Sprintf(`{"echo": %q}`, msg)), nil
		},
	)

	server := codexsdk.CreateSdkMcpServer("sdk", "1.0.0", echoTool)
	receivedResult := false

	for msg, err := range codexsdk.Query(ctx, "Say hello",
		codexsdk.WithPermissionMode("bypassPermissions"),
		codexsdk.WithMCPServers(map[string]codexsdk.MCPServerConfig{
			"sdk": server,
		}),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		if result, ok := msg.(*codexsdk.ResultMessage); ok {
			receivedResult = true
			require.False(t, result.IsError, "Query should not result in error")
		}
	}

	require.True(t, receivedResult, "Should complete successfully with registered tool")
}

// TestSDKTools_Registration tests high-level Tool registration with WithSDKTools.
func TestSDKTools_Registration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	echoTool := codexsdk.NewTool(
		"test_echo",
		"Echoes the input message back",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Message to echo",
				},
			},
			"required": []string{"message"},
		},
		func(_ context.Context, input map[string]any) (map[string]any, error) {
			msg, _ := input["message"].(string)

			return map[string]any{
				"echo": msg,
			}, nil
		},
	)

	receivedResult := false

	for msg, err := range codexsdk.Query(ctx, "Say hello",
		codexsdk.WithPermissionMode("bypassPermissions"),
		codexsdk.WithSDKTools(echoTool),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		if result, ok := msg.(*codexsdk.ResultMessage); ok {
			receivedResult = true
			require.False(t, result.IsError, "Query should not result in error")
		}
	}

	require.True(t, receivedResult, "Should complete successfully with registered tool")
}

// TestSDKTools_Execution tests high-level Tool called with correct input.
func TestSDKTools_Execution(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var toolExecuted bool
	var receivedInput string

	calculatorTool := codexsdk.NewTool(
		"add_numbers",
		"Adds two numbers together and returns the result",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{
					"type":        "number",
					"description": "First number",
				},
				"b": map[string]any{
					"type":        "number",
					"description": "Second number",
				},
			},
			"required": []string{"a", "b"},
		},
		func(_ context.Context, input map[string]any) (map[string]any, error) {
			toolExecuted = true
			a, _ := input["a"].(float64)
			b, _ := input["b"].(float64)
			receivedInput = fmt.Sprintf("a=%g, b=%g", a, b)
			t.Logf("Tool executed with a=%v, b=%v", a, b)

			return map[string]any{
				"result": a + b,
			}, nil
		},
	)

	for _, err := range codexsdk.Query(ctx,
		"Use the add_numbers tool to add 5 and 3",
		codexsdk.WithPermissionMode("bypassPermissions"),
		codexsdk.WithSDKTools(calculatorTool),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}
	}

	require.True(t, toolExecuted, "Tool should have been executed")
	t.Logf("Received input: %s", receivedInput)
}

// TestSDKTools_ReturnValue tests high-level Tool result used by the agent.
func TestSDKTools_ReturnValue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var toolExecuted bool
	expectedResult := 42.0

	magicTool := codexsdk.NewTool(
		"get_magic_number",
		"Returns a magic number",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		func(_ context.Context, _ map[string]any) (map[string]any, error) {
			toolExecuted = true

			return map[string]any{
				"number": expectedResult,
			}, nil
		},
	)

	var mentionedNumber bool

	for msg, err := range codexsdk.Query(ctx,
		"Use the get_magic_number tool and tell me what number it returns",
		codexsdk.WithPermissionMode("bypassPermissions"),
		codexsdk.WithSDKTools(magicTool),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		if assistantMsg, ok := msg.(*codexsdk.AssistantMessage); ok {
			for _, block := range assistantMsg.Content {
				if textBlock, ok := block.(*codexsdk.TextBlock); ok {
					t.Logf("Response: %s", textBlock.Text)

					if contains42(textBlock.Text) {
						mentionedNumber = true
					}
				}
			}
		}
	}

	require.True(t, toolExecuted, "Tool should have been executed")
	require.True(t, mentionedNumber, "Agent should mention the returned number (42)")
}
