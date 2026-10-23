package codexsdk

import (
	"context"
	"fmt"
)

// ListModels spawns a temporary Codex CLI session to discover available models.
// The session is automatically closed after the model list is retrieved.
func ListModels(ctx context.Context, opts ...Option) ([]ModelInfo, error) {
	var models []ModelInfo

	err := WithClient(ctx, func(c Client) error {
		var err error

		models, err = c.ListModels(ctx)
		if err != nil {
			return fmt.Errorf("list models: %w", err)
		}

		return nil
	}, opts...)
	if err != nil {
		return nil, err
	}

	return models, nil
}
