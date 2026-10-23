package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

// Person represents a simple structured output schema.
type Person struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Hobbies []string `json:"hobbies"`
}

// BookReview represents a more complex nested structured output.
type BookReview struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	Rating int    `json:"rating"`
	Review struct {
		Summary  string   `json:"summary"`
		Pros     []string `json:"pros"`
		Cons     []string `json:"cons"`
		Audience string   `json:"audience"`
	} `json:"review"`
}

func createSchemaFile(schema map[string]any) (string, error) {
	f, err := os.CreateTemp("", "codex-structured-output-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")

	if err := enc.Encode(schema); err != nil {
		return "", err
	}

	return f.Name(), nil
}

// getStructuredOutput runs a query and returns structured JSON.
func getStructuredOutput(ctx context.Context, prompt string, schemaFile string, systemPrompt string) (json.RawMessage, error) {
	var lastAssistantText string

	for msg, err := range codexsdk.Query(ctx, prompt,
		codexsdk.WithOutputSchema(schemaFile),
		codexsdk.WithSystemPrompt(systemPrompt),
	) {
		if err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}

		switch m := msg.(type) {
		case *codexsdk.AssistantMessage:
			for _, block := range m.Content {
				if tb, ok := block.(*codexsdk.TextBlock); ok {
					lastAssistantText += tb.Text
				}
			}

		case *codexsdk.ResultMessage:
			if m.Usage != nil {
				fmt.Printf("Tokens: %d in / %d out\n", m.Usage.InputTokens, m.Usage.OutputTokens)
			}

			if m.StructuredOutput != nil {
				data, marshalErr := json.Marshal(m.StructuredOutput)
				if marshalErr == nil {
					return data, nil
				}
			}

			if m.Result != nil && json.Valid([]byte(*m.Result)) {
				return json.RawMessage(*m.Result), nil
			}
		}
	}

	if json.Valid([]byte(lastAssistantText)) {
		return json.RawMessage(lastAssistantText), nil
	}

	return nil, fmt.Errorf("no structured output received")
}

func simpleStructuredOutput() {
	fmt.Println("=== Simple Structured Output ===")
	fmt.Println("Using WithOutputSchema() to get a JSON Person object.")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name":    map[string]any{"type": "string"},
			"age":     map[string]any{"type": "integer"},
			"hobbies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"name", "age", "hobbies"},
	}

	schemaFile, err := createSchemaFile(schema)
	if err != nil {
		fmt.Printf("Error creating schema file: %v\n", err)

		return
	}
	defer os.Remove(schemaFile)

	output, err := getStructuredOutput(
		ctx,
		"Invent a fictional person with a name, age, and exactly 3 hobbies.",
		schemaFile,
		"You are a creative writer. Respond only with valid JSON matching the schema.",
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	var person Person
	if err := json.Unmarshal(output, &person); err != nil {
		fmt.Printf("Failed to parse JSON: %v\n", err)
		fmt.Printf("Raw output: %s\n", string(output))

		return
	}

	fmt.Printf("Name:    %s\n", person.Name)
	fmt.Printf("Age:     %d\n", person.Age)
	fmt.Printf("Hobbies: %v\n", person.Hobbies)
	fmt.Println()
}

func nestedStructuredOutput() {
	fmt.Println("=== Nested Structured Output ===")
	fmt.Println("Using WithOutputSchema() with a complex nested schema.")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"title":  map[string]any{"type": "string"},
			"author": map[string]any{"type": "string"},
			"rating": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
			"review": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"summary":  map[string]any{"type": "string"},
					"pros":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"cons":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"audience": map[string]any{"type": "string"},
				},
				"required": []string{"summary", "pros", "cons", "audience"},
			},
		},
		"required": []string{"title", "author", "rating", "review"},
	}

	schemaFile, err := createSchemaFile(schema)
	if err != nil {
		fmt.Printf("Error creating schema file: %v\n", err)

		return
	}
	defer os.Remove(schemaFile)

	output, err := getStructuredOutput(
		ctx,
		"Write a brief review of '1984' by George Orwell. Include title, author, a rating from 1-5, and a review with a short summary, 2 pros, 2 cons, and target audience.",
		schemaFile,
		"You are a book critic. Respond only with valid JSON matching the schema.",
	)
	if err != nil {
		fmt.Printf("Error: %v\n", err)

		return
	}

	var review BookReview
	if err := json.Unmarshal(output, &review); err != nil {
		fmt.Printf("Failed to parse JSON: %v\n", err)
		fmt.Printf("Raw output: %s\n", string(output))

		return
	}

	fmt.Printf("Title:    %s\n", review.Title)
	fmt.Printf("Author:   %s\n", review.Author)
	fmt.Printf("Rating:   %d/5\n", review.Rating)
	fmt.Printf("Summary:  %s\n", review.Review.Summary)
	fmt.Printf("Pros:     %v\n", review.Review.Pros)
	fmt.Printf("Cons:     %v\n", review.Review.Cons)
	fmt.Printf("Audience: %s\n", review.Review.Audience)
	fmt.Println()
}

func main() {
	fmt.Println("Structured Output Examples")
	fmt.Println()
	fmt.Println("Demonstrates structured JSON responses using output schema constraints.")
	fmt.Println()

	simpleStructuredOutput()
	nestedStructuredOutput()
}
