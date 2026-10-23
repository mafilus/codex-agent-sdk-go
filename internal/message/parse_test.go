package message

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAssistantMessage(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name           string
		data           map[string]any
		wantError      bool
		wantParseErr   bool
		wantErrorValue AssistantMessageError
		wantModel      string
		wantContentLen int
		wantToolUseID  *string
	}{
		{
			name: "no error field",
			data: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "hello"},
					},
					"model": "claude-sonnet-4-5-20250514",
				},
			},
			wantError:      false,
			wantModel:      "claude-sonnet-4-5-20250514",
			wantContentLen: 1,
		},
		{
			name: "authentication_failed error",
			data: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{},
					"model":   "claude-sonnet-4-5-20250514",
				},
				"error": "authentication_failed",
			},
			wantError:      true,
			wantErrorValue: AssistantMessageErrorAuthFailed,
			wantModel:      "claude-sonnet-4-5-20250514",
			wantContentLen: 0,
		},
		{
			name: "rate_limit error",
			data: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{},
					"model":   "claude-sonnet-4-5-20250514",
				},
				"error": "rate_limit",
			},
			wantError:      true,
			wantErrorValue: AssistantMessageErrorRateLimit,
			wantModel:      "claude-sonnet-4-5-20250514",
			wantContentLen: 0,
		},
		{
			name: "unknown error",
			data: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{},
					"model":   "claude-sonnet-4-5-20250514",
				},
				"error": "unknown",
			},
			wantError:      true,
			wantErrorValue: AssistantMessageErrorUnknown,
			wantModel:      "claude-sonnet-4-5-20250514",
			wantContentLen: 0,
		},
		{
			name: "error at top level not in nested message",
			data: map[string]any{
				"type": "assistant",
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "text", "text": "partial response"},
					},
					"model": "claude-sonnet-4-5-20250514",
					"error": "should_be_ignored",
				},
				"error":              "billing_error",
				"parent_tool_use_id": "tool-123",
			},
			wantError:      true,
			wantErrorValue: AssistantMessageErrorBilling,
			wantModel:      "claude-sonnet-4-5-20250514",
			wantContentLen: 1,
			wantToolUseID:  new("tool-123"),
		},
		{
			name: "missing message field returns parse error",
			data: map[string]any{
				"type": "assistant",
			},
			wantParseErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := Parse(logger, tt.data)

			if tt.wantParseErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)

			assistant, ok := msg.(*AssistantMessage)
			require.True(t, ok, "expected *AssistantMessage")
			require.Equal(t, "assistant", assistant.Type)
			require.Equal(t, tt.wantModel, assistant.Model)
			require.Len(t, assistant.Content, tt.wantContentLen)

			if tt.wantError {
				require.NotNil(t, assistant.Error)
				require.Equal(t, tt.wantErrorValue, *assistant.Error)
			} else {
				require.Nil(t, assistant.Error)
			}

			if tt.wantToolUseID != nil {
				require.NotNil(t, assistant.ParentToolUseID)
				require.Equal(t, *tt.wantToolUseID, *assistant.ParentToolUseID)
			}
		})
	}
}

func TestParseCodexAgentMessageDeltaSuppression(t *testing.T) {
	logger := slog.Default()

	tests := []struct {
		name       string
		data       map[string]any
		wantType   string
		wantSystem bool
	}{
		{
			name: "item.updated agent_message suppressed to SystemMessage",
			data: map[string]any{
				"type": "item.updated",
				"item": map[string]any{
					"type": "agent_message",
					"text": "partial delta",
				},
			},
			wantType:   "system",
			wantSystem: true,
		},
		{
			name: "item.started agent_message suppressed to SystemMessage",
			data: map[string]any{
				"type": "item.started",
				"item": map[string]any{
					"type": "agent_message",
					"text": "",
				},
			},
			wantType:   "system",
			wantSystem: true,
		},
		{
			name: "item.completed agent_message emits AssistantMessage",
			data: map[string]any{
				"type": "item.completed",
				"item": map[string]any{
					"type": "agent_message",
					"text": "complete text",
				},
			},
			wantType:   "assistant",
			wantSystem: false,
		},
		{
			name: "item.updated command_execution emits AssistantMessage",
			data: map[string]any{
				"type": "item.updated",
				"item": map[string]any{
					"type":    "command_execution",
					"id":      "cmd_1",
					"command": "ls",
				},
			},
			wantType:   "assistant",
			wantSystem: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := Parse(logger, tt.data)
			require.NoError(t, err)
			require.Equal(t, tt.wantType, msg.MessageType())

			if tt.wantSystem {
				sys, ok := msg.(*SystemMessage)
				require.True(t, ok, "expected *SystemMessage")
				require.Contains(t, sys.Subtype, "agent_message_delta")
			}
		})
	}
}

func TestParseCodexFileChangeKindObject(t *testing.T) {
	logger := slog.Default()

	data := map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"id":   "item-1",
			"type": "file_change",
			"changes": []any{
				map[string]any{
					"path": "hello.txt",
					"kind": map[string]any{
						"type": "create",
					},
				},
			},
		},
	}

	msg, err := Parse(logger, data)
	require.NoError(t, err)

	assistant, ok := msg.(*AssistantMessage)
	require.True(t, ok, "expected *AssistantMessage")
	require.Len(t, assistant.Content, 1)

	toolUse, ok := assistant.Content[0].(*ToolUseBlock)
	require.True(t, ok, "expected first content block to be ToolUseBlock")
	require.Equal(t, "Write", toolUse.Name)
	require.Equal(t, "hello.txt", toolUse.Input["file_path"])
}
