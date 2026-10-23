package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInfo_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	info := Info{
		ID:                     "o4-mini",
		Model:                  "o4-mini",
		DisplayName:            "O4 Mini",
		Description:            "A small, fast model",
		IsDefault:              true,
		Hidden:                 false,
		DefaultReasoningEffort: "medium",
		SupportedReasoningEfforts: []ReasoningEffortOption{
			{Value: "low", Label: "Low"},
			{Value: "medium", Label: "Medium"},
			{Value: "high", Label: "High"},
		},
		InputModalities:     []string{"text", "image"},
		SupportsPersonality: false,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded Info

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, info, decoded)
}

func TestInfo_JSONFields(t *testing.T) {
	t.Parallel()

	raw := `{
		"id": "gpt-4.1",
		"model": "gpt-4.1",
		"displayName": "GPT-4.1",
		"description": "Full-size model",
		"isDefault": false,
		"hidden": true,
		"defaultReasoningEffort": "high",
		"supportedReasoningEfforts": [{"value": "high", "label": "High"}],
		"inputModalities": ["text"],
		"supportsPersonality": true
	}`

	var info Info

	err := json.Unmarshal([]byte(raw), &info)
	require.NoError(t, err)

	assert.Equal(t, "gpt-4.1", info.ID)
	assert.Equal(t, "GPT-4.1", info.DisplayName)
	assert.True(t, info.Hidden)
	assert.True(t, info.SupportsPersonality)
	assert.Equal(t, "high", info.DefaultReasoningEffort)
	assert.Len(t, info.SupportedReasoningEfforts, 1)
	assert.Equal(t, []string{"text"}, info.InputModalities)
}

func TestListResponse_EmptyModels(t *testing.T) {
	t.Parallel()

	raw := `{"models": []}`

	var resp ListResponse

	err := json.Unmarshal([]byte(raw), &resp)
	require.NoError(t, err)
	assert.Empty(t, resp.Models)
}

func TestListResponse_MultipleModels(t *testing.T) {
	t.Parallel()

	raw := `{
		"models": [
			{"id": "o4-mini", "model": "o4-mini", "displayName": "O4 Mini"},
			{"id": "o3", "model": "o3", "displayName": "O3"}
		]
	}`

	var resp ListResponse

	err := json.Unmarshal([]byte(raw), &resp)
	require.NoError(t, err)
	require.Len(t, resp.Models, 2)
	assert.Equal(t, "o4-mini", resp.Models[0].ID)
	assert.Equal(t, "o3", resp.Models[1].ID)
}
