package codexsdk

import "github.com/mafilus/codex-agent-sdk-go/internal/config"

// Optional process observation for the effective SDK-owned app-server. Client
// deliberately stays unchanged so existing client fakes remain compatible.
type ProcessLifecycle = config.ProcessLifecycle
type ProcessLifecycleProvider = config.ProcessLifecycleProvider
type ProcessResult = config.ProcessResult

var (
	ErrProcessNotStarted  = config.ErrProcessNotStarted
	ErrProcessUnsupported = config.ErrProcessUnsupported
)

func (c *clientWrapper) ProcessLifecycle() (ProcessLifecycle, error) {
	return c.impl.ProcessLifecycle()
}
