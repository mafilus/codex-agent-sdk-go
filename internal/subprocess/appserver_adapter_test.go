package subprocess

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/stretchr/testify/require"
)

// Compile-time check that mockAppServerRPC implements appServerRPC.
var _ appServerRPC = (*mockAppServerRPC)(nil)

// mockAppServerRPC simulates an AppServerTransport for testing the adapter
// without spawning a real codex process.
type mockAppServerRPC struct {
	mu sync.Mutex

	started   bool
	closed    bool
	ready     bool
	notifyCh  chan *RPCNotification
	requestCh chan *RPCIncomingRequest

	sentRequests    []mockRPCCall
	sendRequestFunc func(ctx context.Context, method string, params any) (*RPCResponse, error)
	sentResponses   []mockRPCResponse
}

type mockRPCCall struct {
	Method string
	Params any
}

type mockRPCResponse struct {
	ID     int64
	Result json.RawMessage
	Error  *RPCError
}

func newMockAppServerRPC() *mockAppServerRPC {
	return &mockAppServerRPC{
		ready:     true,
		notifyCh:  make(chan *RPCNotification, 32),
		requestCh: make(chan *RPCIncomingRequest, 32),
	}
}

func (m *mockAppServerRPC) Start(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.started = true

	return nil
}

func (m *mockAppServerRPC) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.closed {
		m.closed = true
		close(m.notifyCh)
		close(m.requestCh)
	}

	return nil
}

func (m *mockAppServerRPC) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.ready
}

func (m *mockAppServerRPC) Notifications() <-chan *RPCNotification {
	return m.notifyCh
}

func (m *mockAppServerRPC) Requests() <-chan *RPCIncomingRequest {
	return m.requestCh
}

func (m *mockAppServerRPC) SendRequest(
	ctx context.Context,
	method string,
	params any,
) (*RPCResponse, error) {
	m.mu.Lock()
	m.sentRequests = append(m.sentRequests, mockRPCCall{Method: method, Params: params})
	fn := m.sendRequestFunc
	m.mu.Unlock()

	if fn != nil {
		return fn(ctx, method, params)
	}

	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      1,
		Result:  json.RawMessage(`{}`),
	}, nil
}

func (m *mockAppServerRPC) SendResponse(
	id int64,
	result json.RawMessage,
	rpcErr *RPCError,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sentResponses = append(m.sentResponses, mockRPCResponse{
		ID:     id,
		Result: result,
		Error:  rpcErr,
	})

	return nil
}

func (m *mockAppServerRPC) getSentRequests() []mockRPCCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]mockRPCCall, len(m.sentRequests))
	copy(result, m.sentRequests)

	return result
}

func (m *mockAppServerRPC) getSentResponses() []mockRPCResponse {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]mockRPCResponse, len(m.sentResponses))
	copy(result, m.sentResponses)

	return result
}

// newTestAdapter creates an AppServerAdapter with a mock inner transport.
func newTestAdapter(mock *mockAppServerRPC) *AppServerAdapter {
	log := slog.Default()

	adapter := &AppServerAdapter{
		log:                log.With(slog.String("component", "appserver_adapter")),
		inner:              mock,
		messages:           make(chan map[string]any, 128),
		errs:               make(chan error, 4),
		done:               make(chan struct{}),
		pendingRPCRequests: make(map[string]int64, 8),
	}

	adapter.wg.Add(1)

	go adapter.readLoop()

	return adapter
}

// sendControlRequest is a helper that builds and sends a control_request
// message via the adapter's SendMessage.
func sendControlRequest(
	t *testing.T,
	adapter *AppServerAdapter,
	mock *mockAppServerRPC,
	subtype string,
	extra map[string]any,
) {
	t.Helper()

	request := map[string]any{"subtype": subtype}
	maps.Copy(request, extra)

	msg := map[string]any{
		"type":       "control_request",
		"request_id": "test_req_1",
		"request":    request,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	if mock.sendRequestFunc == nil {
		mock.sendRequestFunc = func(
			_ context.Context,
			_ string,
			_ any,
		) (*RPCResponse, error) {
			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"threadId":"t1","turnId":"turn1"}`),
			}, nil
		}
	}

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)
}

func receiveAdapterMessage(t *testing.T, adapter *AppServerAdapter) map[string]any {
	t.Helper()

	select {
	case msg := <-adapter.messages:
		return msg
	case <-time.After(time.Second):
		t.Fatal("expected adapter message")

		return nil
	}
}

func TestAppServerAdapter_InitializeHandshake(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	sendControlRequest(t, adapter, mock, "initialize", nil)

	calls := mock.getSentRequests()
	require.Len(t, calls, 1)
	require.Equal(t, "thread/start", calls[0].Method)

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "success", resp["subtype"])
		require.Equal(t, "test_req_1", resp["request_id"])
	case <-time.After(time.Second):
		t.Fatal("expected control_response message")
	}
}

func TestAppServerAdapter_UserMessage(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.sendRequestFunc = func(
		_ context.Context,
		_ string,
		_ any,
	) (*RPCResponse, error) {
		return &RPCResponse{
			JSONRPC: "2.0",
			ID:      2,
			Result:  json.RawMessage(`{"turnId":"turn_abc"}`),
		}, nil
	}

	userMsg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": "Hello, world!",
		},
	}

	data, err := json.Marshal(userMsg)
	require.NoError(t, err)

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)

	calls := mock.getSentRequests()
	require.Len(t, calls, 1)
	require.Equal(t, "turn/start", calls[0].Method)

	params, ok := calls[0].Params.(map[string]any)
	require.True(t, ok)

	// Input is wrapped as content blocks for the app-server.
	inputBlocks, ok := params["input"].([]map[string]any)
	require.True(t, ok, "input should be []map[string]any")
	require.Len(t, inputBlocks, 1)
	require.Equal(t, "text", inputBlocks[0]["type"])
	require.Equal(t, "Hello, world!", inputBlocks[0]["text"])
}

func TestAppServerAdapter_InterruptRequest(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	sendControlRequest(t, adapter, mock, "interrupt", nil)

	calls := mock.getSentRequests()
	require.Len(t, calls, 1)
	require.Equal(t, "turn/interrupt", calls[0].Method)

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "control_response", msg["type"])
	case <-time.After(time.Second):
		t.Fatal("expected control_response message")
	}
}

func TestAppServerAdapter_InitializeResumeAndFork(t *testing.T) {
	t.Run("resume", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			_ string,
			_ any,
		) (*RPCResponse, error) {
			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"thread":{"id":"thr_resume"}}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "initialize", map[string]any{
			"resume": "thr_existing",
		})

		calls := mock.getSentRequests()
		require.Len(t, calls, 1)
		require.Equal(t, "thread/resume", calls[0].Method)

		params, ok := calls[0].Params.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "thr_existing", params["threadId"])

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])
	})

	t.Run("fork", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			_ string,
			_ any,
		) (*RPCResponse, error) {
			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"thread":{"id":"thr_forked"}}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "initialize", map[string]any{
			"resume":      "thr_existing",
			"forkSession": true,
		})

		calls := mock.getSentRequests()
		require.Len(t, calls, 1)
		require.Equal(t, "thread/fork", calls[0].Method)

		params, ok := calls[0].Params.(map[string]any)
		require.True(t, ok)
		require.Equal(t, "thr_existing", params["threadId"])
	})
}

func TestAppServerAdapter_SetModelAndPermissionOverrides(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	sendControlRequest(t, adapter, mock, "set_model", map[string]any{
		"model": "gpt-5",
	})

	msg := receiveAdapterMessage(t, adapter)
	require.Equal(t, "control_response", msg["type"])

	sendControlRequest(t, adapter, mock, "set_permission_mode", map[string]any{
		"mode": "acceptAll",
	})

	msg = receiveAdapterMessage(t, adapter)
	require.Equal(t, "control_response", msg["type"])

	userMsg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": "run tests",
		},
	}

	data, err := json.Marshal(userMsg)
	require.NoError(t, err)

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)

	calls := mock.getSentRequests()
	require.Len(t, calls, 1)
	require.Equal(t, "turn/start", calls[0].Method)

	params, ok := calls[0].Params.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "gpt-5", params["model"])
	require.Equal(t, "never", params["approvalPolicy"])

	sandboxPolicy, ok := params["sandboxPolicy"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "dangerFullAccess", sandboxPolicy["type"])
}

func TestAppServerAdapter_ControlRequests_MCPStatusAndUnsupported(t *testing.T) {
	t.Run("mcp_status", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			method string,
			_ any,
		) (*RPCResponse, error) {
			require.Equal(t, "mcpServerStatus/list", method)

			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: json.RawMessage(`{
					"data":[
						{"name":"calc","authStatus":"oauth"},
						{"name":"search","authStatus":"notLoggedIn"}
					],
					"nextCursor":null
				}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "mcp_status", nil)

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "success", resp["subtype"])

		payload, ok := resp["response"].(map[string]any)
		require.True(t, ok)

		servers, ok := payload["mcpServers"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, servers, 2)
		require.Equal(t, "calc", servers[0]["name"])
		require.Equal(t, "connected", servers[0]["status"])
		require.Equal(t, "not_logged_in", servers[1]["status"])
	})

	t.Run("list_models", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			method string,
			_ any,
		) (*RPCResponse, error) {
			require.Equal(t, "model/list", method)

			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: json.RawMessage(`{
					"data":[
						{
							"id":"o4-mini",
							"model":"o4-mini",
							"displayName":"O4 Mini",
							"description":"Small fast model",
							"isDefault":true,
							"hidden":false,
							"defaultReasoningEffort":"medium",
							"supportedReasoningEfforts":[{"value":"low","label":"Low"}],
							"inputModalities":["text","image"],
							"supportsPersonality":false
						},
						{
							"id":"o3",
							"model":"o3",
							"displayName":"O3",
							"isDefault":false,
							"hidden":true
						}
					]
				}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "list_models", nil)

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "success", resp["subtype"])

		payload, ok := resp["response"].(map[string]any)
		require.True(t, ok)

		models, ok := payload["models"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, models, 2)
		require.Equal(t, "o4-mini", models[0]["id"])
		require.Equal(t, "O4 Mini", models[0]["displayName"])
		require.Equal(t, true, models[0]["isDefault"])
		require.Equal(t, "o3", models[1]["id"])
		require.Equal(t, true, models[1]["hidden"])
	})

	t.Run("list_models with empty result", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			method string,
			_ any,
		) (*RPCResponse, error) {
			require.Equal(t, "model/list", method)

			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"data":[]}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "list_models", nil)

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "success", resp["subtype"])

		payload, ok := resp["response"].(map[string]any)
		require.True(t, ok)

		models, ok := payload["models"].([]map[string]any)
		require.True(t, ok)
		require.Empty(t, models)
	})

	t.Run("list_models skips entries without id", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.sendRequestFunc = func(
			_ context.Context,
			_ string,
			_ any,
		) (*RPCResponse, error) {
			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result: json.RawMessage(`{
					"data":[
						{"displayName":"No ID model"},
						{"id":"valid","displayName":"Valid"}
					]
				}`),
			}, nil
		}

		sendControlRequest(t, adapter, mock, "list_models", nil)

		msg := receiveAdapterMessage(t, adapter)
		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)

		payload, ok := resp["response"].(map[string]any)
		require.True(t, ok)

		models, ok := payload["models"].([]map[string]any)
		require.True(t, ok)
		require.Len(t, models, 1)
		require.Equal(t, "valid", models[0]["id"])
	})

	t.Run("unsupported subtype returns error response", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		sendControlRequest(t, adapter, mock, "future_subtype", nil)

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "error", resp["subtype"])
		require.Contains(t, resp["error"], "unsupported control request")
	})

	t.Run("rewind_files returns unsupported response", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		sendControlRequest(t, adapter, mock, "rewind_files", map[string]any{
			"user_message_id": "msg_123",
		})

		msg := receiveAdapterMessage(t, adapter)
		require.Equal(t, "control_response", msg["type"])

		resp, ok := msg["response"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "error", resp["subtype"])
		require.Contains(t, resp["error"], "rewind_files")
	})
}

func TestAppServerAdapter_NotificationTranslation(t *testing.T) {
	tests := []struct {
		name           string
		method         string
		params         json.RawMessage
		expectedType   string
		checkItem      bool
		expectedFields map[string]any
	}{
		{
			name:         "thread/started",
			method:       "thread/started",
			params:       json.RawMessage(`{}`),
			expectedType: "thread.started",
		},
		{
			name:         "turn/started",
			method:       "turn/started",
			params:       json.RawMessage(`{}`),
			expectedType: "turn.started",
		},
		{
			name:         "turn/completed with usage",
			method:       "turn/completed",
			params:       json.RawMessage(`{"usage":{"input_tokens":100,"output_tokens":50}}`),
			expectedType: "turn.completed",
		},
		{
			name:         "turn/failed",
			method:       "turn/failed",
			params:       json.RawMessage(`{"error":{"message":"something broke"}}`),
			expectedType: "turn.failed",
		},
		{
			name:         "item/started with agentMessage",
			method:       "item/started",
			params:       json.RawMessage(`{"item":{"id":"item1","type":"agentMessage","text":"hello"}}`),
			expectedType: "item.started",
			checkItem:    true,
			expectedFields: map[string]any{
				"type": "agent_message",
				"text": "hello",
			},
		},
		// item/agentMessage/delta is tested separately in
		// TestAppServerAdapter_DeltaSuppression_DisabledByDefault and
		// TestAppServerAdapter_DeltaEmission_WhenEnabled since default
		// behavior suppresses deltas (returns nil).
		{
			name:         "item/completed with commandExecution",
			method:       "item/completed",
			params:       json.RawMessage(`{"item":{"id":"item2","type":"commandExecution","command":"ls"}}`),
			expectedType: "item.completed",
			checkItem:    true,
			expectedFields: map[string]any{
				"type":    "command_execution",
				"command": "ls",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockAppServerRPC()
			adapter := newTestAdapter(mock)

			mock.notifyCh <- &RPCNotification{
				JSONRPC: "2.0",
				Method:  tc.method,
				Params:  tc.params,
			}

			select {
			case msg := <-adapter.messages:
				require.Equal(t, tc.expectedType, msg["type"])

				if tc.checkItem {
					item, ok := msg["item"].(map[string]any)
					require.True(t, ok, "expected item field")

					for k, v := range tc.expectedFields {
						require.Equal(t, v, item[k], "field %s mismatch", k)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("expected message from notification")
			}

			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		})
	}
}

func TestBuildInitializeRPC_DynamicToolsPassthrough(t *testing.T) {
	dynamicTools := []map[string]any{
		{
			"name":        "add",
			"description": "Add two numbers",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"a": map[string]any{"type": "number"},
					"b": map[string]any{"type": "number"},
				},
			},
		},
	}

	requestData := map[string]any{
		"subtype":      "initialize",
		"dynamicTools": dynamicTools,
	}

	method, params, _, err := buildInitializeRPC(requestData)
	require.NoError(t, err)
	require.Equal(t, "thread/start", method)

	passedTools, ok := params["dynamicTools"].([]map[string]any)
	require.True(t, ok, "dynamicTools should be passed through")
	require.Len(t, passedTools, 1)
	require.Equal(t, "add", passedTools[0]["name"])
}

func TestAppServerAdapter_InitializeFailFastUnsupportedOptions(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	sendControlRequest(t, adapter, mock, "initialize", map[string]any{
		"permissionPromptToolName": "custom",
	})

	msg := receiveAdapterMessage(t, adapter)
	require.Equal(t, "control_response", msg["type"])

	resp, ok := msg["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "error", resp["subtype"])
	require.Contains(t, resp["error"], "permissionPromptToolName")
}

func TestAppServerAdapter_InitializeTurnOverrides(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	callCount := 0
	mock.sendRequestFunc = func(
		_ context.Context,
		method string,
		params any,
	) (*RPCResponse, error) {
		callCount++
		switch callCount {
		case 1:
			require.Equal(t, "thread/start", method)

			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      1,
				Result:  json.RawMessage(`{"threadId":"thr_1"}`),
			}, nil
		case 2:
			require.Equal(t, "turn/start", method)

			return &RPCResponse{
				JSONRPC: "2.0",
				ID:      2,
				Result:  json.RawMessage(`{"turnId":"turn_1"}`),
			}, nil
		default:
			t.Fatalf("unexpected RPC call %d", callCount)

			return &RPCResponse{}, nil
		}
	}

	sendControlRequest(t, adapter, mock, "initialize", map[string]any{
		"reasoningEffort": "high",
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"answer": map[string]any{"type": "string"},
			},
			"required": []string{"answer"},
		},
	})

	msg := receiveAdapterMessage(t, adapter)
	require.Equal(t, "control_response", msg["type"])

	userMsg := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": "hello",
		},
	}

	data, err := json.Marshal(userMsg)
	require.NoError(t, err)

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)

	calls := mock.getSentRequests()
	require.Len(t, calls, 2)
	require.Equal(t, "turn/start", calls[1].Method)

	params, ok := calls[1].Params.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "high", params["effort"])

	outputSchema, ok := params["outputSchema"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", outputSchema["type"])
}

func TestAppServerAdapter_ItemTypeMapping(t *testing.T) {
	tests := []struct {
		camelCase string
		snakeCase string
	}{
		{"agentMessage", "agent_message"},
		{"commandExecution", "command_execution"},
		{"fileChange", "file_change"},
		{"mcpToolCall", "mcp_tool_call"},
		{"webSearch", "web_search"},
		{"todoList", "todo_list"},
		{"reasoning", "reasoning"},
		{"error", "error"},
		{"unknownType", "unknownType"},
	}

	for _, tc := range tests {
		t.Run(tc.camelCase, func(t *testing.T) {
			result := camelToSnake(tc.camelCase)
			require.Equal(t, tc.snakeCase, result)
		})
	}
}

func TestAppServerAdapter_ServerRequest_HookCallback(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.requestCh <- &RPCIncomingRequest{
		JSONRPC: "2.0",
		Method:  "hooks/callback",
		ID:      42,
		Params:  json.RawMessage(`{"callback_id":"hook_0","input":{"hook_event_name":"PreToolUse"}}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "control_request", msg["type"])
		require.Equal(t, "rpc_42", msg["request_id"])

		reqData, ok := msg["request"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "hooks_callback", reqData["subtype"])
	case <-time.After(time.Second):
		t.Fatal("expected control_request from server request")
	}

	adapter.mu.Lock()
	rpcID, ok := adapter.pendingRPCRequests["rpc_42"]
	adapter.mu.Unlock()

	require.True(t, ok)
	require.Equal(t, int64(42), rpcID)

	resp := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": "rpc_42",
			"response":   map[string]any{"continue": true},
		},
	}

	data, err := json.Marshal(resp)
	require.NoError(t, err)

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)

	responses := mock.getSentResponses()
	require.Len(t, responses, 1)
	require.Equal(t, int64(42), responses[0].ID)
	require.Nil(t, responses[0].Error)
}

func TestAppServerAdapter_Close(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	close(adapter.done)
	mock.Close()
	adapter.wg.Wait()

	_, ok := <-adapter.messages
	require.False(t, ok)
}

func TestAppServerAdapter_EndInput_NoOp(t *testing.T) {
	adapter := &AppServerAdapter{}

	err := adapter.EndInput()
	require.NoError(t, err)
}

func TestAppServerAdapter_ConcurrentSendMessage(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	const numSenders = 10

	var wg sync.WaitGroup

	wg.Add(numSenders)

	for i := range numSenders {
		go func(id int) {
			defer wg.Done()

			userMsg := map[string]any{
				"type": "user",
				"message": map[string]any{
					"role":    "user",
					"content": "msg",
				},
			}

			data, err := json.Marshal(userMsg)
			require.NoError(t, err)

			_ = adapter.SendMessage(context.Background(), data)
		}(i)
	}

	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent sends deadlocked")
	}
}

func TestAppServerAdapter_IsReady_Delegates(t *testing.T) {
	opts := &config.Options{}
	log := slog.Default()
	adapter := NewAppServerAdapter(log, opts)

	require.False(t, adapter.IsReady())
}

func TestAppServerAdapter_UnknownMessageType(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	unknownMsg := map[string]any{
		"type": "some_unknown_type",
		"data": "test",
	}

	data, err := json.Marshal(unknownMsg)
	require.NoError(t, err)

	err = adapter.SendMessage(context.Background(), data)
	require.NoError(t, err)
}

func TestAppServerAdapter_UserMessageItem(t *testing.T) {
	t.Run("item/started with userMessage emits user message", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.notifyCh <- &RPCNotification{
			JSONRPC: "2.0",
			Method:  "item/started",
			Params: json.RawMessage(`{
				"item":{
					"type":"userMessage",
					"id":"msg_001",
					"content":[{"type":"text","text":"What is 2+2?"}]
				}
			}`),
		}

		select {
		case msg := <-adapter.messages:
			require.Equal(t, "user", msg["type"])

			message, ok := msg["message"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "user", message["role"])
			require.Equal(t, "What is 2+2?", message["content"])
			require.Equal(t, "msg_001", msg["uuid"])
		case <-time.After(time.Second):
			t.Fatal("expected user message from userMessage item/started")
		}
	})

	t.Run("item/completed with userMessage emits system message", func(t *testing.T) {
		mock := newMockAppServerRPC()
		adapter := newTestAdapter(mock)

		defer func() {
			close(adapter.done)
			mock.Close()
			adapter.wg.Wait()
		}()

		mock.notifyCh <- &RPCNotification{
			JSONRPC: "2.0",
			Method:  "item/completed",
			Params: json.RawMessage(`{
				"item":{
					"type":"userMessage",
					"id":"msg_001",
					"content":[{"type":"text","text":"What is 2+2?"}]
				}
			}`),
		}

		select {
		case msg := <-adapter.messages:
			require.Equal(t, "system", msg["type"])
			require.Equal(t, "user_message.completed", msg["subtype"])
		case <-time.After(time.Second):
			t.Fatal("expected system message from userMessage item/completed")
		}
	})
}

func TestAppServerAdapter_ReasoningItem_Summary(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "item/completed",
		Params: json.RawMessage(`{
			"item":{
				"type":"reasoning",
				"id":"reason_1",
				"summary":["The answer is","4."],
				"content":[]
			}
		}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "item.completed", msg["type"])

		item, ok := msg["item"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "reasoning", item["type"])
		require.Equal(t, "The answer is\n4.", item["text"])
	case <-time.After(time.Second):
		t.Fatal("expected item.completed with reasoning text")
	}
}

func TestAppServerAdapter_ReasoningItem_EmptySummary(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "item/started",
		Params: json.RawMessage(`{
			"item":{
				"type":"reasoning",
				"id":"reason_2",
				"summary":[],
				"content":[]
			}
		}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "item.started", msg["type"])

		item, ok := msg["item"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "reasoning", item["type"])

		// With empty summary, text should not be set.
		_, hasText := item["text"].(string)
		require.False(t, hasText, "empty summary should not produce text field")
	case <-time.After(time.Second):
		t.Fatal("expected item.started for reasoning")
	}
}

func TestAppServerAdapter_TokenUsageCaching(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	// Send token usage first.
	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "thread/tokenUsage/updated",
		Params: json.RawMessage(`{
			"tokenUsage":{
				"last":{
					"totalTokens":9991,
					"inputTokens":9954,
					"cachedInputTokens":7552,
					"outputTokens":37,
					"reasoningOutputTokens":30
				}
			}
		}`),
	}

	// Drain the system message from tokenUsage.
	select {
	case <-adapter.messages:
	case <-time.After(time.Second):
		t.Fatal("expected system message from token usage")
	}

	// Now send turn/completed without inline usage.
	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "turn/completed",
		Params:  json.RawMessage(`{}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "turn.completed", msg["type"])

		usage, ok := msg["usage"].(map[string]any)
		require.True(t, ok, "expected usage from cached token data")
		require.Equal(t, float64(9954), usage["input_tokens"])
		require.Equal(t, float64(37), usage["output_tokens"])
		require.Equal(t, float64(7552), usage["cached_input_tokens"])
		require.Equal(t, float64(30), usage["reasoning_output_tokens"])
	case <-time.After(time.Second):
		t.Fatal("expected turn.completed with cached usage")
	}
}

func TestAppServerAdapter_TurnCompleted_InlineUsage(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	// Pre-cache some token usage.
	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "thread/tokenUsage/updated",
		Params: json.RawMessage(`{
			"tokenUsage":{"last":{"totalTokens":100,"inputTokens":80,"outputTokens":20}}
		}`),
	}

	select {
	case <-adapter.messages:
	case <-time.After(time.Second):
		t.Fatal("expected system message from token usage")
	}

	// Send turn/completed WITH inline usage — should prefer inline.
	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "turn/completed",
		Params:  json.RawMessage(`{"usage":{"input_tokens":500,"output_tokens":200}}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "turn.completed", msg["type"])

		usage, ok := msg["usage"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, float64(500), usage["input_tokens"])
		require.Equal(t, float64(200), usage["output_tokens"])
	case <-time.After(time.Second):
		t.Fatal("expected turn.completed with inline usage")
	}
}

func TestAppServerAdapter_TurnCompleted_ResultFallbackFromAssistantText(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "item/completed",
		Params: json.RawMessage(`{
			"item":{
				"id":"item_assistant_1",
				"type":"agentMessage",
				"text":"{\"answer\":\"ok\"}"
			}
		}`),
	}

	// Drain the item.completed message.
	select {
	case <-adapter.messages:
	case <-time.After(time.Second):
		t.Fatal("expected item.completed message")
	}

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "turn/completed",
		Params:  json.RawMessage(`{}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "turn.completed", msg["type"])
		require.Equal(t, "{\"answer\":\"ok\"}", msg["result"])
	case <-time.After(time.Second):
		t.Fatal("expected turn.completed with result fallback")
	}
}

func TestAppServerAdapter_TokenUsageEmitsSystem(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "thread/tokenUsage/updated",
		Params:  json.RawMessage(`{"tokenUsage":{"last":{"totalTokens":42}}}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "system", msg["type"])
		require.Equal(t, "thread.token_usage.updated", msg["subtype"])

		data, ok := msg["data"].(map[string]any)
		require.True(t, ok)
		require.NotNil(t, data["tokenUsage"])
	case <-time.After(time.Second):
		t.Fatal("expected system message from token usage notification")
	}
}

func TestAppServerAdapter_RateLimitsNotification(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "account/rateLimits/updated",
		Params:  json.RawMessage(`{"limits":{"rpm":100}}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "system", msg["type"])
		require.Equal(t, "account.rate_limits.updated", msg["subtype"])

		data, ok := msg["data"].(map[string]any)
		require.True(t, ok)
		require.NotNil(t, data["limits"])
	case <-time.After(time.Second):
		t.Fatal("expected system message from rate limits notification")
	}
}

func TestAppServerAdapter_CodexEvent_Duplicates(t *testing.T) {
	duplicates := []string{
		"codex/event/item_started",
		"codex/event/item_completed",
		"codex/event/agent_message_content_delta",
		"codex/event/agent_message_delta",
		"codex/event/agent_message",
		"codex/event/user_message",
	}

	for _, method := range duplicates {
		t.Run(method, func(t *testing.T) {
			mock := newMockAppServerRPC()
			adapter := newTestAdapter(mock)

			defer func() {
				close(adapter.done)
				mock.Close()
				adapter.wg.Wait()
			}()

			mock.notifyCh <- &RPCNotification{
				JSONRPC: "2.0",
				Method:  method,
				Params:  json.RawMessage(`{"data":"test"}`),
			}

			// Duplicate events should be dropped (nil), so nothing
			// should arrive on the messages channel.
			select {
			case msg := <-adapter.messages:
				t.Fatalf("expected nil for duplicate %s, got: %v", method, msg)
			case <-time.After(100 * time.Millisecond):
				// Expected: no message produced.
			}
		})
	}
}

func TestAppServerAdapter_CodexEvent_Unique(t *testing.T) {
	tests := []struct {
		method          string
		expectedSubtype string
	}{
		{"codex/event/task_started", "task.started"},
		{"codex/event/task_complete", "task.complete"},
		{"codex/event/token_count", "token.count"},
		{"codex/event/mcp_startup_update", "mcp.startup_update"},
		{"codex/event/mcp_startup_complete", "mcp.startup_complete"},
		{"codex/event/some_future_event", "codex.event.some_future_event"},
	}

	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			mock := newMockAppServerRPC()
			adapter := newTestAdapter(mock)

			defer func() {
				close(adapter.done)
				mock.Close()
				adapter.wg.Wait()
			}()

			mock.notifyCh <- &RPCNotification{
				JSONRPC: "2.0",
				Method:  tc.method,
				Params:  json.RawMessage(`{"info":"test"}`),
			}

			select {
			case msg := <-adapter.messages:
				require.Equal(t, "system", msg["type"])
				require.Equal(t, tc.expectedSubtype, msg["subtype"])

				data, ok := msg["data"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, "test", data["info"])
			case <-time.After(time.Second):
				t.Fatalf("expected system message for %s", tc.method)
			}
		})
	}
}

func TestAppServerAdapter_DeltaSuppression_DisabledByDefault(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	// includePartialMessages defaults to false in newTestAdapter.
	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "item/agentMessage/delta",
		Params:  json.RawMessage(`{"delta":"hello","itemId":"item_1"}`),
	}

	// Delta should be suppressed — nothing should arrive.
	select {
	case msg := <-adapter.messages:
		t.Fatalf("expected delta to be suppressed, got: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: no message produced.
	}
}

func TestAppServerAdapter_DeltaEmission_WhenEnabled(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)
	adapter.includePartialMessages = true
	adapter.threadID = "thr_test"

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "item/agentMessage/delta",
		Params:  json.RawMessage(`{"delta":"world","itemId":"item_2"}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "stream_event", msg["type"])
		require.Equal(t, "item_2", msg["uuid"])
		require.Equal(t, "thr_test", msg["session_id"])

		event, ok := msg["event"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "content_block_delta", event["type"])

		delta, ok := event["delta"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "text_delta", delta["type"])
		require.Equal(t, "world", delta["text"])
	case <-time.After(time.Second):
		t.Fatal("expected stream_event message from delta")
	}
}

func TestAppServerAdapter_UnknownNotification_PassThrough(t *testing.T) {
	mock := newMockAppServerRPC()
	adapter := newTestAdapter(mock)

	defer func() {
		close(adapter.done)
		mock.Close()
		adapter.wg.Wait()
	}()

	mock.notifyCh <- &RPCNotification{
		JSONRPC: "2.0",
		Method:  "some/future/method",
		Params:  json.RawMessage(`{"key":"value"}`),
	}

	select {
	case msg := <-adapter.messages:
		require.Equal(t, "system", msg["type"])
		require.Equal(t, "some/future/method", msg["subtype"])

		data, ok := msg["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "value", data["key"])
	case <-time.After(time.Second):
		t.Fatal("expected pass-through system message for unknown notification")
	}
}
