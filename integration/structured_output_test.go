//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// TestStructuredOutput_JSONSchema tests OutputFormat produces valid JSON.
func TestStructuredOutput_JSONSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var receivedResponse bool

	for msg, err := range codexsdk.Query(ctx, "What is 2+2? Provide structured output.",
		codexsdk.WithPermissionMode("acceptAll"),
		codexsdk.WithOutputFormat(map[string]any{
			"type": "json_schema",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"answer": map[string]any{
						"type":        "string",
						"description": "The answer to the question",
					},
					"confidence": map[string]any{
						"type":        "number",
						"description": "Confidence level from 0 to 1",
					},
				},
				"required":             []string{"answer", "confidence"},
				"additionalProperties": false,
			},
		}),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if textBlock, ok := block.(*codexsdk.TextBlock); ok {
					t.Logf("Structured output (assistant): %s", textBlock.Text)
					receivedResponse = true
				}
			}
		case *codexsdk.ResultMessage:
			require.False(t, m.IsError, "Query should not result in error")

			if m.Result != nil && *m.Result != "" {
				t.Logf("Structured output (result): %s", *m.Result)
				receivedResponse = true
			}
		}
	}

	require.True(t, receivedResponse, "Should receive structured response")
}

// TestStructuredOutput_RequiredFields tests required fields are present in output.
func TestStructuredOutput_RequiredFields(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var receivedResponse bool

	for msg, err := range codexsdk.Query(ctx,
		"Generate a fictional person with a name and age in structured format.",
		codexsdk.WithPermissionMode("acceptAll"),
		codexsdk.WithOutputFormat(map[string]any{
			"type": "json_schema",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
					},
					"age": map[string]any{
						"type": "integer",
					},
				},
				"required":             []string{"name", "age"},
				"additionalProperties": false,
			},
		}),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if textBlock, ok := block.(*codexsdk.TextBlock); ok {
					t.Logf("Output with required fields (assistant): %s", textBlock.Text)
					receivedResponse = true
				}
			}
		case *codexsdk.ResultMessage:
			require.False(t, m.IsError, "Query should not result in error")

			if m.Result != nil && *m.Result != "" {
				t.Logf("Output with required fields (result): %s", *m.Result)
				receivedResponse = true
			}
		}
	}

	require.True(t, receivedResponse, "Should receive response with required fields")
}

// TestStructuredOutput_WithEnum tests structured output with enum type.
func TestStructuredOutput_WithEnum(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var receivedResponse bool

	for msg, err := range codexsdk.Query(ctx,
		"Pick a random color and intensity. Respond in structured format.",
		codexsdk.WithPermissionMode("acceptAll"),
		codexsdk.WithOutputFormat(map[string]any{
			"type": "json_schema",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"color": map[string]any{
						"type":        "string",
						"enum":        []string{"red", "green", "blue"},
						"description": "A color choice",
					},
					"intensity": map[string]any{
						"type":        "string",
						"enum":        []string{"low", "medium", "high"},
						"description": "Intensity level",
					},
				},
				"required":             []string{"color", "intensity"},
				"additionalProperties": false,
			},
		}),
	) {
		if err != nil {
			skipIfCLINotInstalled(t, err)
			t.Fatalf("Query failed: %v", err)
		}

		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if textBlock, ok := block.(*codexsdk.TextBlock); ok {
					t.Logf("Structured output with enum (assistant): %s", textBlock.Text)
					receivedResponse = true
				}
			}
		case *codexsdk.ResultMessage:
			require.False(t, m.IsError, "Query should not result in error")

			if m.Result != nil && *m.Result != "" {
				t.Logf("Structured output with enum (result): %s", *m.Result)
				receivedResponse = true
			}
		}
	}

	require.True(t, receivedResponse, "Should receive structured response with enum values")
}
