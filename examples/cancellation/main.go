package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// displayMessage standardizes message display function.
func displayMessage(msg codexsdk.Message) {
	switch m := msg.(type) {
	case *codexsdk.AssistantMessage:
		for _, block := range m.Content {
			if textBlock, ok := block.(*codexsdk.TextBlock); ok {
				fmt.Printf("Codex: %s\n", textBlock.Text)
			}
		}

	case *codexsdk.ResultMessage:
		fmt.Println("Result ended")
	}
}

// exampleCancellation demonstrates cancelling a long-running callback.
func exampleCancellation() {
	fmt.Println("=== Cancellation Example ===")
	fmt.Println("This example demonstrates cancellation with tool-permission callbacks.")
	fmt.Println("The example triggers cancellation automatically after the callback starts.")
	fmt.Println()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := codexsdk.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close client: %v\n", err)
		}
	}()

	callbackStarted := make(chan struct{})

	var callbackStartedOnce sync.Once

	longRunningCallback := func(
		ctx context.Context,
		toolName string,
		_ map[string]any,
		_ *codexsdk.ToolPermissionContext,
	) (codexsdk.PermissionResult, error) {
		if toolName != "Bash" {
			return &codexsdk.PermissionResultAllow{}, nil
		}

		fmt.Printf("[CALLBACK] Starting long-running check for tool: %s\n", toolName)
		callbackStartedOnce.Do(func() { close(callbackStarted) })
		fmt.Println("[CALLBACK] Simulating work until context cancellation")

		for i := 1; i <= 10; i++ {
			select {
			case <-ctx.Done():
				fmt.Printf("[CALLBACK] Operation cancelled after %d seconds!\n", i-1)
				fmt.Printf("[CALLBACK] Cancellation reason: %v\n", ctx.Err())

				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
				fmt.Printf("[CALLBACK] Working... %d/10 seconds\n", i)
			}
		}

		fmt.Println("[CALLBACK] Operation completed successfully")

		return &codexsdk.PermissionResultAllow{}, nil
	}

	if err := client.Start(ctx,
		codexsdk.WithLogger(logger),
		codexsdk.WithCanUseTool(longRunningCallback),
		codexsdk.WithPermissionMode("default"),
	); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)

		return
	}

	fmt.Println("User: Create a file named cancellation_demo.txt with 'Hello World'")
	fmt.Println()

	queryDone := make(chan error, 1)

	go func() {
		queryDone <- client.Query(ctx, "Create a file named cancellation_demo.txt with 'Hello World'")
	}()

	responseDone := make(chan struct{})

	go func() {
		defer close(responseDone)

		for msg, err := range client.ReceiveResponse(ctx) {
			if err != nil {
				fmt.Printf("[MAIN] ReceiveResponse ended: %v\n", err)

				return
			}

			displayMessage(msg)
		}
	}()

	select {
	case <-callbackStarted:
		fmt.Println("[MAIN] Callback started; cancelling context in 2 seconds...")
		time.Sleep(2 * time.Second)
		cancel()
	case <-time.After(30 * time.Second):
		fmt.Println("[MAIN] Timeout waiting for callback to start")

		return
	}

	select {
	case err := <-queryDone:
		if err != nil {
			fmt.Printf("[MAIN] Query ended with error (expected after cancel): %v\n", err)
		}
	case <-time.After(15 * time.Second):
		fmt.Println("[MAIN] Query did not finish in time after cancellation")
	}

	<-responseDone
	fmt.Println()
}

// exampleGracefulShutdown demonstrates graceful shutdown with in-flight callbacks.
func exampleGracefulShutdown() {
	fmt.Println("=== Graceful Shutdown Example ===")
	fmt.Println("This example demonstrates graceful shutdown of in-flight callbacks.")
	fmt.Println()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := codexsdk.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	callbackStarted := make(chan struct{})
	callbackDone := make(chan struct{})

	var callbackStartedOnce sync.Once

	waitingCallback := func(
		ctx context.Context,
		toolName string,
		_ map[string]any,
		_ *codexsdk.ToolPermissionContext,
	) (codexsdk.PermissionResult, error) {
		if toolName != "Bash" {
			return &codexsdk.PermissionResultAllow{}, nil
		}

		fmt.Printf("[CALLBACK] Started for tool: %s\n", toolName)
		callbackStartedOnce.Do(func() { close(callbackStarted) })

		<-ctx.Done()
		fmt.Println("[CALLBACK] Context cancelled during graceful shutdown")
		close(callbackDone)

		return nil, ctx.Err()
	}

	if err := client.Start(ctx,
		codexsdk.WithLogger(logger),
		codexsdk.WithCanUseTool(waitingCallback),
		codexsdk.WithPermissionMode("default"),
	); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)

		return
	}

	receiveCtx, stopReceive := context.WithCancel(context.Background())
	defer stopReceive()

	go func() {
		for range client.ReceiveMessages(receiveCtx) {
		}
	}()

	go func() {
		prompt := "Use Bash to run exactly this command and create the file: printf 'test' > graceful_shutdown_demo.txt"
		if err := client.Query(ctx, prompt); err != nil {
			fmt.Printf("Query error (expected during shutdown): %v\n", err)
		}
	}()

	select {
	case <-callbackStarted:
		fmt.Println("[MAIN] Callback is running, initiating graceful shutdown...")
	case <-time.After(30 * time.Second):
		fmt.Println("[MAIN] Timeout waiting for callback to start")

		return
	}

	time.Sleep(500 * time.Millisecond)

	fmt.Println("[MAIN] Calling client.Close() - this will cancel in-flight operations")

	if err := client.Close(); err != nil {
		fmt.Printf("[MAIN] Close completed with: %v\n", err)
	} else {
		fmt.Println("[MAIN] Close completed successfully")
	}

	select {
	case <-callbackDone:
		fmt.Println("[MAIN] In-flight callback exited after shutdown")
	case <-time.After(10 * time.Second):
		fmt.Println("[MAIN] Timeout waiting for callback to exit")
	}

	fmt.Println()
}

func main() {
	fmt.Println("Starting Codex SDK Cancellation Examples...")
	fmt.Println("============================================")
	fmt.Println()

	examples := map[string]func(){
		"cancellation":      exampleCancellation,
		"graceful_shutdown": exampleGracefulShutdown,
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <example_name>")
		fmt.Println("\nAvailable examples:")
		fmt.Println("  all               - Run all examples")
		fmt.Println("  cancellation      - Demonstrate cancelling a long-running callback")
		fmt.Println("  graceful_shutdown - Demonstrate graceful shutdown")

		return
	}

	exampleName := os.Args[1]

	if exampleName == "all" {
		exampleOrder := []string{"cancellation", "graceful_shutdown"}
		for _, name := range exampleOrder {
			if fn, ok := examples[name]; ok {
				fn()
				fmt.Println("--------------------------------------------------")
				fmt.Println()
			}
		}
	} else if fn, ok := examples[exampleName]; ok {
		fn()
	} else {
		fmt.Printf("Unknown example: %s\n", exampleName)
		fmt.Println("\nAvailable examples:")
		fmt.Println("  all               - Run all examples")

		for name := range examples {
			fmt.Printf("  %s\n", name)
		}

		os.Exit(1)
	}
}
