package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	"github.com/mafilus/codex-agent-sdk-go/internal/mcp"
	"github.com/mafilus/codex-agent-sdk-go/internal/permission"
	"github.com/mafilus/codex-agent-sdk-go/internal/userinput"
)

const (
	// defaultInitializeTimeout is the default timeout for initialize control requests.
	defaultInitializeTimeout = 60 * time.Second
)

// Session encapsulates protocol handling logic for MCP servers and callbacks.
type Session struct {
	log        *slog.Logger
	controller *Controller
	options    *config.Options

	sdkMcpServers      map[string]mcp.ServerInstance
	sdkDynamicTools    map[string]*config.DynamicTool
	sdkMcpToolBindings map[string]mcpToolBinding

	initMu               sync.RWMutex
	initializationResult map[string]any
}

type mcpToolBinding struct {
	server string
	tool   string
}

// NewSession creates a new Session for protocol handling.
func NewSession(
	log *slog.Logger,
	controller *Controller,
	options *config.Options,
) *Session {
	return &Session{
		log:             log.With("component", "session"),
		controller:      controller,
		options:         options,
		sdkMcpServers:   make(map[string]mcp.ServerInstance, 4),
		sdkDynamicTools: make(map[string]*config.DynamicTool, 4),
	}
}

// RegisterHandlers registers protocol handlers for MCP tool calls and
// command approval requests.
func (s *Session) RegisterHandlers() {
	s.controller.RegisterHandler("item_tool/call", s.HandleDynamicToolCall)
	s.controller.RegisterHandler("can_use_tool", s.HandleCanUseTool)
	s.controller.RegisterHandler("item_commandExecution/requestApproval", s.HandleCanUseTool)
	s.controller.RegisterHandler("item_commandExecution_requestApproval", s.HandleCanUseTool)
	s.controller.RegisterHandler("item_tool/requestUserInput", s.HandleRequestUserInput)
}

// RegisterMCPServers extracts and registers SDK MCP servers from options.
func (s *Session) RegisterMCPServers() {
	if s.options == nil || s.options.MCPServers == nil {
		return
	}

	for serverKey, serverConfig := range s.options.MCPServers {
		if serverConfig == nil {
			continue
		}

		sdkConfig, ok := serverConfig.(*mcp.SdkServerConfig)
		if !ok {
			continue
		}

		if sdkConfig.Instance == nil {
			continue
		}

		server, ok := sdkConfig.Instance.(mcp.ServerInstance)
		if !ok {
			continue
		}

		s.sdkMcpServers[serverKey] = server
		s.log.Debug("registered SDK MCP server", slog.String("server", serverKey))
	}
}

// RegisterDynamicTools indexes SDK dynamic tools by name for dispatch.
func (s *Session) RegisterDynamicTools() {
	if s.options == nil || len(s.options.SDKTools) == 0 {
		return
	}

	for _, tool := range s.options.SDKTools {
		s.sdkDynamicTools[tool.Name] = tool
		s.log.Debug("registered dynamic tool", slog.String("tool", tool.Name))
	}
}

// Initialize sends the initialization control request to the CLI.
func (s *Session) Initialize(ctx context.Context) error {
	s.log.DebugContext(ctx, "sending initialize request")

	payload := s.buildInitializePayload()

	timeout := s.getInitializeTimeout()

	resp, err := s.controller.SendRequest(ctx, "initialize", payload, timeout)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}

	s.initMu.Lock()
	s.initializationResult = resp.Payload()
	s.initMu.Unlock()

	return nil
}

// buildInitializePayload builds thread/start initialization payload from options.
func (s *Session) buildInitializePayload() map[string]any {
	payload := make(map[string]any, 16)

	if s.options == nil {
		return payload
	}

	if s.options.Model != "" {
		payload["model"] = s.options.Model
	}

	if s.options.Cwd != "" {
		payload["cwd"] = s.options.Cwd
	}

	if s.options.SystemPromptPreset != nil {
		payload["systemPromptPreset"] = s.options.SystemPromptPreset
	} else if s.options.SystemPrompt != "" {
		payload["systemPrompt"] = s.options.SystemPrompt
	}

	if s.options.ContinueConversation {
		payload["continueConversation"] = true
	}

	if s.options.Resume != "" {
		payload["resume"] = s.options.Resume
	}

	if s.options.ForkSession {
		payload["forkSession"] = true
	}

	if s.options.Effort != nil {
		payload["reasoningEffort"] = string(*s.options.Effort)
	}

	sandboxMode := s.options.Sandbox
	if sandboxMode == "" {
		sandboxMode = mapPermissionToSandbox(s.options.PermissionMode)
	}

	if sandboxMode != "" {
		payload["sandbox"] = sandboxMode
	}

	if approvalPolicy := mapPermissionToApprovalPolicy(s.options.PermissionMode); approvalPolicy != "" {
		payload["approvalPolicy"] = approvalPolicy
	}

	if s.options.PermissionMode != "" {
		payload["permissionMode"] = s.options.PermissionMode
	}

	if len(s.options.AllowedTools) > 0 {
		payload["allowedTools"] = s.options.AllowedTools
	}

	if len(s.options.DisallowedTools) > 0 {
		payload["disallowedTools"] = s.options.DisallowedTools
	}

	switch t := s.options.Tools.(type) {
	case config.ToolsList:
		payload["tools"] = t
	case *config.ToolsPreset:
		payload["tools"] = t
	}

	if len(s.options.AddDirs) > 0 {
		payload["addDirs"] = s.options.AddDirs
	}

	if servers := serializeMCPServers(s.options.MCPServers); len(servers) > 0 {
		payload["mcpServers"] = servers
	}

	dynamicTools := serializeDynamicTools(s.options.SDKTools)
	s.sdkMcpToolBindings = make(map[string]mcpToolBinding)
	names := make(map[string]bool, len(dynamicTools))
	for _, tool := range dynamicTools {
		name, _ := tool["name"].(string)
		names[name] = true
	}
	// In-process MCP servers cannot be launched by Codex. Advertise their tools
	// through the app-server dynamic-tool API. Codex reserves the mcp__ prefix,
	// so keep an exact binding from each unique wire alias to its MCP handler.
	for _, serverName := range slices.Sorted(maps.Keys(s.sdkMcpServers)) {
		tools := s.sdkMcpServers[serverName].ListTools()
		sort.Slice(tools, func(i, j int) bool {
			return fmt.Sprint(tools[i]["name"]) < fmt.Sprint(tools[j]["name"])
		})
		for _, tool := range tools {
			toolName, _ := tool["name"].(string)
			base := "sdk_mcp__" + serverName + "__" + toolName
			name := base
			for suffix := 2; names[name]; suffix++ {
				name = base + "_" + strconv.Itoa(suffix)
			}
			schema := tool["inputSchema"]
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			dynamicTools = append(dynamicTools, map[string]any{
				"name":        name,
				"description": tool["description"],
				"inputSchema": schema,
			})
			names[name] = true
			s.sdkMcpToolBindings[name] = mcpToolBinding{server: serverName, tool: toolName}
		}
	}
	if len(dynamicTools) > 0 {
		payload["dynamicTools"] = dynamicTools
	}

	if len(s.options.Config) > 0 {
		cfg := make(map[string]any, len(s.options.Config))
		for k, v := range s.options.Config {
			cfg[k] = v
		}

		payload["config"] = cfg
	}

	if s.options.OutputSchema != "" {
		payload["outputSchema"] = s.options.OutputSchema
	} else if schema := extractOutputSchema(s.options.OutputFormat); schema != nil {
		payload["outputSchema"] = schema
	}

	if s.options.PermissionPromptToolName != "" {
		payload["permissionPromptToolName"] = s.options.PermissionPromptToolName
	}

	return payload
}

// serializeDynamicTools converts SDK dynamic tools into the flat array format
// expected by the Codex CLI dynamicTools API.
func serializeDynamicTools(tools []*config.DynamicTool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}

	result := make([]map[string]any, 0, len(tools))

	for _, tool := range tools {
		entry := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
		}

		if tool.InputSchema != nil {
			entry["inputSchema"] = tool.InputSchema
		}

		result = append(result, entry)
	}

	return result
}

// serializeMCPServers converts MCP server configs into a map suitable for the
// initialize payload. SDK servers are serialized as {"type":"sdk"} so the CLI
// routes tool calls back through the control protocol.
func serializeMCPServers(servers map[string]mcp.ServerConfig) map[string]any {
	if len(servers) == 0 {
		return nil
	}

	result := make(map[string]any, len(servers))

	for name, serverCfg := range servers {
		if serverCfg == nil {
			continue
		}

		switch cfg := serverCfg.(type) {
		case *mcp.StdioServerConfig:
			entry := map[string]any{
				"type":    string(cfg.GetType()),
				"command": cfg.Command,
			}

			if len(cfg.Args) > 0 {
				entry["args"] = cfg.Args
			}

			if len(cfg.Env) > 0 {
				entry["env"] = cfg.Env
			}

			result[name] = entry
		case *mcp.SSEServerConfig:
			entry := map[string]any{
				"type": string(cfg.Type),
				"url":  cfg.URL,
			}

			if len(cfg.Headers) > 0 {
				entry["headers"] = cfg.Headers
			}

			result[name] = entry
		case *mcp.HTTPServerConfig:
			entry := map[string]any{
				"type": string(cfg.Type),
				"url":  cfg.URL,
			}

			if len(cfg.Headers) > 0 {
				entry["headers"] = cfg.Headers
			}

			result[name] = entry
		case *mcp.SdkServerConfig:
			entry := map[string]any{
				"type": string(cfg.Type),
			}

			if instance, ok := cfg.Instance.(mcp.ServerInstance); ok {
				entry["tools"] = instance.ListTools()
			}

			result[name] = entry
		}
	}

	return result
}

func extractOutputSchema(outputFormat map[string]any) map[string]any {
	if outputFormat == nil {
		return nil
	}

	formatType, _ := outputFormat["type"].(string)
	if formatType == "json_schema" {
		if schema, ok := outputFormat["schema"].(map[string]any); ok {
			return schema
		}

		return nil
	}

	if _, ok := outputFormat["properties"]; ok {
		return outputFormat
	}

	return nil
}

func mapPermissionToSandbox(permMode string) string {
	switch permMode {
	case "acceptEdits":
		return "workspace-write"
	case "bypassPermissions", "acceptAll":
		return "danger-full-access"
	default:
		return ""
	}
}

func mapPermissionToApprovalPolicy(permMode string) string {
	switch permMode {
	case "bypassPermissions", "acceptAll":
		return "never"
	case "askAll":
		return "untrusted"
	case "default", "acceptEdits", "plan", "":
		return "on-request"
	default:
		return ""
	}
}

// getInitializeTimeout returns the initialize timeout.
func (s *Session) getInitializeTimeout() time.Duration {
	if s.options != nil && s.options.InitializeTimeout != nil {
		return *s.options.InitializeTimeout
	}

	if timeoutStr := os.Getenv("CODEX_INITIALIZE_TIMEOUT"); timeoutStr != "" {
		if timeoutSec, err := strconv.Atoi(timeoutStr); err == nil && timeoutSec > 0 {
			return time.Duration(timeoutSec) * time.Second
		}
	}

	return defaultInitializeTimeout
}

// NeedsInitialization returns true if the session has callbacks that require initialization.
func (s *Session) NeedsInitialization() bool {
	if s.options == nil {
		return false
	}

	return s.options.CanUseTool != nil ||
		s.options.OnUserInput != nil ||
		len(s.sdkMcpServers) > 0 ||
		len(s.sdkDynamicTools) > 0
}

// GetInitializationResult returns a copy of the server initialization info.
func (s *Session) GetInitializationResult() map[string]any {
	s.initMu.RLock()
	defer s.initMu.RUnlock()

	if s.initializationResult == nil {
		return nil
	}

	return maps.Clone(s.initializationResult)
}

// GetSDKMCPServerNames returns the names of all registered SDK MCP servers.
func (s *Session) GetSDKMCPServerNames() []string {
	names := make([]string, 0, len(s.sdkMcpServers))
	for name := range s.sdkMcpServers {
		names = append(names, name)
	}

	return names
}

// HandleDynamicToolCall handles item/tool/call requests from the CLI for
// SDK-registered dynamic tools and MCP server tools.
func (s *Session) HandleDynamicToolCall(
	ctx context.Context,
	req *ControlRequest,
) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	toolFullName, _ := req.Request["tool"].(string)
	arguments, _ := req.Request["arguments"].(map[string]any)

	// Try plain name lookup in dynamic tools first.
	if tool, ok := s.sdkDynamicTools[toolFullName]; ok {
		updated, denied, err := s.authorizeToolCall(ctx, toolFullName, arguments)
		if err != nil {
			return nil, err
		}
		if denied != nil {
			return deniedToolResponse(denied.Message), nil
		}
		return s.executeDynamicTool(ctx, tool, updated)
	}

	// Fall back to MCP server lookup for mcp__<server>__<tool> names.
	binding, bound := s.sdkMcpToolBindings[toolFullName]
	serverName, toolName := binding.server, binding.tool
	var err error
	if !bound {
		serverName, toolName, err = parseMCPToolName(toolFullName)
	}
	if err != nil {
		//nolint:nilerr // Error is encoded in the protocol response
		return map[string]any{
			"success": false,
			"contentItems": []map[string]any{{
				"type": "inputText",
				"text": fmt.Sprintf("unknown tool: %s", toolFullName),
			}},
		}, nil
	}

	server, exists := s.sdkMcpServers[serverName]
	if !exists {
		return map[string]any{
			"success": false,
			"contentItems": []map[string]any{{
				"type": "inputText",
				"text": fmt.Sprintf("SDK MCP server not found: %s", serverName),
			}},
		}, nil
	}

	arguments, denied, err := s.authorizeToolCall(ctx, "mcp__"+serverName+"__"+toolName, arguments)
	if err != nil {
		return nil, err
	}
	if denied != nil {
		return deniedToolResponse(denied.Message), nil
	}
	result, callErr := server.CallTool(ctx, toolName, arguments)
	if callErr != nil {
		//nolint:nilerr // Error is encoded in the protocol response
		return map[string]any{
			"success": false,
			"contentItems": []map[string]any{{
				"type": "inputText",
				"text": callErr.Error(),
			}},
		}, nil
	}

	isError, _ := result["is_error"].(bool)

	contentItems := convertMCPContentToItems(result)

	return map[string]any{
		"success":      !isError,
		"contentItems": contentItems,
	}, nil
}

// authorizeToolCall applies the same policy to plain dynamic tools and MCP tools.
func (s *Session) authorizeToolCall(ctx context.Context, name string, input map[string]any) (map[string]any, *permission.ResultDeny, error) {
	if s.options == nil || s.options.CanUseTool == nil {
		return input, nil, nil
	}
	decision, err := s.options.CanUseTool(ctx, name, input, &permission.Context{})
	if err != nil {
		return nil, nil, err
	}
	switch d := decision.(type) {
	case *permission.ResultAllow:
		if d != nil {
			if d.UpdatedInput != nil {
				input = d.UpdatedInput
			}
			return input, nil, nil
		}
	case *permission.ResultDeny:
		if d != nil {
			return nil, d, nil
		}
	}
	return nil, nil, fmt.Errorf("tool permission callback returned invalid result %T", decision)
}

func deniedToolResponse(message string) map[string]any {
	return map[string]any{
		"success":      false,
		"contentItems": []map[string]any{{"type": "inputText", "text": message}},
	}
}

// executeDynamicTool calls a dynamic tool handler and formats the result
// as the protocol response.
func (s *Session) executeDynamicTool(
	ctx context.Context,
	tool *config.DynamicTool,
	arguments map[string]any,
) (map[string]any, error) {
	result, err := tool.Handler(ctx, arguments)
	if err != nil {
		//nolint:nilerr // Error is encoded in the protocol response
		return map[string]any{
			"success": false,
			"contentItems": []map[string]any{{
				"type": "inputText",
				"text": err.Error(),
			}},
		}, nil
	}

	data, err := json.Marshal(result)
	if err != nil {
		return map[string]any{
			"success": false,
			"contentItems": []map[string]any{{
				"type": "inputText",
				"text": fmt.Sprintf("failed to marshal tool result: %v", err),
			}},
		}, nil
	}

	return map[string]any{
		"success": true,
		"contentItems": []map[string]any{{
			"type": "inputText",
			"text": string(data),
		}},
	}, nil
}

// parseMCPToolName splits "mcp__<server>__<tool>" into server and tool names.
func parseMCPToolName(fullName string) (serverName, toolName string, err error) {
	const prefix = "mcp__"
	if len(fullName) <= len(prefix) || fullName[:len(prefix)] != prefix {
		return "", "", fmt.Errorf("invalid MCP tool name format: %s", fullName)
	}

	rest := fullName[len(prefix):]
	idx := 0

	for i := 0; i+1 < len(rest); i++ {
		if rest[i] == '_' && rest[i+1] == '_' {
			idx = i

			break
		}
	}

	if idx == 0 {
		return "", "", fmt.Errorf("invalid MCP tool name format (missing server/tool separator): %s", fullName)
	}

	return rest[:idx], rest[idx+2:], nil
}

// convertMCPContentToItems converts MCP result content entries to the
// DynamicToolCallResponse contentItems format.
func convertMCPContentToItems(result map[string]any) []map[string]any {
	content, ok := result["content"].([]map[string]any)
	if !ok {
		// Try []any (common from JSON unmarshalling).
		if contentAny, ok := result["content"].([]any); ok {
			items := make([]map[string]any, 0, len(contentAny))
			for _, entry := range contentAny {
				if entryMap, ok := entry.(map[string]any); ok {
					text, _ := entryMap["text"].(string)
					items = append(items, map[string]any{
						"type": "inputText",
						"text": text,
					})
				}
			}

			return items
		}

		return []map[string]any{}
	}

	items := make([]map[string]any, 0, len(content))
	for _, entry := range content {
		text, _ := entry["text"].(string)
		items = append(items, map[string]any{
			"type": "inputText",
			"text": text,
		})
	}

	return items
}

// HandleRequestUserInput handles item/tool/requestUserInput requests from the CLI.
// It parses the request into typed userinput types, invokes the OnUserInput callback,
// and serializes the response back to the wire format.
func (s *Session) HandleRequestUserInput(
	ctx context.Context,
	req *ControlRequest,
) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if s.options == nil || s.options.OnUserInput == nil {
		return map[string]any{"answers": map[string]any{}}, nil
	}

	parsed, err := parseUserInputRequest(req)
	if err != nil {
		return nil, fmt.Errorf("parse user input request: %w", err)
	}

	resp, err := s.options.OnUserInput(ctx, parsed)
	if err != nil {
		return nil, err
	}

	return serializeUserInputResponse(resp), nil
}

// parseUserInputRequest extracts typed userinput.Request from the wire format.
func parseUserInputRequest(req *ControlRequest) (*userinput.Request, error) {
	result := &userinput.Request{}
	result.ItemID, _ = req.Request["item_id"].(string)
	result.ThreadID, _ = req.Request["thread_id"].(string)
	result.TurnID, _ = req.Request["turn_id"].(string)

	questionsRaw, _ := req.Request["questions"].([]any)
	if len(questionsRaw) == 0 {
		return result, nil
	}

	result.Questions = make([]userinput.Question, 0, len(questionsRaw))

	for _, qRaw := range questionsRaw {
		qMap, ok := qRaw.(map[string]any)
		if !ok {
			continue
		}

		q := userinput.Question{}
		q.ID, _ = qMap["id"].(string)
		q.Header, _ = qMap["header"].(string)
		q.Question, _ = qMap["question"].(string)
		q.IsOther, _ = qMap["is_other"].(bool)
		q.IsSecret, _ = qMap["is_secret"].(bool)

		if optionsRaw, ok := qMap["options"].([]any); ok {
			q.Options = make([]userinput.QuestionOption, 0, len(optionsRaw))

			for _, oRaw := range optionsRaw {
				oMap, ok := oRaw.(map[string]any)
				if !ok {
					continue
				}

				opt := userinput.QuestionOption{}
				opt.Label, _ = oMap["label"].(string)
				opt.Description, _ = oMap["description"].(string)

				q.Options = append(q.Options, opt)
			}
		}

		result.Questions = append(result.Questions, q)
	}

	return result, nil
}

// serializeUserInputResponse converts a typed Response into the wire format.
func serializeUserInputResponse(resp *userinput.Response) map[string]any {
	if resp == nil || len(resp.Answers) == 0 {
		return map[string]any{"answers": map[string]any{}}
	}

	answers := make(map[string]any, len(resp.Answers))

	for qID, answer := range resp.Answers {
		if answer == nil {
			continue
		}

		answers[qID] = map[string]any{
			"answers": answer.Answers,
		}
	}

	return map[string]any{"answers": answers}
}

// HandleCanUseTool is called by CLI before tool use.
func (s *Session) HandleCanUseTool(
	ctx context.Context,
	req *ControlRequest,
) (map[string]any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	toolName, _ := req.Request["tool_name"].(string)
	input, _ := req.Request["input"].(map[string]any)

	// Compatibility path for newer app-server approval requests.
	if toolName == "" {
		if command, ok := req.Request["command"].(string); ok && command != "" {
			toolName = "Bash"

			if input == nil {
				input = map[string]any{
					"command": command,
				}
				if cwd, ok := req.Request["cwd"].(string); ok && cwd != "" {
					input["cwd"] = cwd
				}
			}
		}
	}

	if s.options == nil || s.options.CanUseTool == nil {
		return map[string]any{
			"decision": "accept",
		}, nil
	}

	var suggestions []*permission.Update
	if suggestionsData, ok := req.Request["suggestions"].([]any); ok {
		suggestions = make([]*permission.Update, 0, len(suggestionsData))

		for _, sg := range suggestionsData {
			if suggestionMap, ok := sg.(map[string]any); ok {
				update := &permission.Update{}
				if t, ok := suggestionMap["type"].(string); ok {
					update.Type = permission.UpdateType(t)
				}

				suggestions = append(suggestions, update)
			}
		}
	}

	permCtx := &permission.Context{
		Suggestions: suggestions,
	}

	decision, err := s.options.CanUseTool(ctx, toolName, input, permCtx)
	if err != nil {
		return nil, err
	}

	switch decision.(type) {
	case *permission.ResultAllow:
		return map[string]any{
			"decision": "accept",
		}, nil

	case *permission.ResultDeny:
		return map[string]any{
			"decision": "decline",
		}, nil

	default:
		return nil, fmt.Errorf(
			"tool permission callback must return *ResultAllow or *ResultDeny, got %T",
			decision,
		)
	}
}
