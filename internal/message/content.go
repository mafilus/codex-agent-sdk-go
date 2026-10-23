// Package message provides message and content block types for Codex conversations.
package message

import "encoding/json"

// Block type constants.
const (
	BlockTypeText       = "text"
	BlockTypeThinking   = "thinking"
	BlockTypeToolUse    = "tool_use"
	BlockTypeToolResult = "tool_result"
)

// ContentBlock represents a block of content within a message.
type ContentBlock interface {
	BlockType() string
}

// Compile-time verification that all content block types implement ContentBlock.
var (
	_ ContentBlock = (*TextBlock)(nil)
	_ ContentBlock = (*ThinkingBlock)(nil)
	_ ContentBlock = (*ToolUseBlock)(nil)
	_ ContentBlock = (*ToolResultBlock)(nil)
)

// TextBlock contains plain text content.
type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// BlockType implements the ContentBlock interface.
func (b *TextBlock) BlockType() string { return BlockTypeText }

// ThinkingBlock contains the agent's thinking process.
type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
}

// BlockType implements the ContentBlock interface.
func (b *ThinkingBlock) BlockType() string { return BlockTypeThinking }

// ToolUseBlock represents the agent using a tool.
type ToolUseBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// BlockType implements the ContentBlock interface.
func (b *ToolUseBlock) BlockType() string { return BlockTypeToolUse }

// ToolResultBlock contains the result of a tool execution.
//
//nolint:tagliatelle // CLI uses snake_case for JSON fields
type ToolResultBlock struct {
	Type      string         `json:"type"`
	ToolUseID string         `json:"tool_use_id"`
	Content   []ContentBlock `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

// BlockType implements the ContentBlock interface.
func (b *ToolResultBlock) BlockType() string { return BlockTypeToolResult }

// UnmarshalJSON implements json.Unmarshaler for ToolResultBlock.
func (b *ToolResultBlock) UnmarshalJSON(data []byte) error {
	type Alias ToolResultBlock

	aux := &struct {
		Content json.RawMessage `json:"content,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(b),
	}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Content) == 0 || string(aux.Content) == "null" {
		return nil
	}

	// Try string first
	var text string
	if err := json.Unmarshal(aux.Content, &text); err == nil {
		b.Content = []ContentBlock{&TextBlock{Type: BlockTypeText, Text: text}}

		return nil
	}

	// Try array of blocks
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(aux.Content, &rawBlocks); err != nil {
		return err
	}

	b.Content = make([]ContentBlock, 0, len(rawBlocks))

	for _, raw := range rawBlocks {
		block, err := UnmarshalContentBlock(raw)
		if err != nil {
			return err
		}

		b.Content = append(b.Content, block)
	}

	return nil
}

// UnmarshalContentBlock unmarshals a single content block from JSON.
func UnmarshalContentBlock(data []byte) (ContentBlock, error) {
	var typeHolder struct {
		Type string `json:"type"`
	}

	if err := json.Unmarshal(data, &typeHolder); err != nil {
		return nil, err
	}

	switch typeHolder.Type {
	case BlockTypeText:
		var block TextBlock
		if err := json.Unmarshal(data, &block); err != nil {
			return nil, err
		}

		return &block, nil
	case BlockTypeThinking:
		var block ThinkingBlock
		if err := json.Unmarshal(data, &block); err != nil {
			return nil, err
		}

		return &block, nil
	case BlockTypeToolUse:
		var block ToolUseBlock
		if err := json.Unmarshal(data, &block); err != nil {
			return nil, err
		}

		return &block, nil
	case BlockTypeToolResult:
		var block ToolResultBlock
		if err := json.Unmarshal(data, &block); err != nil {
			return nil, err
		}

		return &block, nil
	default:
		var block TextBlock
		if err := json.Unmarshal(data, &block); err != nil {
			return nil, err
		}

		return &block, nil
	}
}
