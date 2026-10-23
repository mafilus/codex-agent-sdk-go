package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// ToolUsageLog tracks tool usage for demonstration.
type ToolUsageLog struct {
	Tool        string
	Input       map[string]any
	Suggestions []*codexsdk.PermissionUpdate
}

var toolUsageLog []ToolUsageLog

// displayMessage standardizes message display across examples.
func displayMessage(msg codexsdk.Message) {
	switch m := msg.(type) {
	case *codexsdk.UserMessage:
		for _, block := range m.Content.Blocks() {
			if textBlock, ok := block.(*codexsdk.TextBlock); ok {
				fmt.Printf("User: %s\n", textBlock.Text)
			}
		}

	case *codexsdk.AssistantMessage:
		for _, block := range m.Content {
			if textBlock, ok := block.(*codexsdk.TextBlock); ok {
				fmt.Printf("Codex: %s\n", textBlock.Text)
			}
		}

	case *codexsdk.SystemMessage:
		// Ignore system messages in display

	case *codexsdk.ResultMessage:
		fmt.Println("Task completed!")

		if m.Usage != nil {
			fmt.Printf("   Tokens: %d in / %d out\n", m.Usage.InputTokens, m.Usage.OutputTokens)
		}
	}
}

// myPermissionCallback controls tool permissions based on tool type and input.
func myPermissionCallback(
	ctx context.Context,
	toolName string,
	inputData map[string]any,
	permCtx *codexsdk.ToolPermissionContext,
) (codexsdk.PermissionResult, error) {
	// Log the tool request
	toolUsageLog = append(toolUsageLog, ToolUsageLog{
		Tool:        toolName,
		Input:       inputData,
		Suggestions: permCtx.Suggestions,
	})

	inputJSON, _ := json.MarshalIndent(inputData, "   ", "  ")

	fmt.Printf("\n🔧 Tool Permission Request: %s\n", toolName)
	fmt.Printf("   Input: %s\n", string(inputJSON))

	// Always allow read operations
	if toolName == "Read" || toolName == "Glob" || toolName == "Grep" {
		fmt.Printf("   ✅ Automatically allowing %s (read-only operation)\n", toolName)

		return &codexsdk.PermissionResultAllow{
			Behavior: "allow",
		}, nil
	}

	// Deny write operations to system directories
	if toolName == "Write" || toolName == "Edit" || toolName == "MultiEdit" {
		filePath, ok := inputData["file_path"].(string)
		if !ok {
			filePath = ""
		}

		if strings.HasPrefix(filePath, "/etc/") || strings.HasPrefix(filePath, "/usr/") {
			fmt.Printf("   ❌ Denying write to system directory: %s\n", filePath)

			return &codexsdk.PermissionResultDeny{
				Behavior: "deny",
				Message:  fmt.Sprintf("Cannot write to system directory: %s", filePath),
			}, nil
		}

		// Redirect writes to a safe directory
		if !strings.HasPrefix(filePath, "/tmp/") && !strings.HasPrefix(filePath, "./") {
			pathParts := strings.Split(filePath, "/")
			fileName := pathParts[len(pathParts)-1]
			safePath := fmt.Sprintf("./safe_output/%s", fileName)

			fmt.Printf("   ⚠️  Redirecting write from %s to %s\n", filePath, safePath)

			modifiedInput := make(map[string]any)
			maps.Copy(modifiedInput, inputData)

			modifiedInput["file_path"] = safePath

			return &codexsdk.PermissionResultAllow{
				Behavior:     "allow",
				UpdatedInput: modifiedInput,
			}, nil
		}
	}

	// Check dangerous bash commands
	if toolName == "Bash" {
		command, ok := inputData["command"].(string)
		if !ok {
			command = ""
		}

		dangerousCommands := []string{"rm -rf", "sudo", "chmod 777", "dd if=", "mkfs"}

		for _, dangerous := range dangerousCommands {
			if strings.Contains(command, dangerous) {
				fmt.Printf("   ❌ Denying dangerous command: %s\n", command)

				return &codexsdk.PermissionResultDeny{
					Behavior: "deny",
					Message:  fmt.Sprintf("Dangerous command pattern detected: %s", dangerous),
				}, nil
			}
		}

		// Allow but log the command
		fmt.Printf("   ✅ Allowing bash command: %s\n", command)

		return &codexsdk.PermissionResultAllow{
			Behavior: "allow",
		}, nil
	}

	// For all other tools, prompt the user (in Golang this would be interactive stdin).
	// Note: Go cannot do synchronous stdin prompting in this callback context,
	// so we deny by default. In an interactive scenario, you would prompt the user
	// via a separate channel or UI mechanism.
	fmt.Printf("   ❓ Unknown tool: %s (would prompt user in interactive mode)\n", toolName)

	return &codexsdk.PermissionResultDeny{
		Behavior: "deny",
		Message:  "Tool requires user approval - not available in non-interactive mode",
	}, nil
}

func main() {
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("Tool Permission Callback Example")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("\nThis example demonstrates how to:")
	fmt.Println("1. Allow/deny tools based on type")
	fmt.Println("2. Modify tool inputs for safety")
	fmt.Println("3. Log tool usage")
	fmt.Println("4. Prompt for unknown tools")
	fmt.Println(strings.Repeat("=", 60))

	// Configure logging
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Create client
	client := codexsdk.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close client: %v\n", err)
		}
	}()

	// Configure options with our callback
	if err := client.Start(ctx,
		codexsdk.WithLogger(logger),
		codexsdk.WithCanUseTool(myPermissionCallback),
		// Use default permission mode to ensure callbacks are invoked
		codexsdk.WithPermissionMode("default"),
		codexsdk.WithCwd("."),
	); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)

		return
	}

	fmt.Println("\n📝 Sending query to Codex...")

	queryText := `Please do the following:
1. List the files in the current directory
2. Create a simple golang hello world script at /tmp/hello.go
3. Run the script to test it`

	if err := client.Query(ctx, queryText); err != nil {
		fmt.Printf("Failed to send query: %v\n", err)

		return
	}

	fmt.Println("\n📨 Receiving response...")

	messageCount := 0

	for msg, err := range client.ReceiveMessages(ctx) {
		if err != nil {
			break
		}

		messageCount++

		displayMessage(msg)

		if _, ok := msg.(*codexsdk.ResultMessage); ok {
			fmt.Printf("   Messages processed: %d\n", messageCount)

			break
		}
	}
	// Print tool usage summary
	fmt.Println("\n" + strings.Repeat("=", 60))

	fmt.Println("Tool Usage Summary")
	fmt.Println(strings.Repeat("=", 60))

	for i, usage := range toolUsageLog {
		fmt.Printf("\n%d. Tool: %s\n", i+1, usage.Tool)

		inputJSON, _ := json.MarshalIndent(usage.Input, "   ", "  ")
		fmt.Printf("   Input: %s\n", string(inputJSON))

		if len(usage.Suggestions) > 0 {
			fmt.Printf("   Suggestions: %d permission updates suggested\n", len(usage.Suggestions))
		}
	}
}
