package client

import (
	"context"
	"errors"
	"testing"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

func TestClientOptionalProcessLifecycle(t *testing.T) {
	c := New()
	if _, err := c.ProcessLifecycle(); !errors.Is(err, config.ErrProcessNotStarted) {
		t.Fatal(err)
	}
	if err := c.Start(context.Background(), &config.Options{Transport: newMockTransport()}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ProcessLifecycle(); !errors.Is(err, config.ErrProcessUnsupported) {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
