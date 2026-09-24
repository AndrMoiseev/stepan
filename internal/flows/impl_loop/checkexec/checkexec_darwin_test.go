//go:build darwin

package checkexec

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestRunContextTimeoutKillsGroupAfterLeaderExits(t *testing.T) {
	parentReady, childReady := readinessPaths(t)
	started := time.Now()
	result, err := RunContext(context.Background(), exitedLeaderHelperCommand(t, parentReady, childReady, "", time.Second))
	if !errors.Is(err, ErrTimeout) || result.Failure != FailureTimeout {
		t.Fatalf("timeout result = %+v, %v", result, err)
	}
	assertPromptTermination(t, started)
	assertExitedLeaderDiagnostics(t, result)
	assertHelperTreeStopped(t, parentReady, childReady)
}

func TestRunContextCancellationKillsGroupAfterLeaderExits(t *testing.T) {
	parentReady, childReady := readinessPaths(t)
	leaderExiting := parentReady + ".leader-exiting"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultChannel := make(chan struct {
		result Result
		err    error
	}, 1)
	started := time.Now()
	go func() {
		result, err := RunContext(ctx, exitedLeaderHelperCommand(t, parentReady, childReady, leaderExiting, 10*time.Second))
		resultChannel <- struct {
			result Result
			err    error
		}{result, err}
	}()
	awaitFile(t, parentReady)
	awaitFile(t, leaderExiting)
	assertProcessAlive(t, awaitHelperPID(t, parentReady))
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

func exitedLeaderHelperCommand(t *testing.T, parentReady, childReady, leaderExiting string, timeout time.Duration) Command {
	t.Helper()
	command := Command{
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
	if leaderExiting != "" {
		command.Args = append(command.Args, "--leader-exiting="+leaderExiting)
	}
	return command
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

func assertProcessAlive(t *testing.T, pid int) {
	t.Helper()
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		t.Fatalf("descendant process %d exited before cancellation", pid)
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		t.Fatal(err)
	}
}
