//go:build integration

package integration

import (
	"context"
	"iter"

	codexsdk "github.com/mafilus/codex-agent-sdk-go"
)

func liveQuery(ctx context.Context, prompt string, opts ...codexsdk.Option) iter.Seq2[codexsdk.Message, error] {
	return codexsdk.Query(ctx, prompt, liveOptions(opts...)...)
}

type liveClient struct{ codexsdk.Client }

func newLiveClient() *liveClient { return &liveClient{Client: codexsdk.NewClient()} }
func (c *liveClient) Start(ctx context.Context, opts ...codexsdk.Option) error {
	return c.Client.Start(ctx, liveOptions(opts...)...)
}
func (c *liveClient) StartWithPrompt(ctx context.Context, prompt string, opts ...codexsdk.Option) error {
	return c.Client.StartWithPrompt(ctx, prompt, liveOptions(opts...)...)
}
func (c *liveClient) StartWithStream(ctx context.Context, messages iter.Seq[codexsdk.StreamingMessage], opts ...codexsdk.Option) error {
	return c.Client.StartWithStream(ctx, messages, liveOptions(opts...)...)
}
func (c *liveClient) ProcessLifecycle() (codexsdk.ProcessLifecycle, error) {
	provider, ok := c.Client.(codexsdk.ProcessLifecycleProvider)
	if !ok {
		return nil, codexsdk.ErrProcessUnsupported
	}
	return provider.ProcessLifecycle()
}
