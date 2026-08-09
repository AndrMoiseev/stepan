//go:build windows

package codexexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCancellationKillsProcessTree(t *testing.T) {
	tests := []struct {
		name   string
		reason TerminationReason
	}{
		{"timeout", TimedOut},
		{"operator", OperatorCanceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "pids.json")
			t.Setenv("GO_WANT_CODEX_HELPER", "tree-parent")
			t.Setenv("CODEX_HELPER_PID_FILE", pidFile)
			cfg := fakeConfig(t, filepath.Join(root, "run"))
			cfg.Timeout = 500 * time.Millisecond
			ctx := context.Background()
			if test.reason == OperatorCanceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cfg.Timeout = 5 * time.Second
				go func() {
					deadline := time.Now().Add(2 * time.Second)
					for {
						if _, err := os.Stat(pidFile); err == nil || time.Now().After(deadline) {
							cancel()
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
				}()
			}

			started := time.Now()
			result, err := Run(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(started) > 3*time.Second || result.TerminationReason != test.reason || result.FailureClass != string(test.reason) {
				t.Fatalf("cancellation result = %+v", result)
			}
			if result.SessionID == nil || *result.SessionID != "interrupted-session" {
				t.Fatalf("partial evidence did not preserve session: %+v", result)
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			var pids []int
			if err := json.Unmarshal(data, &pids); err != nil || len(pids) != 3 {
				t.Fatalf("pids = %v, %v", pids, err)
			}
			for _, pid := range pids {
				if alive, err := processAlive(pid); err != nil || alive {
					t.Fatalf("process %d remains alive: alive=%v err=%v", pid, alive, err)
				}
			}
			if _, err := os.Stat(filepath.Join(cfg.ArtifactDir, "result.json")); err != nil {
				t.Fatal("partial result missing:", err)
			}
		})
	}
}

func processAlive(pid int) (bool, error) {
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, syscall.Errno(87)) {
			return false, nil
		}
		return false, err
	}
	defer syscall.CloseHandle(handle)
	state, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	return state == syscall.WAIT_TIMEOUT, nil
}
