// Package model defines types for Codex CLI model discovery.
package model

// Info describes a model available from the Codex CLI.
type Info struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Description               string                  `json:"description"`
	IsDefault                 bool                    `json:"isDefault"`
	Hidden                    bool                    `json:"hidden"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
	InputModalities           []string                `json:"inputModalities"`
	SupportsPersonality       bool                    `json:"supportsPersonality"`
}

// ReasoningEffortOption describes a selectable reasoning effort level.
type ReasoningEffortOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// ListResponse is the response payload from the model/list RPC method.
type ListResponse struct {
	Models []Info `json:"models"`
}
