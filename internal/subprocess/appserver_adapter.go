package subprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"strings"
	"sync"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
	sdkerrors "github.com/mafilus/codex-agent-sdk-go/internal/errors"
)

// appServerRPC defines the JSON-RPC operations that AppServerAdapter
// needs from the inner transport. AppServerTransport implements this,
// and tests can provide a mock.
type appServerRPC interface {
	Start(ctx context.Context) error
	Close() error
	IsReady() bool
	SendRequest(ctx context.Context, method string, params any) (*RPCResponse, error)
	SendResponse(id int64, result json.RawMessage, rpcErr *RPCError) error
	Notifications() <-chan *RPCNotification
	Requests() <-chan *RPCIncomingRequest
}

// AppServerAdapter bridges the control_request/control_response protocol
// used by Controller/Session to the JSON-RPC 2.0 protocol spoken by
// codex app-server. It implements config.Transport.
type AppServerAdapter struct {
	log   *slog.Logger
	inner appServerRPC

	messages chan map[string]any
	errs     chan error
	done     chan struct{}

	mu       sync.Mutex
	threadID string
	turnID   string

	modelOverride             *string
	approvalPolicyOverride    *string
	sandboxPolicyOverride     map[string]any
	effortOverride            *string
	outputSchemaOverride      any
	collaborationModeOverride map[string]any

	// lastTokenUsage caches the most recent token usage data from
	// thread/tokenUsage/updated, injected into turn/completed if no
	// inline usage is present.
	lastTokenUsage map[string]any

	// lastAssistantText caches the latest completed assistant text for a turn.
	// Used as a fallback result payload when turn/completed does not include one.
	lastAssistantText string

	// includePartialMessages controls whether streaming deltas are emitted
	// as stream_event messages. When false, delta notifications are suppressed.
	includePartialMessages bool

	// pendingRPCRequests maps synthetic request_id strings to JSON-RPC IDs
	// for server-to-client requests (hooks/MCP).
	pendingRPCRequests map[string]int64

	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// Compile-time verification that AppServerAdapter implements Transport.
var _ config.Transport = (*AppServerAdapter)(nil)

// NewAppServerAdapter creates a new adapter that wraps an AppServerTransport.
func NewAppServerAdapter(
	log *slog.Logger,
	opts *config.Options,
) *AppServerAdapter {
	return &AppServerAdapter{
		log:                    log.With(slog.String("component", "appserver_adapter")),
		inner:                  NewAppServerTransport(log, opts),
		messages:               make(chan map[string]any, 128),
		errs:                   make(chan error, 4),
		done:                   make(chan struct{}),
		includePartialMessages: opts.IncludePartialMessages,
		pendingRPCRequests:     make(map[string]int64, 8),
	}
}

// Start initializes the inner transport (JSON-RPC handshake) and starts
// the adapter read loop that translates notifications into exec-event format.
func (a *AppServerAdapter) Start(ctx context.Context) error {
	if err := a.inner.Start(ctx); err != nil {
		return err
	}

	a.wg.Add(1)

	go a.readLoop()

	return nil
}

// ReadMessages returns channels populated by the adapter read loop with
// messages in exec-event format that message.Parse() understands.
func (a *AppServerAdapter) ReadMessages(
	_ context.Context,
) (<-chan map[string]any, <-chan error) {
	return a.messages, a.errs
}

// SendMessage intercepts outgoing control protocol messages and translates
// them into JSON-RPC calls on the inner transport.
func (a *AppServerAdapter) SendMessage(ctx context.Context, data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal outgoing message: %w", err)
	}

	msgType, _ := raw["type"].(string)

	switch msgType {
	case "control_request":
		return a.handleControlRequest(ctx, raw)
	case "control_response":
		return a.handleControlResponse(raw)
	case "user":
		return a.handleUserMessage(ctx, raw)
	default:
		a.log.DebugContext(ctx, "passing through unknown message type",
			slog.String("type", msgType),
		)

		return nil
	}
}

// Close shuts down the adapter and inner transport.
func (a *AppServerAdapter) Close() error {
	a.closeOnce.Do(func() {
		close(a.done)
		a.closeErr = a.inner.Close()
		a.wg.Wait()
	})
	return a.closeErr
}

// IsReady delegates to the inner transport.
func (a *AppServerAdapter) IsReady() bool {
	return a.inner.IsReady()
}

// EndInput is a no-op for app-server sessions which stay alive for
// multi-turn interaction.
func (a *AppServerAdapter) EndInput() error {
	return nil
}

// handleControlRequest translates outgoing control_request messages to
// JSON-RPC calls.
func (a *AppServerAdapter) handleControlRequest(
	ctx context.Context,
	raw map[string]any,
) error {
	requestData, _ := raw["request"].(map[string]any)
	if requestData == nil {
		return fmt.Errorf("control_request missing 'request' field")
	}

	subtype, _ := requestData["subtype"].(string)
	requestID, _ := raw["request_id"].(string)

	switch subtype {
	case "initialize":
		return a.handleInitialize(ctx, requestID, requestData)
	case "interrupt":
		return a.handleInterrupt(ctx, requestID)
	case "set_permission_mode":
		return a.handleSetPermissionMode(requestID, requestData)
	case "set_model":
		return a.handleSetModel(requestID, requestData)
	case "mcp_status":
		return a.handleMCPStatus(ctx, requestID)
	case "list_models":
		return a.handleListModels(ctx, requestID)
	case "rewind_files":
		return a.handleRewindFiles(requestID)
	default:
		a.log.DebugContext(ctx, "unsupported control_request subtype",
			slog.String("subtype", subtype),
		)

		a.injectErrorControlResponse(
			requestID,
			fmt.Sprintf("%s: %s", sdkerrors.ErrUnsupportedControlRequest, subtype),
		)

		return nil
	}
}

// handleInitialize translates an "initialize" control_request into a
// thread/start JSON-RPC call and fabricates a control_response.
func (a *AppServerAdapter) handleInitialize(
	ctx context.Context,
	requestID string,
	requestData map[string]any,
) error {
	method, params, turnOverrides, err := buildInitializeRPC(requestData)
	if err != nil {
		a.injectErrorControlResponse(requestID, err.Error())

		return nil
	}

	resp, err := a.inner.SendRequest(ctx, method, params)
	if err != nil {
		a.injectErrorControlResponse(requestID, fmt.Sprintf("%s RPC: %v", method, err))

		return nil
	}

	responsePayload := map[string]any{}

	if resp.Result != nil {
		var result map[string]any
		if unmarshalErr := json.Unmarshal(resp.Result, &result); unmarshalErr == nil {
			a.log.DebugContext(ctx, "initialize rpc response",
				slog.String("method", method),
				slog.Any("result", result),
			)

			responsePayload = result

			if tid := extractThreadID(result); tid != "" {
				a.mu.Lock()
				a.threadID = tid
				a.mu.Unlock()
			}
		}
	}

	// If collaboration mode is set but missing a model, backfill from
	// the thread/start response so the CLI doesn't reject turn/start.
	if cm := turnOverrides.collaborationMode; cm != nil {
		if settings, ok := cm["settings"].(map[string]any); ok {
			if _, hasModel := settings["model"]; !hasModel {
				if respModel, ok := responsePayload["model"].(string); ok && respModel != "" {
					settings["model"] = respModel
				}
			}
		}
	}

	a.mu.Lock()
	a.effortOverride = turnOverrides.effort
	a.outputSchemaOverride = cloneAnyValue(turnOverrides.outputSchema)
	a.collaborationModeOverride = cloneAnyMap(turnOverrides.collaborationMode)
	a.mu.Unlock()

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   responsePayload,
	})

	return nil
}

type initializeTurnOverrides struct {
	collaborationMode map[string]any
	effort            *string
	outputSchema      any
}

//nolint:gocyclo // initialization normalization intentionally handles many option variants.
func buildInitializeRPC(
	requestData map[string]any,
) (string, map[string]any, initializeTurnOverrides, error) {
	resumeID, _ := requestData["resume"].(string)
	forkSession, _ := requestData["forkSession"].(bool)
	continueConversation, _ := requestData["continueConversation"].(bool)

	turnOverrides := initializeTurnOverrides{}

	params := make(map[string]any, 24)
	if model, ok := requestData["model"].(string); ok && model != "" {
		params["model"] = model
	}

	if cwd, ok := requestData["cwd"].(string); ok && cwd != "" {
		params["cwd"] = cwd
	}

	if configMap, ok := requestData["config"].(map[string]any); ok && len(configMap) > 0 {
		params["config"] = configMap
	}

	if approvalRaw, ok := requestData["approvalPolicy"].(string); ok && approvalRaw != "" {
		if approvalPolicy, err := normalizeApprovalPolicy(approvalRaw); err == nil {
			params["approvalPolicy"] = approvalPolicy
		}
	}

	if sandboxRaw, ok := requestData["sandbox"].(string); ok && sandboxRaw != "" {
		if sandboxMode, err := normalizeSandboxMode(sandboxRaw); err == nil {
			params["sandbox"] = sandboxMode
		}
	}

	if effortRaw, ok := requestData["reasoningEffort"].(string); ok && effortRaw != "" {
		effort, err := normalizeEffort(effortRaw)
		if err != nil {
			return "", nil, initializeTurnOverrides{}, err
		}

		turnOverrides.effort = &effort
	}

	if permMode, ok := requestData["permissionMode"].(string); ok && permMode == permissionModePlan {
		model, _ := requestData["model"].(string)
		turnOverrides.collaborationMode = buildCollaborationMode(permissionModePlan, model)
	}

	if developerInstructions, ok := requestData["systemPrompt"].(string); ok && developerInstructions != "" {
		params["developerInstructions"] = developerInstructions
	}

	// Claude-style systemPromptPreset is emulated by forwarding its append text
	// as developer instructions when no explicit system prompt is provided.
	if _, hasSystemPrompt := params["developerInstructions"]; !hasSystemPrompt {
		if preset, ok := requestData["systemPromptPreset"].(map[string]any); ok {
			if appendText, ok := preset["append"].(string); ok && appendText != "" {
				params["developerInstructions"] = appendText
			}
		}
	}

	// Pass through compatibility initialize fields. Codex app-server ignores
	// unknown fields and may use some of these in newer versions.
	passthroughKeys := []string{
		"allowedTools",
		"disallowedTools",
		"tools",
		"addDirs",
		"mcpServers",
		"dynamicTools",
		"permissionPromptToolName",
	}
	for _, key := range passthroughKeys {
		value, ok := requestData[key]
		if !ok || value == nil {
			continue
		}

		params[key] = cloneAnyValue(value)
	}

	if outputSchema, ok := requestData["outputSchema"]; ok {
		normalizedOutputSchema, err := normalizeOutputSchema(outputSchema)
		if err != nil {
			return "", nil, initializeTurnOverrides{}, err
		}

		turnOverrides.outputSchema = normalizedOutputSchema
	}

	if permissionPromptToolName, ok := requestData["permissionPromptToolName"].(string); ok &&
		permissionPromptToolName != "" && permissionPromptToolName != "stdio" {
		return "", nil, initializeTurnOverrides{}, fmt.Errorf(
			"%w: permissionPromptToolName %q is unsupported by codex app-server",
			sdkerrors.ErrUnsupportedOption,
			permissionPromptToolName,
		)
	}

	if continueConversation && resumeID == "" {
		return "", nil, initializeTurnOverrides{}, fmt.Errorf(
			"%w: continueConversation requires resume in app-server mode",
			sdkerrors.ErrUnsupportedOption,
		)
	}

	if resumeID != "" {
		params["threadId"] = resumeID
		if forkSession {
			return "thread/fork", params, turnOverrides, nil
		}

		return "thread/resume", params, turnOverrides, nil
	}

	return "thread/start", params, turnOverrides, nil
}

// handleInterrupt translates an "interrupt" control_request into a
// turn/interrupt JSON-RPC call.
func (a *AppServerAdapter) handleInterrupt(
	ctx context.Context,
	requestID string,
) error {
	a.mu.Lock()
	turnID := a.turnID
	threadID := a.threadID
	a.mu.Unlock()

	params := map[string]any{}
	if threadID != "" {
		params["threadId"] = threadID
	}

	if turnID != "" {
		params["turnId"] = turnID
	}

	_, err := a.inner.SendRequest(ctx, "turn/interrupt", params)
	if err != nil {
		a.log.WarnContext(ctx, "turn/interrupt failed",
			slog.String("error", err.Error()),
		)
	}

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   map[string]any{},
	})

	return nil
}

func (a *AppServerAdapter) handleSetPermissionMode(
	requestID string,
	requestData map[string]any,
) error {
	mode, _ := requestData["mode"].(string)

	approvalPolicy, sandboxPolicy, err := permissionModeToTurnOverrides(mode)
	if err != nil {
		a.injectErrorControlResponse(requestID, err.Error())

		return nil
	}

	a.mu.Lock()
	if approvalPolicy == "" {
		a.approvalPolicyOverride = nil
	} else {
		ap := approvalPolicy
		a.approvalPolicyOverride = &ap
	}

	if sandboxPolicy == nil {
		a.sandboxPolicyOverride = nil
	} else {
		cloned := make(map[string]any, len(sandboxPolicy))
		maps.Copy(cloned, sandboxPolicy)

		a.sandboxPolicyOverride = cloned
	}

	if mode == permissionModePlan {
		var model string
		if a.modelOverride != nil {
			model = *a.modelOverride
		}

		a.collaborationModeOverride = buildCollaborationMode(permissionModePlan, model)
	} else {
		a.collaborationModeOverride = nil
	}
	a.mu.Unlock()

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   map[string]any{},
	})

	return nil
}

func (a *AppServerAdapter) handleSetModel(
	requestID string,
	requestData map[string]any,
) error {
	var modelPtr *string

	switch v := requestData["model"].(type) {
	case nil:
		modelPtr = nil
	case string:
		model := v
		modelPtr = &model
	default:
		a.injectErrorControlResponse(
			requestID,
			fmt.Errorf("%w: model must be a string or null", sdkerrors.ErrUnsupportedOption).Error(),
		)

		return nil
	}

	a.mu.Lock()
	a.modelOverride = modelPtr
	a.mu.Unlock()

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   map[string]any{},
	})

	return nil
}

func (a *AppServerAdapter) handleMCPStatus(ctx context.Context, requestID string) error {
	resp, err := a.inner.SendRequest(ctx, "mcpServerStatus/list", map[string]any{})
	if err != nil {
		a.injectErrorControlResponse(requestID, fmt.Sprintf("mcpServerStatus/list RPC: %v", err))

		return nil
	}

	payload := map[string]any{
		"mcpServers": []map[string]any{},
	}

	if resp.Result != nil {
		var result map[string]any
		if unmarshalErr := json.Unmarshal(resp.Result, &result); unmarshalErr == nil {
			if data, ok := result["data"].([]any); ok && len(data) > 0 {
				servers := make([]map[string]any, 0, len(data))
				for _, raw := range data {
					entry, ok := raw.(map[string]any)
					if !ok {
						continue
					}

					name, _ := entry["name"].(string)
					authStatus, _ := entry["authStatus"].(string)

					if name == "" {
						continue
					}

					servers = append(servers, map[string]any{
						"name":   name,
						"status": mapMCPAuthStatus(authStatus),
					})
				}

				payload["mcpServers"] = servers
			}
		}
	}

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   payload,
	})

	return nil
}

func (a *AppServerAdapter) handleListModels(ctx context.Context, requestID string) error {
	resp, err := a.inner.SendRequest(ctx, "model/list", map[string]any{})
	if err != nil {
		a.injectErrorControlResponse(requestID, fmt.Sprintf("model/list RPC: %v", err))

		return nil
	}

	payload := map[string]any{
		"models": []map[string]any{},
	}

	if resp.Result != nil {
		var result map[string]any
		if unmarshalErr := json.Unmarshal(resp.Result, &result); unmarshalErr == nil {
			if data, ok := result["data"].([]any); ok && len(data) > 0 {
				models := make([]map[string]any, 0, len(data))
				for _, raw := range data {
					entry, ok := raw.(map[string]any)
					if !ok {
						continue
					}

					id, _ := entry["id"].(string)
					if id == "" {
						continue
					}

					m := map[string]any{
						"id":                  id,
						"supportsPersonality": false,
					}

					if v, ok := entry["model"].(string); ok {
						m["model"] = v
					}

					if v, ok := entry["displayName"].(string); ok {
						m["displayName"] = v
					}

					if v, ok := entry["description"].(string); ok {
						m["description"] = v
					}

					if v, ok := entry["isDefault"].(bool); ok {
						m["isDefault"] = v
					}

					if v, ok := entry["hidden"].(bool); ok {
						m["hidden"] = v
					}

					if v, ok := entry["defaultReasoningEffort"].(string); ok {
						m["defaultReasoningEffort"] = v
					}

					if v, ok := entry["supportedReasoningEfforts"].([]any); ok {
						m["supportedReasoningEfforts"] = v
					}

					if v, ok := entry["inputModalities"].([]any); ok {
						m["inputModalities"] = v
					}

					if v, ok := entry["supportsPersonality"].(bool); ok {
						m["supportsPersonality"] = v
					}

					models = append(models, m)
				}

				payload["models"] = models
			}
		}
	}

	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "success",
		"request_id": requestID,
		"response":   payload,
	})

	return nil
}

func (a *AppServerAdapter) handleRewindFiles(requestID string) error {
	a.injectErrorControlResponse(
		requestID,
		fmt.Errorf(
			"%w: rewind_files is not supported by codex app-server",
			sdkerrors.ErrUnsupportedControlRequest,
		).Error(),
	)

	return nil
}

func mapMCPAuthStatus(authStatus string) string {
	switch authStatus {
	case "oauth", "bearerToken":
		return "connected"
	case "notLoggedIn":
		return "not_logged_in"
	case "unsupported":
		return "unsupported"
	default:
		if authStatus == "" {
			return "unknown"
		}

		return authStatus
	}
}

// handleControlResponse translates an outgoing control_response (from
// Controller to server) back into a JSON-RPC response for the inner
// transport. This handles hook/MCP callback responses.
func (a *AppServerAdapter) handleControlResponse(raw map[string]any) error {
	responseData, _ := raw["response"].(map[string]any)
	if responseData == nil {
		return nil
	}

	requestID, _ := responseData["request_id"].(string)
	if requestID == "" {
		return nil
	}

	a.mu.Lock()
	rpcID, ok := a.pendingRPCRequests[requestID]

	if ok {
		delete(a.pendingRPCRequests, requestID)
	}

	a.mu.Unlock()

	if !ok {
		return nil
	}

	payload, _ := responseData["response"].(map[string]any)

	var result json.RawMessage

	var rpcErr *RPCError

	subtype, _ := responseData["subtype"].(string)
	if subtype == "error" {
		errMsg, _ := responseData["error"].(string)
		rpcErr = &RPCError{Code: -32603, Message: errMsg}
	} else if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal response payload: %w", err)
		}

		result = data
	} else {
		result = json.RawMessage(`{}`)
	}

	return a.inner.SendResponse(rpcID, result, rpcErr)
}

// handleUserMessage translates an outgoing user message into a turn/start
// JSON-RPC call.
func (a *AppServerAdapter) handleUserMessage(
	ctx context.Context,
	raw map[string]any,
) error {
	messageData, _ := raw["message"].(map[string]any)

	// Build input as an array of content blocks — the app-server expects
	// a sequence, not a plain string.
	var input any

	if messageData != nil {
		if content, ok := messageData["content"].(string); ok {
			input = []map[string]any{
				{"type": "text", "text": content},
			}
		} else if contentBlocks, ok := messageData["content"].([]any); ok {
			input = contentBlocks
		}
	}

	a.mu.Lock()
	threadID := a.threadID
	modelOverride := a.modelOverride
	approvalPolicyOverride := a.approvalPolicyOverride
	sandboxPolicyOverride := cloneAnyMap(a.sandboxPolicyOverride)
	effortOverride := a.effortOverride
	outputSchemaOverride := cloneAnyValue(a.outputSchemaOverride)
	collaborationModeOverride := cloneAnyMap(a.collaborationModeOverride)
	a.mu.Unlock()

	params := map[string]any{
		"input": input,
	}

	if threadID != "" {
		params["threadId"] = threadID
	}

	if modelOverride != nil {
		params["model"] = *modelOverride
	}

	if approvalPolicyOverride != nil {
		params["approvalPolicy"] = *approvalPolicyOverride
	}

	if sandboxPolicyOverride != nil {
		params["sandboxPolicy"] = sandboxPolicyOverride
	}

	if effortOverride != nil {
		params["effort"] = *effortOverride
	}

	if outputSchemaOverride != nil {
		params["outputSchema"] = outputSchemaOverride
	}

	if collaborationModeOverride != nil {
		// Ensure the collaboration mode settings include a model — the CLI
		// requires it. Fall back to the model from params or the override.
		if settings, ok := collaborationModeOverride["settings"].(map[string]any); ok {
			if _, hasModel := settings["model"]; !hasModel {
				if m, ok := params["model"].(string); ok && m != "" {
					settings["model"] = m
				}
			}
		}

		params["collaborationMode"] = collaborationModeOverride
	}

	resp, err := a.inner.SendRequest(ctx, "turn/start", params)
	if err != nil {
		return fmt.Errorf("turn/start RPC: %w", err)
	}

	if resp.Result != nil {
		var result map[string]any
		if unmarshalErr := json.Unmarshal(resp.Result, &result); unmarshalErr == nil {
			if tid, ok := result["turnId"].(string); ok && tid != "" {
				a.mu.Lock()
				a.turnID = tid
				a.mu.Unlock()
			} else if turnObj, ok := result["turn"].(map[string]any); ok {
				if tid, ok := turnObj["id"].(string); ok && tid != "" {
					a.mu.Lock()
					a.turnID = tid
					a.mu.Unlock()
				}
			}
		}
	}

	return nil
}

// readLoop reads notifications and requests from the inner transport and
// translates them into exec-event format messages.
func (a *AppServerAdapter) readLoop() {
	defer a.wg.Done()
	defer close(a.messages)
	defer close(a.errs)

	notifications := a.inner.Notifications()
	requests := a.inner.Requests()

	for {
		select {
		case notif, ok := <-notifications:
			if !ok {
				notifications = nil

				if requests == nil {
					return
				}

				continue
			}

			a.handleNotification(notif)

		case req, ok := <-requests:
			if !ok {
				requests = nil

				if notifications == nil {
					return
				}

				continue
			}

			a.handleServerRequest(req)

		case <-a.done:
			return
		}
	}
}

// handleNotification translates a JSON-RPC notification into exec-event
// format and sends it to the messages channel.
func (a *AppServerAdapter) handleNotification(notif *RPCNotification) {
	event := a.translateNotification(notif)
	if event == nil {
		return
	}

	select {
	case a.messages <- event:
	case <-a.done:
	}
}

// handleServerRequest translates an incoming JSON-RPC request from the
// server into a control_request message for the Controller to handle.
func (a *AppServerAdapter) handleServerRequest(req *RPCIncomingRequest) {
	syntheticID := fmt.Sprintf("rpc_%d", req.ID)

	a.mu.Lock()
	a.pendingRPCRequests[syntheticID] = req.ID
	a.mu.Unlock()

	var requestPayload map[string]any
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &requestPayload); err != nil {
			requestPayload = make(map[string]any, 1)
		}
	} else {
		requestPayload = make(map[string]any, 1)
	}

	requestPayload["subtype"] = methodToSubtype(req.Method)

	msg := map[string]any{
		"type":       "control_request",
		"request_id": syntheticID,
		"request":    requestPayload,
	}

	select {
	case a.messages <- msg:
	case <-a.done:
	}
}

// translateNotification converts a JSON-RPC notification into an exec-event
// format map that message.Parse() can handle. Every notification produces an
// event; nothing is silently dropped.
func (a *AppServerAdapter) translateNotification(
	notif *RPCNotification,
) map[string]any {
	var params map[string]any
	if notif.Params != nil {
		if err := json.Unmarshal(notif.Params, &params); err != nil {
			a.log.Warn("failed to unmarshal notification params",
				slog.String("method", notif.Method),
				slog.String("error", err.Error()),
			)

			params = make(map[string]any, 1)
		}
	} else {
		params = make(map[string]any, 1)
	}

	switch notif.Method {
	case "thread/started":
		event := map[string]any{"type": "thread.started"}
		if tid := extractThreadID(params); tid != "" {
			event["thread_id"] = tid
		}

		a.mu.Lock()
		if tid, _ := event["thread_id"].(string); tid != "" {
			a.threadID = tid
		} else if a.threadID != "" {
			event["thread_id"] = a.threadID
		}
		a.mu.Unlock()

		return event

	case "turn/started":
		if turnObj, ok := params["turn"].(map[string]any); ok {
			if tid, ok := turnObj["id"].(string); ok && tid != "" {
				a.mu.Lock()
				a.turnID = tid
				a.lastAssistantText = ""
				a.mu.Unlock()
			}
		}

		return map[string]any{"type": "turn.started"}

	case "item/started":
		return a.translateItemNotification("item.started", params)

	case "item/agentMessage/delta":
		if !a.includePartialMessages {
			return nil
		}

		delta, _ := params["delta"].(string)
		itemID, _ := params["itemId"].(string)

		a.mu.Lock()
		sessionID := a.threadID
		a.mu.Unlock()

		return map[string]any{
			"type":       "stream_event",
			"uuid":       itemID,
			"session_id": sessionID,
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": delta,
				},
			},
		}

	case "item/completed":
		return a.translateItemNotification("item.completed", params)

	case "turn/completed":
		return a.translateTurnCompleted(params)

	case "turn/failed":
		event := map[string]any{"type": "turn.failed"}

		if errMsg, ok := params["error"].(string); ok {
			event["error"] = map[string]any{"message": errMsg}
		} else if errObj, ok := params["error"].(map[string]any); ok {
			event["error"] = errObj
		}

		return event

	case "thread/tokenUsage/updated":
		return a.translateTokenUsageUpdated(params)

	case "account/rateLimits/updated":
		return map[string]any{
			"type":    "system",
			"subtype": "account.rate_limits.updated",
			"data":    params,
		}

	default:
		// Handle codex/event/* namespace.
		if strings.HasPrefix(notif.Method, "codex/event/") {
			return a.translateCodexEvent(notif.Method, params)
		}

		// Pass through all unknown notifications as system messages.
		a.log.Debug("passing through unknown notification",
			slog.String("method", notif.Method),
		)

		return map[string]any{
			"type":    "system",
			"subtype": notif.Method,
			"data":    params,
		}
	}
}

// translateItemNotification handles item/started and item/completed,
// dispatching userMessage items to the user message format and all
// other item types through the standard item translation.
func (a *AppServerAdapter) translateItemNotification(
	eventType string,
	params map[string]any,
) map[string]any {
	nested, _ := params["item"].(map[string]any)
	if nested == nil {
		nested = params
	}

	itemType, _ := nested["type"].(string)

	// userMessage items need special handling: item/started emits a
	// "user" message so the content is visible; item/completed emits a
	// system lifecycle event (no new content to show).
	if itemType == "userMessage" {
		if eventType == "item.started" {
			return a.translateUserMessageItem(nested)
		}

		return map[string]any{
			"type":    "system",
			"subtype": "user_message.completed",
			"data":    nested,
		}
	}

	item := a.extractAndTranslateItem(params)

	if eventType == "item.completed" {
		if itemType, ok := item["type"].(string); ok && itemType == "agent_message" {
			if text, ok := item["text"].(string); ok && strings.TrimSpace(text) != "" {
				a.mu.Lock()
				a.lastAssistantText = text
				a.mu.Unlock()
			}
		}
	}

	return map[string]any{
		"type": eventType,
		"item": item,
	}
}

// translateUserMessageItem converts a userMessage item into the "user"
// message format that parseUserMessage already handles.
func (a *AppServerAdapter) translateUserMessageItem(
	nested map[string]any,
) map[string]any {
	var text string

	if contentArr, ok := nested["content"].([]any); ok {
		parts := make([]string, 0, len(contentArr))

		for _, block := range contentArr {
			if blockMap, ok := block.(map[string]any); ok {
				if blockMap["type"] == "text" {
					if t, ok := blockMap["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
		}

		text = strings.Join(parts, "\n")
	}

	event := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": text,
		},
	}

	if id, ok := nested["id"].(string); ok {
		event["uuid"] = id
	}

	return event
}

// translateTurnCompleted builds a turn.completed event, injecting cached
// token usage when no inline usage is present.
func (a *AppServerAdapter) translateTurnCompleted(
	params map[string]any,
) map[string]any {
	event := map[string]any{"type": "turn.completed"}

	a.mu.Lock()
	if a.threadID != "" {
		event["session_id"] = a.threadID
		event["thread_id"] = a.threadID
	}
	a.mu.Unlock()

	if usage, ok := params["usage"].(map[string]any); ok {
		event["usage"] = usage
	} else {
		// No inline usage — try cached token usage from
		// thread/tokenUsage/updated.
		a.mu.Lock()
		cached := a.lastTokenUsage
		a.mu.Unlock()

		if cached != nil {
			event["usage"] = convertTokenUsage(cached)
		}
	}

	if v, ok := params["isError"]; ok {
		event["is_error"] = v
	}

	if v, ok := params["result"]; ok {
		event["result"] = v
	} else {
		a.mu.Lock()
		lastAssistantText := a.lastAssistantText
		a.mu.Unlock()

		if strings.TrimSpace(lastAssistantText) != "" {
			event["result"] = lastAssistantText
		}
	}

	return event
}

// translateTokenUsageUpdated caches the latest token usage and emits a
// system message.
func (a *AppServerAdapter) translateTokenUsageUpdated(
	params map[string]any,
) map[string]any {
	if tokenUsage, ok := params["tokenUsage"].(map[string]any); ok {
		a.mu.Lock()
		a.lastTokenUsage = tokenUsage
		a.mu.Unlock()
	}

	return map[string]any{
		"type":    "system",
		"subtype": "thread.token_usage.updated",
		"data":    params,
	}
}

// convertTokenUsage extracts the "last" usage object from cached token
// usage and converts camelCase keys to snake_case for the exec-event format.
func convertTokenUsage(tokenUsage map[string]any) map[string]any {
	last, ok := tokenUsage["last"].(map[string]any)
	if !ok {
		return nil
	}

	keyMap := map[string]string{ //nolint:gosec // G101 false positive: field name mapping, not credentials
		"totalTokens":           "total_tokens",
		"inputTokens":           "input_tokens",
		"outputTokens":          "output_tokens",
		"cachedInputTokens":     "cached_input_tokens",
		"reasoningOutputTokens": "reasoning_output_tokens",
	}

	result := make(map[string]any, len(last))

	for k, v := range last {
		if snakeKey, ok := keyMap[k]; ok {
			result[snakeKey] = v
		} else {
			result[k] = v
		}
	}

	return result
}

const permissionModePlan = "plan"

func permissionModeToTurnOverrides(mode string) (string, map[string]any, error) {
	const (
		approvalOnRequest = "on-request"
		approvalNever     = "never"
	)

	const approvalUntrusted = "untrusted"

	switch mode {
	case "", "default", permissionModePlan:
		return approvalOnRequest, nil, nil
	case "acceptEdits":
		return approvalOnRequest, map[string]any{"type": "workspaceWrite"}, nil
	case "bypassPermissions", "acceptAll":
		return approvalNever, map[string]any{"type": "dangerFullAccess"}, nil
	case "askAll":
		return approvalUntrusted, nil, nil
	default:
		return "", nil, fmt.Errorf("%w: permission mode %q", sdkerrors.ErrUnsupportedOption, mode)
	}
}

func normalizeApprovalPolicy(value string) (string, error) {
	switch value {
	case "on-request", "onRequest":
		return "on-request", nil
	case "on-failure", "onFailure":
		return "on-failure", nil
	case "untrusted", "unlessTrusted":
		return "untrusted", nil
	case "never":
		return "never", nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("%w: approvalPolicy %q", sdkerrors.ErrUnsupportedOption, value)
	}
}

func normalizeSandboxMode(value string) (string, error) {
	switch value {
	case "read-only", "workspace-write", "danger-full-access":
		return value, nil
	case "readOnly":
		return "read-only", nil
	case "workspaceWrite":
		return "workspace-write", nil
	case "dangerFullAccess":
		return "danger-full-access", nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("%w: sandbox %q", sdkerrors.ErrUnsupportedOption, value)
	}
}

func normalizeEffort(value string) (string, error) {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh":
		return value, nil
	case "max":
		return "xhigh", nil
	default:
		return "", fmt.Errorf("%w: reasoningEffort %q", sdkerrors.ErrUnsupportedOption, value)
	}
}

func normalizeOutputSchema(value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil //nolint:nilnil // nil output schema is the explicit "unset" signal.
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil //nolint:nilnil // empty schema string is treated as unset.
		}

		// Support passing raw JSON schema.
		var parsed any
		if json.Unmarshal([]byte(s), &parsed) == nil {
			return parsed, nil
		}

		// Support CLI-style file path schema input.
		if data, err := os.ReadFile(s); err == nil {
			if err := json.Unmarshal(data, &parsed); err != nil {
				return nil, fmt.Errorf(
					"%w: outputSchema file %q does not contain valid JSON schema: %v",
					sdkerrors.ErrUnsupportedOption,
					s,
					err,
				)
			}

			return parsed, nil
		}

		// Fall back to raw string for forward compatibility.
		return v, nil
	default:
		return cloneAnyValue(value), nil
	}
}

// buildCollaborationMode constructs the collaborationMode object for turn/start.
// The Codex CLI requires this on each turn/start when plan mode is active —
// ModeKind::Plan enables request_user_input, while ModeKind::Default does not.
func buildCollaborationMode(mode string, model string) map[string]any {
	settings := map[string]any{
		"developerInstructions": nil,
	}

	if model != "" {
		settings["model"] = model
	}

	return map[string]any{
		"mode":     mode,
		"settings": settings,
	}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}

	dst := make(map[string]any, len(src))
	maps.Copy(dst, src)

	return dst
}

func cloneAnyValue(src any) any {
	switch v := src.(type) {
	case nil:
		return nil
	case map[string]any:
		dst := make(map[string]any, len(v))
		for k, item := range v {
			dst[k] = cloneAnyValue(item)
		}

		return dst
	case []any:
		dst := make([]any, len(v))
		for i, item := range v {
			dst[i] = cloneAnyValue(item)
		}

		return dst
	default:
		return v
	}
}

// codexEventDuplicates lists codex/event/* methods that duplicate
// notifications already handled via the standard JSON-RPC protocol.
var codexEventDuplicates = map[string]bool{
	"codex/event/item_started":                true,
	"codex/event/item_completed":              true,
	"codex/event/agent_message_content_delta": true,
	"codex/event/agent_message_delta":         true,
	"codex/event/agent_message":               true,
	"codex/event/user_message":                true,
}

// codexEventSubtypes maps unique codex/event/* methods to their system
// message subtypes.
var codexEventSubtypes = map[string]string{
	"codex/event/task_started":         "task.started",
	"codex/event/task_complete":        "task.complete",
	"codex/event/token_count":          "token.count",
	"codex/event/mcp_startup_update":   "mcp.startup_update",
	"codex/event/mcp_startup_complete": "mcp.startup_complete",
}

// translateCodexEvent handles codex/event/* notifications. Duplicates of
// standard protocol events are logged and dropped; unique events are
// emitted as system messages.
func (a *AppServerAdapter) translateCodexEvent(
	method string,
	params map[string]any,
) map[string]any {
	if codexEventDuplicates[method] {
		a.log.Debug("dropping duplicate codex event",
			slog.String("method", method),
		)

		return nil
	}

	if subtype, ok := codexEventSubtypes[method]; ok {
		return map[string]any{
			"type":    "system",
			"subtype": subtype,
			"data":    params,
		}
	}

	// Unknown codex/event/* — pass through with derived subtype.
	name := strings.TrimPrefix(method, "codex/event/")

	return map[string]any{
		"type":    "system",
		"subtype": "codex.event." + name,
		"data":    params,
	}
}

// extractAndTranslateItem extracts the nested "item" object from app-server
// notification params and converts camelCase types to snake_case. It also
// handles reasoning items whose text lives in a "summary" array.
func (a *AppServerAdapter) extractAndTranslateItem(params map[string]any) map[string]any {
	// App-server nests the item under an "item" key.
	nested, ok := params["item"].(map[string]any)
	if !ok {
		nested = params
	}

	item := make(map[string]any, len(nested))

	maps.Copy(item, nested)

	if itemType, ok := item["type"].(string); ok {
		item["type"] = camelToSnake(itemType)
	}

	// Reasoning items carry text in a "summary" string array, not "text".
	if item["type"] == "reasoning" {
		if summaryArr, ok := item["summary"].([]any); ok && len(summaryArr) > 0 {
			parts := make([]string, 0, len(summaryArr))

			for _, v := range summaryArr {
				if s, ok := v.(string); ok {
					parts = append(parts, s)
				}
			}

			if len(parts) > 0 {
				item["text"] = strings.Join(parts, "\n")
			}
		}
	}

	return item
}

// injectControlResponse fabricates a control_response and injects it into
// the messages channel for the Controller to pick up.
func (a *AppServerAdapter) injectControlResponse(
	requestID string,
	responseData map[string]any,
) {
	msg := map[string]any{
		"type":     "control_response",
		"response": responseData,
	}

	select {
	case a.messages <- msg:
	case <-a.done:
	}
}

func (a *AppServerAdapter) injectErrorControlResponse(requestID string, errMsg string) {
	a.injectControlResponse(requestID, map[string]any{
		"subtype":    "error",
		"request_id": requestID,
		"error":      errMsg,
	})
}

// extractThreadID extracts the thread ID from a thread/start response.
// The ID is nested at result.thread.id.
func extractThreadID(result map[string]any) string {
	// Try nested thread.id first (actual app-server format).
	if thread, ok := result["thread"].(map[string]any); ok {
		if id, ok := thread["id"].(string); ok {
			return id
		}
	}

	// Fallback: try top-level threadId or thread_id.
	if id, ok := result["threadId"].(string); ok {
		return id
	}

	if id, ok := result["thread_id"].(string); ok {
		return id
	}

	return ""
}

// camelToSnake converts a camelCase string to snake_case.
// Specifically handles the known item types from the app-server protocol.
var camelToSnakeMap = map[string]string{
	"agentMessage":     "agent_message",
	"commandExecution": "command_execution",
	"fileChange":       "file_change",
	"mcpToolCall":      "mcp_tool_call",
	"userMessage":      "user_message",
	"webSearch":        "web_search",
	"todoList":         "todo_list",
	"reasoning":        "reasoning",
	"error":            "error",
}

func camelToSnake(s string) string {
	if mapped, ok := camelToSnakeMap[s]; ok {
		return mapped
	}

	return s
}

// methodToSubtype maps a JSON-RPC method name to a control_request subtype.
func methodToSubtype(method string) string {
	// Strip namespace prefix if present (e.g., "hooks/callback" -> "hook_callback")
	parts := strings.SplitN(method, "/", 2)
	if len(parts) == 2 {
		return parts[0] + "_" + parts[1]
	}

	return method
}
