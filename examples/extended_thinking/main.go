package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

func displayMessage(msg codexsdk.Message) {
	switch m := msg.(type) {
	case *codexsdk.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *codexsdk.ThinkingBlock:
				fmt.Println("[Thinking]")
				fmt.Println(b.Thinking)
				fmt.Println("[End Thinking]")
			case *codexsdk.TextBlock:
				fmt.Printf("Codex: %s\n", b.Text)
			}
		}
	case *codexsdk.SystemMessage:
		if (m.Subtype == "item/reasoning/summaryPartAdded" || m.Subtype == "item/reasoning/summaryTextDelta") && m.Data != nil {
			if text, ok := m.Data["text"].(string); ok && text != "" {
				fmt.Printf("[Reasoning Summary] %s\n", text)
			}
		}
	case *codexsdk.ResultMessage:
		fmt.Println("Result ended")

		if m.Usage != nil {
			fmt.Printf("Tokens: %d in / %d out\n", m.Usage.InputTokens, m.Usage.OutputTokens)
		}
	}
}

func runEffortExample(title string, effort codexsdk.Effort, prompt string) {
	fmt.Printf("=== %s ===\n", title)
	fmt.Println()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := codexsdk.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close client: %v\n", err)
		}
	}()

	if err := client.Start(ctx,
		codexsdk.WithLogger(logger),
		codexsdk.WithEffort(effort),
	); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)

		return
	}

	fmt.Printf("Prompt: %s\n", prompt)
	fmt.Println(strings.Repeat("-", 60))

	if err := client.Query(ctx, prompt); err != nil {
		fmt.Printf("Failed to send query: %v\n", err)

		return
	}

	for msg, err := range client.ReceiveResponse(ctx) {
		if err != nil {
			fmt.Printf("Error receiving response: %v\n", err)

			return
		}

		displayMessage(msg)
	}

	fmt.Println()
}

func runStreamingEffortExample() {
	fmt.Println("=== Streaming Reasoning Example ===")
	fmt.Println("Streams response events while using high reasoning effort.")
	fmt.Println()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := codexsdk.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	defer func() {
		if err := client.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close client: %v\n", err)
		}
	}()

	if err := client.Start(ctx,
		codexsdk.WithLogger(logger),
		codexsdk.WithEffort(codexsdk.EffortHigh),
	); err != nil {
		fmt.Printf("Failed to connect: %v\n", err)

		return
	}

	prompt := "A train leaves Chicago at 9am at 60mph. Another leaves New York at 10am at 80mph toward Chicago. They are 790 miles apart. When do they meet?"
	fmt.Printf("Prompt: %s\n", prompt)
	fmt.Println(strings.Repeat("-", 60))

	if err := client.Query(ctx, prompt); err != nil {
		fmt.Printf("Failed to send query: %v\n", err)

		return
	}

	for msg, err := range client.ReceiveMessages(ctx) {
		if err != nil {
			break
		}

		displayMessage(msg)

		if _, ok := msg.(*codexsdk.ResultMessage); ok {
			break
		}
	}

	fmt.Println()
}

func main() {
	fmt.Println("Reasoning Effort Examples")
	fmt.Println("Demonstrating supported reasoning controls with WithEffort")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()
	fmt.Println("Note: detailed reasoning output is model/runtime dependent.")
	fmt.Println()

	examples := map[string]func(){
		"low": func() {
			runEffortExample(
				"Low Effort Example",
				codexsdk.EffortLow,
				"Explain the relationship between the Fibonacci sequence and the golden ratio in one short paragraph.",
			)
		},
		"high": func() {
			runEffortExample(
				"High Effort Example",
				codexsdk.EffortHigh,
				"What is the sum of the first 20 prime numbers? Show the key steps.",
			)
		},
		"streaming": runStreamingEffortExample,
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <example_name>")
		fmt.Println()
		fmt.Println("Available examples:")
		fmt.Println("  all       - Run all examples")
		fmt.Println("  low       - Low reasoning effort")
		fmt.Println("  high      - High reasoning effort")
		fmt.Println("  streaming - Stream responses with high effort")

		return
	}

	example := os.Args[1]
	if example == "all" {
		examples["low"]()
		fmt.Println(strings.Repeat("-", 60))
		fmt.Println()
		examples["high"]()
		fmt.Println(strings.Repeat("-", 60))
		fmt.Println()
		examples["streaming"]()

		return
	}

	if fn, ok := examples[example]; ok {
		fn()

		return
	}

	fmt.Printf("Error: Unknown example '%s'\n", example)
	fmt.Println("Available examples: all, low, high, streaming")
	os.Exit(1)
}
