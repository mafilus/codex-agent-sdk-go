package protocol

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

// Connection owns the transport, controller and session for one connection.
// Callers retain backend selection, option validation and turn completion.
// Construct a new Connection for each startup attempt; Start and Close must not
// run concurrently. Close itself is safe for concurrent callers.
type Connection struct {
	Transport  config.Transport
	Controller *Controller
	Session    *Session

	closeOnce sync.Once
	closeErr  error
}

// NewConnection binds the resources without starting the transport. Keeping the
// connection on failed startup preserves optional process lifecycle observation.
func NewConnection(log *slog.Logger, transport config.Transport, options *config.Options) *Connection {
	controller := NewController(log, transport)
	return &Connection{
		Transport:  transport,
		Controller: controller,
		Session:    NewSession(log, controller, options),
	}
}

// Start opens the transport and registers session handlers. forceInitialize is
// true for app-server queries and persistent/streaming clients; otherwise the
// session decides whether the selected options require initialization.
// Any startup failure releases the connection's resources before returning.
func (c *Connection) Start(ctx context.Context, forceInitialize bool) (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, c.Close())
		}
	}()

	if err := c.Transport.Start(ctx); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}
	if err := c.Controller.Start(ctx); err != nil {
		return fmt.Errorf("start protocol controller: %w", err)
	}

	c.Session.RegisterMCPServers()
	c.Session.RegisterDynamicTools()
	c.Session.RegisterHandlers()
	if forceInitialize || c.Session.NeedsInitialization() {
		if err := c.Session.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize session: %w", err)
		}
	}
	return nil
}

// Close stops the controller before releasing its transport. Concurrent callers
// join the same cleanup and receive the same error.
func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.Controller.Stop()
		c.closeErr = c.Transport.Close()
	})
	return c.closeErr
}
