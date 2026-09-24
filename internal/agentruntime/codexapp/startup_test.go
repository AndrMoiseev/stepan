//go:build process_integration

package codexapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeStartupCancellationDuringHandshake(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "initialize-silent")
	ready := filepath.Join(t.TempDir(), "ready")
	t.Setenv("STEPAN_INITIALIZE_READY", ready)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	workspace := t.TempDir()
	go func() {
		runtime, err := StartRuntimeWithConfigContext(ctx, RuntimeConfig{Executable: os.Args[0], Workspace: workspace})
		if runtime != nil {
			err = errors.Join(err, runtime.Close())
		}
		done <- err
	}()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("startup exited before initialize: %v", err)
		case <-deadline:
			cancel()
			<-done
			t.Fatal("initialize request was not received")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		err := <-done
		t.Fatalf("startup ignored cancellation until fake exited: %v", err)
	}
}

func TestRuntimeStartupTimeoutDuringHandshake(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "initialize-silent")
	t.Setenv("STEPAN_INITIALIZE_READY", filepath.Join(t.TempDir(), "ready"))
	started := time.Now()
	runtime, err := startRuntimeWithConfig(context.Background(), RuntimeConfig{Executable: os.Args[0], Workspace: t.TempDir()}, time.Second)
	if runtime != nil {
		_ = runtime.Close()
		t.Fatal("timed-out startup returned a runtime")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("startup timeout took %v", elapsed)
	}
}

func TestRuntimeStartupRejectsAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtime, err := StartRuntimeWithConfigContext(ctx, RuntimeConfig{Executable: "must-not-be-started", Workspace: t.TempDir()})
	if runtime != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup = %v, %v", runtime, err)
	}
}

func TestRuntimeOutlivesStartupContext(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "runtime-interrupt-ignore")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace := t.TempDir()
	runtime, err := StartRuntimeWithConfigContext(ctx, RuntimeConfig{Executable: os.Args[0], Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	cancel()
	if _, err := runtime.StartThread(testThreadConfig(workspace)); err != nil {
		t.Fatalf("startup cancellation closed returned runtime: %v", err)
	}
}
