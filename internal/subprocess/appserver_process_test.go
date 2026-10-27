package subprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mafilus/codex-agent-sdk-go/internal/config"
)

// The test executable is the subprocess: no shell, model or new script.
func TestMain(m *testing.M) {
	if mode := os.Getenv("SDK_PROCESS_FIXTURE"); mode != "" {
		os.Exit(runProcessFixture(mode))
	}
	os.Exit(m.Run())
}
func runProcessFixture(mode string) int {
	if len(os.Args) != 3 || os.Args[1] != "app-server" || os.Args[2] != "--stdio" {
		return 93
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			return 94
		}
		if msg.Method == "initialize" {
			if mode == "bad-init" {
				return 7
			}
			if _, err := fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n", *msg.ID); err != nil {
				return 95
			}
		}
		if msg.Method == "initialized" && mode == "blocked-input" {
			if _, err := fmt.Println(`{"jsonrpc":"2.0","method":"fixture/blocked","params":{}}`); err != nil {
				return 95
			}
			// The parent cancels a request and must kill this non-reading process.
			<-time.After(10 * time.Second)
			return 98
		}
		if msg.Method == "initialized" && mode != "wait" {
			for i := range 600 {
				if _, err := fmt.Printf("{\"jsonrpc\":\"2.0\",\"method\":\"item/agentMessage/delta\",\"params\":{\"index\":%d}}\n", i); err != nil {
					return 95
				}
			}
			if _, err := fmt.Println(`{"jsonrpc":"2.0","method":"turn/completed","params":{}}`); err != nil {
				return 95
			}
			code, err := strconv.Atoi(mode)
			if err != nil {
				return 96
			}
			return code
		}
	}
	if scanner.Err() != nil {
		return 97
	}
	return 0
}
func processTransport(t *testing.T, mode string) *AppServerTransport {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{CliPath: binary, SkipVersionCheck: true, Env: map[string]string{"SDK_PROCESS_FIXTURE": mode}})
}
func TestAppServerOwnedProcessNaturalExitAndTerminalDrain(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			transport := processTransport(t, strconv.Itoa(code))
			t.Cleanup(func() {
				if err := transport.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := transport.ProcessLifecycle(); !errors.Is(err, config.ErrProcessNotStarted) {
				t.Fatal(err)
			}
			if err := transport.Start(ctx); err != nil {
				t.Fatal(err)
			}
			lifecycle, err := transport.ProcessLifecycle()
			if err != nil {
				t.Fatal(err)
			}
			result, err := lifecycle.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != code || result.Signaled || result.StopRequested || result.ContextErr != nil || (result.WaitErr != nil) != (code != 0) {
				t.Fatalf("wrong process result: %+v", result)
			}
			// OS Wait has happened with no event consumer; terminal output must survive.
			for i := range 601 {
				select {
				case n, ok := <-transport.Notifications():
					if !ok {
						t.Fatalf("notification %d lost", i)
					}
					if i == 600 && n.Method != "turn/completed" {
						t.Fatal("terminal lost")
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			<-lifecycle.ReaderDone()
			again, err := lifecycle.Wait(ctx)
			if err != nil || again.ExitCode != result.ExitCode || !again.ExitedAt.Equal(result.ExitedAt) {
				t.Fatal("unstable Wait")
			}
		})
	}
}
func TestAppServerOwnedProcessStopAndCanceledWaiter(t *testing.T) {
	transport := processTransport(t, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := transport.Start(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := transport.Wait(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := transport.Stop(ctx); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := transport.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	result, err := transport.Wait(ctx)
	if err != nil || !result.StopRequested {
		t.Fatalf("wrong stop result: %+v %v", result, err)
	}
	select {
	case <-transport.ReaderDone():
	default:
		t.Fatal("reader not joined")
	}
}
func TestAppServerOwnedProcessContextAndFailedInitialization(t *testing.T) {
	for _, mode := range []string{"wait", "bad-init"} {
		t.Run(mode, func(t *testing.T) {
			transport := processTransport(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := transport.Start(ctx)
			if mode == "bad-init" {
				if err == nil {
					t.Fatal("expected initialize error")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			waiter, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			result, err := transport.Wait(waiter)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "bad-init" && result.ExitCode != 7 {
				t.Fatalf("failed initialize result: %+v", result)
			}
			if mode == "wait" && !errors.Is(result.ContextErr, context.Canceled) {
				t.Fatalf("missing launch cancellation: %+v", result)
			}
			if err := transport.Stop(waiter); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestAppServerOwnedProcessMissingBinary(t *testing.T) {
	transport := NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{CliPath: "/definitely-missing-sdk-process"})
	if err := transport.Start(context.Background()); err == nil {
		t.Fatal("expected start failure")
	}
	if _, err := transport.ProcessLifecycle(); !errors.Is(err, config.ErrProcessNotStarted) {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerOwnedProcessSpawnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(path, []byte("invalid executable format"), 0700); err != nil {
		t.Fatal(err)
	}
	transport := NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{CliPath: path})
	if err := transport.Start(context.Background()); err == nil {
		t.Fatal("expected spawn failure")
	}
	if _, err := transport.ProcessLifecycle(); !errors.Is(err, config.ErrProcessNotStarted) {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerOwnedProcessCanceledStopStillJoins(t *testing.T) {
	transport := processTransport(t, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := transport.Start(ctx); err != nil {
		t.Fatal(err)
	}
	caller, stop := context.WithCancel(context.Background())
	stop()
	if err := transport.Stop(caller); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := transport.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := transport.Wait(ctx)
	if err != nil || !result.StopRequested {
		t.Fatalf("shutdown did not continue: %+v %v", result, err)
	}
}

type delayedProcessReaderClose struct {
	io.ReadCloser
	entered chan struct{}
	release chan struct{}
}

func (r *delayedProcessReaderClose) Close() error {
	close(r.entered)
	<-r.release
	return r.ReadCloser.Close()
}

func TestAppServerOwnedProcessStopBudgetJoinsSharedReaderCleanup(t *testing.T) {
	// Unit probe for the post-OS-Wait reader join, not a fake process proof.
	transport := NewAppServerTransport(slog.New(slog.NewTextHandler(io.Discard, nil)), &config.Options{})
	reader, writer := io.Pipe()
	defer func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}()
	delayed := &delayedProcessReaderClose{ReadCloser: reader, entered: make(chan struct{}), release: make(chan struct{})}
	transport.stdoutRead = delayed
	transport.stdout = bufio.NewScanner(reader)
	transport.processStarted = true
	close(transport.processDone)
	go transport.readLoop()
	caller, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- transport.Stop(caller) }()
	<-delayed.entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(delayed.release)
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := transport.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-transport.ReaderDone():
	default:
		t.Fatal("reader not joined")
	}
}
