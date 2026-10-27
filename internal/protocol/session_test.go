package protocol

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/mafilus/codex-agent-sdk-go/internal/mcp"
	"github.com/mafilus/codex-agent-sdk-go/internal/permission"
	"github.com/mafilus/codex-agent-sdk-go/internal/userinput"
	mcpproto "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestSession_MCPToolsAdvertisedAndDispatched(t *testing.T) {
	server := mcp.NewSDKServer("probe", "1.0.0")
	var received map[string]any
	server.AddTool(&mcpproto.Tool{
		Name: "echo", Description: "Echo a message",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, req *mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
		var err error
		received, err = mcp.ParseArguments(req)
		return mcp.TextResult("echoed"), err
	})
	session := NewSession(slog.Default(), nil, &config.Options{
		MCPServers: map[string]mcp.ServerConfig{
			"probe": &mcp.SdkServerConfig{Instance: server},
		},
		SDKTools: []*config.DynamicTool{{Name: "plain", Description: "Plain tool"}},
	})
	session.RegisterMCPServers()
	tools, ok := session.buildInitializePayload()["dynamicTools"].([]map[string]any)
	require.True(t, ok, "dynamicTools must be a tool list")
	require.Len(t, tools, 2)
	require.Equal(t, "plain", tools[0]["name"])
	require.Equal(t, "sdk_mcp__probe__echo", tools[1]["name"])
	require.Equal(t, "Echo a message", tools[1]["description"])
	require.Equal(t, map[string]any{"type": "object"}, tools[1]["inputSchema"])

	result, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
		Request: map[string]any{
			"tool": tools[1]["name"], "arguments": map[string]any{"message": "hello"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"message": "hello"}, received)
	require.Equal(t, true, result["success"])
	content, ok := result["contentItems"].([]map[string]any)
	require.True(t, ok, "contentItems must be a content list")
	require.NotEmpty(t, content)
	require.Equal(t, "echoed", content[0]["text"])

	// A colliding explicit tool keeps its name; the MCP alias stays unique.
	session.options.SDKTools = append(session.options.SDKTools, &config.DynamicTool{
		Name: "sdk_mcp__probe__echo", Description: "Explicit override",
	})
	tools, ok = session.buildInitializePayload()["dynamicTools"].([]map[string]any)
	require.True(t, ok, "dynamicTools must be a tool list")
	require.Len(t, tools, 3)
	require.Equal(t, "Explicit override", tools[1]["description"])
	require.Equal(t, "sdk_mcp__probe__echo_2", tools[2]["name"])
}

func TestSession_MCPAliasesAndEmptySchema(t *testing.T) {
	opts := &config.Options{MCPServers: map[string]mcp.ServerConfig{}}
	for serverName, toolName := range map[string]string{"a": "b__c", "a__b": "c"} {
		server := mcp.NewSDKServer(serverName, "1.0.0")
		server.AddTool(&mcpproto.Tool{Name: toolName}, func(_ context.Context, _ *mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
			return mcp.TextResult(serverName), nil
		})
		opts.MCPServers[serverName] = &mcp.SdkServerConfig{Instance: server}
	}
	session := NewSession(slog.Default(), nil, opts)
	session.RegisterMCPServers()
	tools, ok := session.buildInitializePayload()["dynamicTools"].([]map[string]any)
	require.True(t, ok, "dynamicTools must be a tool list")
	require.Len(t, tools, 2)
	require.NotEqual(t, tools[0]["name"], tools[1]["name"])
	for i, expected := range []string{"a", "a__b"} {
		require.Equal(t, map[string]any{"type": "object", "properties": map[string]any{}}, tools[i]["inputSchema"])
		result, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
			Request: map[string]any{"tool": tools[i]["name"], "arguments": map[string]any{}},
		})
		require.NoError(t, err)
		require.Equal(t, true, result["success"])
		content, ok := result["contentItems"].([]map[string]any)
		require.True(t, ok, "contentItems must be a content list")
		require.NotEmpty(t, content)
		require.Equal(t, expected, content[0]["text"])
	}
}

func TestSession_MCPToolPermissions(t *testing.T) {
	for _, denied := range []bool{false, true} {
		for _, alias := range []bool{false, true} {
			t.Run(fmt.Sprintf("denied=%t/alias=%t", denied, alias), func(t *testing.T) {
				server := mcp.NewSDKServer("probe", "1.0.0")
				calls := 0
				server.AddTool(&mcpproto.Tool{Name: "echo"}, func(_ context.Context, req *mcpproto.CallToolRequest) (*mcpproto.CallToolResult, error) {
					calls++
					args, err := mcp.ParseArguments(req)
					require.Equal(t, "updated", args["message"])
					return mcp.TextResult("ok"), err
				})
				opts := &config.Options{
					MCPServers: map[string]mcp.ServerConfig{"probe": &mcp.SdkServerConfig{Instance: server}},
					CanUseTool: func(_ context.Context, name string, _ map[string]any, _ *permission.Context) (permission.Result, error) {
						require.Equal(t, "mcp__probe__echo", name)
						return &permission.ResultAllow{UpdatedInput: map[string]any{"message": "updated"}}, nil
					},
				}
				if denied {
					opts.DisallowedTools = []string{"mcp__probe__echo"}
				}
				require.NoError(t, config.ConfigureToolPermissionPolicy(opts))
				session := NewSession(slog.Default(), nil, opts)
				session.RegisterMCPServers()
				tools, ok := session.buildInitializePayload()["dynamicTools"].([]map[string]any)
				require.True(t, ok, "dynamicTools must be a tool list")
				require.NotEmpty(t, tools)
				name := any("mcp__probe__echo")
				if alias {
					name = tools[0]["name"]
				}
				result, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
					Request: map[string]any{"tool": name, "arguments": map[string]any{"message": "original"}},
				})
				require.NoError(t, err)
				require.Equal(t, !denied, result["success"])
				if denied {
					require.Zero(t, calls)
				} else {
					require.Equal(t, 1, calls)
				}
			})
		}
	}
}

// TestSession_NeedsInitialization_Empty tests that NeedsInitialization returns false
// when no CanUseTool or MCP servers are configured.
func TestSession_NeedsInitialization_Empty(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log:             log,
		options:         &config.Options{},
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	require.False(t, session.NeedsInitialization(),
		"Expected NeedsInitialization() to return false with empty options")
}

func TestSession_NeedsInitialization_AdvancedOptionsAlone(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log: log,
		options: &config.Options{
			Resume:               "thread_123",
			ContinueConversation: true,
			OutputFormat:         map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object"}},
		},
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	require.False(t, session.NeedsInitialization(),
		"Expected NeedsInitialization() to remain false without callbacks/MCP")
}

func TestSession_NeedsInitialization_WithDynamicTools(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log:           log,
		options:       &config.Options{},
		sdkMcpServers: make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: map[string]*config.DynamicTool{
			"add": {Name: "add", Description: "Add numbers"},
		},
	}

	require.True(t, session.NeedsInitialization(),
		"Expected NeedsInitialization() to return true with dynamic tools")
}

func TestSession_RegisterDynamicTools(t *testing.T) {
	log := slog.Default()

	tools := []*config.DynamicTool{
		{
			Name:        "add",
			Description: "Add two numbers",
			InputSchema: map[string]any{"type": "object"},
			Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
				return map[string]any{"result": 42}, nil
			},
		},
		{
			Name:        "multiply",
			Description: "Multiply two numbers",
			Handler: func(_ context.Context, _ map[string]any) (map[string]any, error) {
				return map[string]any{"result": 6}, nil
			},
		},
	}

	session := NewSession(log, nil, &config.Options{SDKTools: tools})
	session.RegisterDynamicTools()

	require.Len(t, session.sdkDynamicTools, 2)
	require.NotNil(t, session.sdkDynamicTools["add"])
	require.NotNil(t, session.sdkDynamicTools["multiply"])
	require.Equal(t, "Add two numbers", session.sdkDynamicTools["add"].Description)
}

func TestSession_BuildInitializePayload_DynamicTools(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log: log,
		options: &config.Options{
			SDKTools: []*config.DynamicTool{
				{
					Name:        "add",
					Description: "Add two numbers",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"a": map[string]any{"type": "number"},
							"b": map[string]any{"type": "number"},
						},
						"required": []string{"a", "b"},
					},
				},
				{
					Name:        "greet",
					Description: "Say hello",
				},
			},
		},
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	payload := session.buildInitializePayload()

	dynamicTools, ok := payload["dynamicTools"].([]map[string]any)
	require.True(t, ok, "dynamicTools should be []map[string]any")
	require.Len(t, dynamicTools, 2)

	require.Equal(t, "add", dynamicTools[0]["name"])
	require.Equal(t, "Add two numbers", dynamicTools[0]["description"])
	require.NotNil(t, dynamicTools[0]["inputSchema"])

	inputSchema, ok := dynamicTools[0]["inputSchema"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", inputSchema["type"])

	require.Equal(t, "greet", dynamicTools[1]["name"])
	require.Equal(t, "Say hello", dynamicTools[1]["description"])
	require.Nil(t, dynamicTools[1]["inputSchema"])
}

func TestSession_HandleDynamicToolCall_PlainName(t *testing.T) {
	log := slog.Default()

	var calledWith map[string]any

	session := NewSession(log, nil, &config.Options{})
	session.sdkDynamicTools["add"] = &config.DynamicTool{
		Name: "add",
		Handler: func(_ context.Context, input map[string]any) (map[string]any, error) {
			calledWith = input

			return map[string]any{"result": 42}, nil
		},
	}

	resp, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
		Request: map[string]any{
			"tool":      "add",
			"arguments": map[string]any{"a": float64(5), "b": float64(3)},
		},
	})

	require.NoError(t, err)
	require.Equal(t, true, resp["success"])
	require.NotNil(t, calledWith)
	require.Equal(t, float64(5), calledWith["a"])

	items, ok := resp["contentItems"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Contains(t, items[0]["text"], "42")
}

func TestSession_HandleDynamicToolCall_FallbackMCP(t *testing.T) {
	log := slog.Default()

	session := NewSession(log, nil, &config.Options{})
	// No dynamic tools registered, but we have an MCP server
	// The MCP fallback should try to parse the name and fail
	// since no MCP server is registered.

	resp, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
		Request: map[string]any{
			"tool":      "mcp__sdk__calc",
			"arguments": map[string]any{},
		},
	})

	require.NoError(t, err)
	require.Equal(t, false, resp["success"])

	items, ok := resp["contentItems"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Contains(t, items[0]["text"], "SDK MCP server not found")
}

func TestSession_HandleDynamicToolCall_UnknownTool(t *testing.T) {
	log := slog.Default()

	session := NewSession(log, nil, &config.Options{})

	resp, err := session.HandleDynamicToolCall(context.Background(), &ControlRequest{
		Request: map[string]any{
			"tool":      "nonexistent",
			"arguments": map[string]any{},
		},
	})

	require.NoError(t, err)
	require.Equal(t, false, resp["success"])

	items, ok := resp["contentItems"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	require.Contains(t, items[0]["text"], "unknown tool")
}

func TestSession_BuildInitializePayload_IncludesAdvancedFields(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log: log,
		options: &config.Options{
			Model:                "gpt-5",
			Cwd:                  "/tmp/project",
			ContinueConversation: true,
			Resume:               "thread_abc",
			ForkSession:          true,
			AddDirs:              []string{"/tmp/extra"},
			OutputFormat: map[string]any{
				"type": "json_schema",
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"answer": map[string]any{"type": "string"},
					},
				},
			},
		},
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	payload := session.buildInitializePayload()

	require.Equal(t, "gpt-5", payload["model"])
	require.Equal(t, "/tmp/project", payload["cwd"])
	require.Equal(t, true, payload["continueConversation"])
	require.Equal(t, "thread_abc", payload["resume"])
	require.Equal(t, true, payload["forkSession"])
	require.Equal(t, []string{"/tmp/extra"}, payload["addDirs"])

	outputSchema, ok := payload["outputSchema"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", outputSchema["type"])
}

// TestSession_InitializationResult_DataRace tests for data race between
// writing initializationResult and reading it via GetInitializationResult().
// Run with: go test -race -run TestSession_InitializationResult_DataRace.
func TestSession_InitializationResult_DataRace(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log:             log,
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	const iterations = 1000

	var wg sync.WaitGroup

	// Writer goroutine: simulates what Initialize() does (with mutex protection)

	wg.Go(func() {
		for i := range iterations {
			session.initMu.Lock()
			session.initializationResult = map[string]any{
				"iteration": i,
				"data":      "test",
			}
			session.initMu.Unlock()
		}
	})

	// Reader goroutine: simulates concurrent GetInitializationResult() calls

	wg.Go(func() {
		for range iterations {
			result := session.GetInitializationResult()

			// Access the map to ensure the race detector catches any issues
			if result != nil {
				_ = len(result)
			}
		}
	})

	wg.Wait()
}

// TestSession_InitializationResult_ConcurrentReadWrite tests the race between
// a single write and multiple concurrent reads.
// Run with: go test -race -run TestSession_InitializationResult_ConcurrentReadWrite.
func TestSession_InitializationResult_ConcurrentReadWrite(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log:             log,
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	const (
		readers    = 10
		iterations = 1000
	)

	var wg sync.WaitGroup

	// Single writer (simulates Initialize with mutex protection)

	wg.Go(func() {
		for i := range iterations {
			session.initMu.Lock()
			session.initializationResult = map[string]any{
				"version": "1.0.0",
				"count":   i,
			}
			session.initMu.Unlock()
		}
	})

	// Multiple readers using GetInitializationResult()
	for range readers {
		wg.Go(func() {
			for range iterations {
				result := session.GetInitializationResult()
				if result != nil {
					// Access map contents - safe because we received a copy
					_ = result["version"]
					_ = result["count"]
				}
			}
		})
	}

	wg.Wait()
}

func TestSession_HandleRequestUserInput_NoCallback(t *testing.T) {
	log := slog.Default()

	session := NewSession(log, nil, &config.Options{})

	resp, err := session.HandleRequestUserInput(context.Background(), &ControlRequest{
		Request: map[string]any{
			"subtype":   "item_tool/requestUserInput",
			"item_id":   "item_1",
			"thread_id": "thread_1",
			"questions": []any{
				map[string]any{
					"id":       "q1",
					"question": "Pick a language",
					"options": []any{
						map[string]any{"label": "Go", "description": "Fast compiled"},
					},
				},
			},
		},
	})

	require.NoError(t, err)

	answers, ok := resp["answers"].(map[string]any)
	require.True(t, ok)
	require.Empty(t, answers, "no callback should return empty answers")
}

func TestSession_HandleRequestUserInput_WithCallback(t *testing.T) {
	log := slog.Default()

	var captured *userinput.Request

	session := NewSession(log, nil, &config.Options{
		OnUserInput: func(_ context.Context, req *userinput.Request) (*userinput.Response, error) {
			captured = req

			answers := make(map[string]*userinput.Answer, 1)
			answers[req.Questions[0].ID] = &userinput.Answer{
				Answers: []string{req.Questions[0].Options[1].Label},
			}

			return &userinput.Response{Answers: answers}, nil
		},
	})

	resp, err := session.HandleRequestUserInput(context.Background(), &ControlRequest{
		Request: map[string]any{
			"subtype":   "item_tool/requestUserInput",
			"item_id":   "item_42",
			"thread_id": "thread_7",
			"turn_id":   "turn_3",
			"questions": []any{
				map[string]any{
					"id":       "lang",
					"header":   "Language",
					"question": "Which language?",
					"options": []any{
						map[string]any{"label": "Go", "description": "Fast compiled"},
						map[string]any{"label": "Rust", "description": "Memory safe"},
						map[string]any{"label": "Python", "description": "Interpreted"},
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, "item_42", captured.ItemID)
	require.Equal(t, "thread_7", captured.ThreadID)
	require.Equal(t, "turn_3", captured.TurnID)
	require.Len(t, captured.Questions, 1)
	require.Equal(t, "lang", captured.Questions[0].ID)
	require.Equal(t, "Language", captured.Questions[0].Header)
	require.Equal(t, "Which language?", captured.Questions[0].Question)
	require.Len(t, captured.Questions[0].Options, 3)
	require.Equal(t, "Rust", captured.Questions[0].Options[1].Label)

	answers, ok := resp["answers"].(map[string]any)
	require.True(t, ok)

	langAnswer, ok := answers["lang"].(map[string]any)
	require.True(t, ok)

	answerList, ok := langAnswer["answers"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"Rust"}, answerList)
}

func TestSession_HandleRequestUserInput_FreeText(t *testing.T) {
	log := slog.Default()

	session := NewSession(log, nil, &config.Options{
		OnUserInput: func(_ context.Context, req *userinput.Request) (*userinput.Response, error) {
			answers := make(map[string]*userinput.Answer, 1)
			answers[req.Questions[0].ID] = &userinput.Answer{
				Answers: []string{"my free text answer"},
			}

			return &userinput.Response{Answers: answers}, nil
		},
	})

	resp, err := session.HandleRequestUserInput(context.Background(), &ControlRequest{
		Request: map[string]any{
			"subtype":   "item_tool/requestUserInput",
			"item_id":   "item_1",
			"thread_id": "thread_1",
			"questions": []any{
				map[string]any{
					"id":       "name",
					"question": "What is your name?",
				},
			},
		},
	})

	require.NoError(t, err)

	answers, ok := resp["answers"].(map[string]any)
	require.True(t, ok)

	nameAnswer, ok := answers["name"].(map[string]any)
	require.True(t, ok)

	answerList, ok := nameAnswer["answers"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"my free text answer"}, answerList)
}

func TestSession_NeedsInitialization_WithOnUserInput(t *testing.T) {
	log := slog.Default()

	session := &Session{
		log: log,
		options: &config.Options{
			OnUserInput: func(_ context.Context, _ *userinput.Request) (*userinput.Response, error) {
				return &userinput.Response{}, nil
			},
		},
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}

	require.True(t, session.NeedsInitialization(),
		"Expected NeedsInitialization() to return true with OnUserInput set")
}

func TestMapPermissionToApprovalPolicy_Plan(t *testing.T) {
	require.Equal(t, "on-request", mapPermissionToApprovalPolicy("plan"))
}
