//go:build darwin

package checkexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRunContextTimeoutKillsGroupAfterLeaderExits(t *testing.T) {
	parentReady, childReady := readinessPaths(t)
	started := time.Now()
	result, err := RunContext(context.Background(), exitedLeaderHelperCommand(t, parentReady, childReady, time.Second))
	if !errors.Is(err, ErrTimeout) || result.Failure != FailureTimeout {
		t.Fatalf("timeout result = %+v, %v", result, err)
	}
	assertPromptTermination(t, started)
	assertExitedLeaderDiagnostics(t, result)
	assertHelperTreeStopped(t, parentReady, childReady)
}

func TestRunContextCancellationKillsGroupAfterLeaderExits(t *testing.T) {
	parentReady, childReady := readinessPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultChannel := make(chan struct {
		result Result
		err    error
	}, 1)
	started := time.Now()
	go func() {
		result, err := RunContext(ctx, exitedLeaderHelperCommand(t, parentReady, childReady, 10*time.Second))
		resultChannel <- struct {
			result Result
			err    error
		}{result, err}
	}()
	awaitFile(t, parentReady)
	cancel()
	select {
	case outcome := <-resultChannel:
		if !errors.Is(outcome.err, ErrCanceled) || outcome.result.Failure != FailureCanceled {
			t.Fatalf("cancellation result = %+v, %v", outcome.result, outcome.err)
		}
		assertPromptTermination(t, started)
		assertExitedLeaderDiagnostics(t, outcome.result)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled check did not return")
	}
	assertHelperTreeStopped(t, parentReady, childReady)
}

func exitedLeaderHelperCommand(t *testing.T, parentReady, childReady string, timeout time.Duration) Command {
	t.Helper()
	return Command{
		Program: os.Args[0],
		Args: []string{
			"--",
			"--exit-parent-ready=" + parentReady,
			"--hang-child-ready=" + childReady,
		},
		Env:     map[string]string{helperEnvironment: "1"},
		CWD:     t.TempDir(),
		Timeout: timeout,
	}
}

func assertPromptTermination(t *testing.T, started time.Time) {
	t.Helper()
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("termination took %s", elapsed)
	}
}

func assertExitedLeaderDiagnostics(t *testing.T, result Result) {
	t.Helper()
	if !bytes.Contains(result.Stdout, []byte("stdout before leader exit")) || !bytes.Contains(result.Stderr, []byte("stderr before leader exit")) {
		t.Fatalf("leader diagnostics were not retained: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}
