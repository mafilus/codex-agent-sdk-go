package message

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/mafilus/codex-agent-sdk-go/internal/errors"
)

// Parse converts a raw JSON map into a typed Message.
//
// This function handles both Claude-style messages (with "type": "user"|"assistant"|etc.)
// and Codex-style events (with "type": "thread.started"|"item.completed"|etc.).
func Parse(log *slog.Logger, data map[string]any) (Message, error) {
	log = log.With("component", "message_parser")

	msgType, ok := data["type"].(string)
	if !ok {
		return nil, &errors.MessageParseError{
			Message: "missing or invalid 'type' field",
			Err:     fmt.Errorf("missing or invalid 'type' field"),
			Data:    data,
		}
	}

	log.Debug("parsing message", slog.String("message_type", msgType))

	// Try Claude-style message types first
	switch msgType {
	case "user":
		return parseUserMessage(data)
	case "assistant":
		return parseAssistantMessage(data)
	case "system":
		return parseSystemMessage(data)
	case "result":
		return parseResultMessage(data)
	case "stream_event":
		return parseStreamEvent(data)
	}

	// Try Codex event types
	return parseCodexEvent(log, data, EventType(msgType))
}

// parseCodexEvent converts a Codex event into a claude-sdk-compatible Message.
func parseCodexEvent(
	log *slog.Logger,
	data map[string]any,
	eventType EventType,
) (Message, error) {
	switch eventType {
	case EventItemCompleted, EventItemStarted, EventItemUpdated:
		return parseCodexItemEvent(log, data)

	case EventTurnCompleted:
		return parseCodexTurnCompleted(data)

	case EventTurnFailed:
		return parseCodexTurnFailed(data)

	case EventThreadStarted, EventTurnStarted:
		// System-level events → SystemMessage
		return &SystemMessage{
			Type:    "system",
			Subtype: string(eventType),
			Data:    data,
		}, nil

	case EventError:
		msg, _ := data["message"].(string)
		errType := AssistantMessageErrorUnknown

		return &AssistantMessage{
			Type: "assistant",
			Content: []ContentBlock{
				&TextBlock{Type: BlockTypeText, Text: "Error: " + msg},
			},
			Error: &errType,
		}, nil

	default:
		return nil, errors.ErrUnknownMessageType
	}
}

// parseCodexItemEvent converts a Codex item event into an AssistantMessage.
func parseCodexItemEvent(log *slog.Logger, data map[string]any) (Message, error) {
	event, err := ParseCodexEvent(data)
	if err != nil {
		return nil, &errors.MessageParseError{
			Message: "failed to parse codex event",
			Err:     err,
			Data:    data,
		}
	}

	if event.Item == nil {
		log.Debug("codex event has no item", slog.String("event_type", string(event.Type)))

		return &SystemMessage{
			Type:    "system",
			Subtype: string(event.Type),
			Data:    data,
		}, nil
	}

	// Defense-in-depth: suppress non-completed agent_message events that
	// reach the parser (e.g. item.updated deltas from custom transports or
	// exec backend). Only item.completed agent_message events produce an
	// AssistantMessage; others are emitted as system lifecycle messages.
	if event.Item.Type == ItemTypeAgentMessage && event.Type != EventItemCompleted {
		return &SystemMessage{
			Type:    "system",
			Subtype: string(event.Type) + ".agent_message_delta",
			Data:    data,
		}, nil
	}

	return convertCodexItem(event.Item, event.Type), nil
}

// convertCodexItem converts a single Codex item to an AssistantMessage.
func convertCodexItem(item *CodexItem, eventType EventType) *AssistantMessage {
	msg := &AssistantMessage{
		Type: "assistant",
	}

	switch item.Type {
	case ItemTypeAgentMessage:
		msg.Content = []ContentBlock{
			&TextBlock{Type: BlockTypeText, Text: item.Text},
		}

	case ItemTypeReasoning:
		msg.Content = []ContentBlock{
			&ThinkingBlock{Type: BlockTypeThinking, Thinking: item.Text},
		}

	case ItemTypeCommandExec:
		toolUse := &ToolUseBlock{
			Type: BlockTypeToolUse,
			ID:   item.ID,
			Name: "Bash",
			Input: map[string]any{
				"command": item.Command,
			},
		}
		msg.Content = []ContentBlock{toolUse}

		// For completed events, add result
		if eventType == EventItemCompleted {
			result := &ToolResultBlock{
				Type:      BlockTypeToolResult,
				ToolUseID: item.ID,
				Content: []ContentBlock{
					&TextBlock{Type: BlockTypeText, Text: item.AggregatedOutput},
				},
			}

			if item.ExitCode != nil && *item.ExitCode != 0 {
				result.IsError = true
			}

			msg.Content = append(msg.Content, result)
		}

	case ItemTypeFileChange:
		toolName := "Edit"

		for _, change := range item.Changes {
			if change.Kind == "create" {
				toolName = "Write"

				break
			}
		}

		input := map[string]any{}
		if len(item.Changes) > 0 {
			input["file_path"] = item.Changes[0].Path
		}

		msg.Content = []ContentBlock{
			&ToolUseBlock{
				Type:  BlockTypeToolUse,
				ID:    item.ID,
				Name:  toolName,
				Input: input,
			},
		}

		if eventType == EventItemCompleted {
			msg.Content = append(msg.Content, &ToolResultBlock{
				Type:      BlockTypeToolResult,
				ToolUseID: item.ID,
				IsError:   item.Status == "failed" || item.Status == "declined",
			})
		}

	case ItemTypeMCPToolCall:
		toolName := item.Tool
		if item.Server != "" {
			toolName = item.Server + ":" + item.Tool
		}

		msg.Content = []ContentBlock{
			&ToolUseBlock{
				Type:  BlockTypeToolUse,
				ID:    item.ID,
				Name:  toolName,
				Input: map[string]any{},
			},
		}

	case ItemTypeWebSearch:
		msg.Content = []ContentBlock{
			&ToolUseBlock{
				Type: BlockTypeToolUse,
				ID:   item.ID,
				Name: "WebSearch",
				Input: map[string]any{
					"query": item.Query,
				},
			},
		}

	case ItemTypeError:
		msg.Content = []ContentBlock{
			&TextBlock{Type: BlockTypeText, Text: "Error: " + item.Message},
		}
		errType := AssistantMessageErrorUnknown
		msg.Error = &errType

	default:
		// Unknown item type — pass through as text
		msg.Content = []ContentBlock{
			&TextBlock{Type: BlockTypeText, Text: item.Text},
		}
	}

	return msg
}

// parseCodexTurnCompleted converts a turn.completed event to a ResultMessage.
func parseCodexTurnCompleted(data map[string]any) (*ResultMessage, error) {
	result := &ResultMessage{
		Type:    "result",
		Subtype: "success",
	}

	if v, ok := data["is_error"].(bool); ok {
		result.IsError = v
	} else if v, ok := data["isError"].(bool); ok {
		result.IsError = v
	}

	if sid, ok := data["session_id"].(string); ok {
		result.SessionID = sid
	} else if sid, ok := data["thread_id"].(string); ok {
		result.SessionID = sid
	} else if sid, ok := data["threadId"].(string); ok {
		result.SessionID = sid
	}

	if txt, ok := data["result"].(string); ok {
		result.Result = &txt
	}

	// Parse usage if present
	if usageData, ok := data["usage"].(map[string]any); ok {
		usage := &Usage{}

		if v, ok := usageData["input_tokens"].(float64); ok {
			usage.InputTokens = int(v)
		} else if v, ok := usageData["inputTokens"].(float64); ok {
			usage.InputTokens = int(v)
		}

		if v, ok := usageData["output_tokens"].(float64); ok {
			usage.OutputTokens = int(v)
		} else if v, ok := usageData["outputTokens"].(float64); ok {
			usage.OutputTokens = int(v)
		}

		if v, ok := usageData["cached_input_tokens"].(float64); ok {
			usage.CachedInputTokens = int(v)
		} else if v, ok := usageData["cachedInputTokens"].(float64); ok {
			usage.CachedInputTokens = int(v)
		}

		if v, ok := usageData["reasoning_output_tokens"].(float64); ok {
			usage.ReasoningOutputTokens = int(v)
		} else if v, ok := usageData["reasoningOutputTokens"].(float64); ok {
			usage.ReasoningOutputTokens = int(v)
		}

		result.Usage = usage
	}

	return result, nil
}

// parseCodexTurnFailed converts a turn.failed event to a ResultMessage.
func parseCodexTurnFailed(data map[string]any) (*ResultMessage, error) {
	result := &ResultMessage{
		Type:    "result",
		Subtype: "error",
		IsError: true,
	}

	if errorData, ok := data["error"].(map[string]any); ok {
		if msg, ok := errorData["message"].(string); ok {
			result.Result = &msg
		}
	}

	return result, nil
}

// parseUserMessage parses a UserMessage from raw JSON.
func parseUserMessage(data map[string]any) (*UserMessage, error) {
	msg := &UserMessage{Type: "user"}

	messageData, ok := data["message"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("user message: missing or invalid 'message' field")
	}

	contentData, ok := messageData["content"]
	if !ok {
		return nil, fmt.Errorf("user message: missing content field")
	}

	contentJSON, err := json.Marshal(contentData)
	if err != nil {
		return nil, fmt.Errorf("user message: marshal content: %w", err)
	}

	var content UserMessageContent
	if err := json.Unmarshal(contentJSON, &content); err != nil {
		return nil, fmt.Errorf("user message: %w", err)
	}

	msg.Content = content

	if uuid, ok := data["uuid"].(string); ok {
		msg.UUID = &uuid
	}

	if parentToolUseID, ok := data["parent_tool_use_id"].(string); ok {
		msg.ParentToolUseID = &parentToolUseID
	}

	return msg, nil
}

// parseAssistantMessage parses an AssistantMessage from raw JSON.
func parseAssistantMessage(data map[string]any) (*AssistantMessage, error) {
	msg := &AssistantMessage{Type: "assistant"}

	messageData, ok := data["message"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'message' field")
	}

	if contentData, ok := messageData["content"].([]any); ok {
		content, err := parseContentBlocks(contentData)
		if err != nil {
			return nil, fmt.Errorf("parse assistant content: %w", err)
		}

		msg.Content = content
	}

	if model, ok := messageData["model"].(string); ok {
		msg.Model = model
	}

	if parentToolUseID, ok := data["parent_tool_use_id"].(string); ok {
		msg.ParentToolUseID = &parentToolUseID
	}

	if errorVal, ok := data["error"].(string); ok {
		errType := AssistantMessageError(errorVal)
		msg.Error = &errType
	}

	return msg, nil
}

// parseSystemMessage parses a SystemMessage from raw JSON.
func parseSystemMessage(data map[string]any) (*SystemMessage, error) {
	msg := &SystemMessage{Type: "system"}

	subtype, ok := data["subtype"].(string)
	if !ok {
		return nil, fmt.Errorf("system message: missing or invalid 'subtype' field")
	}

	msg.Subtype = subtype

	if msgData, ok := data["data"].(map[string]any); ok {
		msg.Data = msgData
	} else {
		msg.Data = make(map[string]any, len(data))

		for k, v := range data {
			if k != "type" && k != "subtype" {
				msg.Data[k] = v
			}
		}
	}

	return msg, nil
}

// parseStreamEvent parses a StreamEvent from raw JSON.
func parseStreamEvent(data map[string]any) (*StreamEvent, error) {
	event := &StreamEvent{}

	uuid, ok := data["uuid"].(string)
	if !ok {
		return nil, fmt.Errorf("stream_event: missing or invalid 'uuid' field")
	}

	event.UUID = uuid

	sessionID, ok := data["session_id"].(string)
	if !ok {
		return nil, fmt.Errorf("stream_event: missing or invalid 'session_id' field")
	}

	event.SessionID = sessionID

	eventData, ok := data["event"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("stream_event: missing or invalid 'event' field")
	}

	event.Event = eventData

	if parentToolUseID, ok := data["parent_tool_use_id"].(string); ok {
		event.ParentToolUseID = &parentToolUseID
	}

	return event, nil
}

// parseResultMessage parses a ResultMessage from raw JSON.
func parseResultMessage(data map[string]any) (*ResultMessage, error) {
	if _, ok := data["subtype"].(string); !ok {
		return nil, fmt.Errorf("result message: missing or invalid 'subtype' field")
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal result: %w", err)
	}

	var msg ResultMessage
	if err := json.Unmarshal(jsonBytes, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &msg, nil
}

// parseContentBlocks parses an array of content blocks.
func parseContentBlocks(data []any) ([]ContentBlock, error) {
	blocks := make([]ContentBlock, 0, len(data))

	for i, item := range data {
		blockData, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("content block %d: not an object", i)
		}

		block, err := parseContentBlock(blockData)
		if err != nil {
			return nil, fmt.Errorf("content block %d: %w", i, err)
		}

		blocks = append(blocks, block)
	}

	return blocks, nil
}

// parseContentBlock parses a single content block from a map.
func parseContentBlock(data map[string]any) (ContentBlock, error) {
	blockType, ok := data["type"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid 'type' field")
	}

	switch blockType {
	case BlockTypeText:
		text, _ := data["text"].(string)

		return &TextBlock{Type: BlockTypeText, Text: text}, nil
	case BlockTypeThinking:
		thinking, _ := data["thinking"].(string)
		signature, _ := data["signature"].(string)

		return &ThinkingBlock{Type: BlockTypeThinking, Thinking: thinking, Signature: signature}, nil
	case BlockTypeToolUse:
		id, _ := data["id"].(string)
		name, _ := data["name"].(string)
		input, _ := data["input"].(map[string]any)

		return &ToolUseBlock{Type: BlockTypeToolUse, ID: id, Name: name, Input: input}, nil
	case BlockTypeToolResult:
		block := &ToolResultBlock{Type: BlockTypeToolResult}

		if toolUseID, ok := data["tool_use_id"].(string); ok {
			block.ToolUseID = toolUseID
		}

		if isError, ok := data["is_error"].(bool); ok {
			block.IsError = isError
		}

		if contentData, ok := data["content"].([]any); ok {
			content, err := parseContentBlocks(contentData)
			if err != nil {
				return nil, fmt.Errorf("parse tool result content: %w", err)
			}

			block.Content = content
		}

		return block, nil
	default:
		return nil, fmt.Errorf("unknown content block type: %s", blockType)
	}
}
