package subprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

func (t *AppServerTransport) ProcessLifecycle() (config.ProcessLifecycle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.processStarted {
		return nil, config.ErrProcessNotStarted
	}
	return t, nil
}
func (t *AppServerTransport) Done() <-chan struct{}       { return t.processDone }
func (t *AppServerTransport) ReaderDone() <-chan struct{} { return t.readDone }
func (t *AppServerTransport) Wait(ctx context.Context) (config.ProcessResult, error) {
	select {
	case <-t.processDone:
	case <-ctx.Done():
		return config.ProcessResult{}, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.processResult, nil
}

// This is the only exec.Cmd.Wait owner, including spontaneous exits.
func (t *AppServerTransport) waitProcess(ctx context.Context, cmd *exec.Cmd) {
	err := cmd.Wait()
	result := config.ProcessResult{ExitCode: -1, ContextErr: ctx.Err(), ExitedAt: time.Now().UTC()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if state, ok := processWaitStatus(cmd); ok {
		result.Signaled = state.Signaled()
		result.CoreDump = state.CoreDump()
		if result.Signaled {
			result.Signal = state.Signal().String()
		}
	}
	if err != nil {
		result.WaitErr = fmt.Errorf("codex process exited: code=%d signal=%s", result.ExitCode, result.Signal)
	}
	t.mu.Lock()
	result.StopRequested = t.killRequested
	t.processResult = result
	close(t.processDone)
	t.mu.Unlock()
}

func processWaitStatus(cmd *exec.Cmd) (syscall.WaitStatus, bool) {
	if cmd.ProcessState == nil {
		var zero syscall.WaitStatus
		return zero, false
	}
	state, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return state, ok
}

func (t *AppServerTransport) Stop(ctx context.Context) error {
	t.shutdownOnce.Do(func() { go t.shutdownProcess() })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-t.shutdownDone:
		return t.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *AppServerTransport) shutdownProcess() {
	defer close(t.shutdownDone)
	t.mu.Lock()
	t.closing = true
	close(t.stop)
	started := t.processStarted
	cmd := t.cmd
	stdin := t.stdin
	stdout := t.stdoutRead
	t.mu.Unlock()
	var errs []error
	if stdin != nil {
		if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("close process stdin: %w", err))
		}
	}
	if started {
		t.mu.Lock()
		select {
		case <-t.processDone:
		default:
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				errs = append(errs, fmt.Errorf("kill codex process: %w", err))
			} else if err == nil {
				t.killRequested = true
			}
		}
		t.mu.Unlock()
		<-t.processDone
		// Explicit shutdown may discard output. Release a reader even if a
		// descendant still inherited stdout after the owned process exited.
		select {
		case <-t.readDone:
		default:
			if stdout != nil {
				if err := stdout.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					errs = append(errs, fmt.Errorf("close process stdout: %w", err))
				}
			}
		}
		<-t.readDone
		t.delivery.Wait()
	}
	t.shutdownErr = errors.Join(errs...)
}

func (a *AppServerAdapter) ProcessLifecycle() (config.ProcessLifecycle, error) {
	provider, ok := a.inner.(config.ProcessLifecycleProvider)
	if !ok {
		return nil, config.ErrProcessUnsupported
	}
	return provider.ProcessLifecycle()
}
