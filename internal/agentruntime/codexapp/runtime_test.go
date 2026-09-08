package codexapp

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRuntimeInterruptAcknowledgedAndRepeatedClose(t *testing.T) {
	runtime, thread, turnErr := startRuntimeTurn(t, "runtime-interrupt-ack")
	if err := runtime.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := <-turnErr; !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("turn error = %v", err)
	}
	if err := runtime.Interrupt(); err != nil {
		t.Fatal("second interrupt:", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal("second close:", err)
	}
	if _, _, _, active := runtime.connection.activeTurn(); active {
		t.Fatal("turn ID remained active after interrupt")
	}
	if _, err := runtime.RunTurn(thread, "retry"); !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("closed runtime turn = %v", err)
	}
}

func TestRuntimeCloseDuringTurnIsGracefulNotInterrupt(t *testing.T) {
	runtime, _, turnErr := startRuntimeTurn(t, "runtime-interrupt-ignore")
	started := time.Now()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("close took %v", elapsed)
	}
	if err := <-turnErr; !errors.Is(err, ErrRuntimeClosed) || errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("turn error = %v", err)
	}
}

func TestRuntimeInterruptUsesBoundedGrace(t *testing.T) {
	runtime, _, turnErr := startRuntimeTurn(t, "runtime-interrupt-ignore")
	runtime.grace = 100 * time.Millisecond
	started := time.Now()
	if err := runtime.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("interrupt took %v", elapsed)
	}
	if err := <-turnErr; !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("turn error = %v", err)
	}
	if interruptGracePeriod != 3*time.Second {
		t.Fatalf("production grace = %v", interruptGracePeriod)
	}
}

func TestRuntimeCloseOutsideTurnIsImmediate(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "runtime-interrupt-ignore")
	runtime, err := StartRuntime(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("close took %v", elapsed)
	}
}

func TestRuntimeCrashRequiresNewRuntimeAndThread(t *testing.T) {
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "runtime-crash")
	workspace := t.TempDir()
	old, err := StartRuntime(os.Args[0], workspace)
	if err != nil {
		t.Fatal(err)
	}
	oldThread, err := old.StartThread(testThreadConfig(workspace))
	if err != nil {
		t.Fatal(err)
	}
	oldPID := old.process.command.Process.Pid
	_, err = old.RunTurn(oldThread, "crash")
	if !errors.Is(err, ErrAppServerExited) || !strings.Contains(err.Error(), "fake crash") {
		t.Fatalf("crash error = %v", err)
	}
	if _, err := old.StartThread(testThreadConfig(workspace)); !errors.Is(err, ErrAppServerExited) {
		t.Fatalf("old runtime reused = %v", err)
	}
	_ = old.Close()

	t.Setenv("GO_WANT_CODEXAPP_FAKE", "runtime-restart")
	fresh, err := StartRuntime(os.Args[0], workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	newThread, err := fresh.StartThread(testThreadConfig(workspace))
	if err != nil {
		t.Fatal(err)
	}
	oldCodexThread := oldThread.(*Thread)
	newCodexThread := newThread.(*Thread)
	if oldPID == fresh.process.command.Process.Pid || oldCodexThread.ID == newCodexThread.ID || newCodexThread.ID != "thread-new" {
		t.Fatalf("old/new thread IDs = %q/%q", oldCodexThread.ID, newCodexThread.ID)
	}
}

func startRuntimeTurn(t *testing.T, scenario string) (*Runtime, *Thread, <-chan error) {
	t.Helper()
	t.Setenv("GO_WANT_CODEXAPP_FAKE", scenario)
	runtime, err := StartRuntime(os.Args[0], t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	thread, err := runtime.StartThread(testThreadConfig(runtime.workspace))
	if err != nil {
		t.Fatal(err)
	}
	turnErr := make(chan error, 1)
	go func() {
		_, err := runtime.RunTurn(thread, "wait")
		turnErr <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		_, turnID, _, active := runtime.connection.activeTurn()
		if active && turnID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not start")
		}
		time.Sleep(time.Millisecond)
	}
	return runtime, thread.(*Thread), turnErr
}
