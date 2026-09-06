//go:build qwen_real_cli && darwin

package qwenapp

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func realCLIPlatformAssertionName() string {
	return "darwin_process_groups_close_interrupt_and_no_descendants"
}

func realCLIPlatformProcessSet(t *testing.T, process *Process) []int {
	t.Helper()
	process.mu.Lock()
	if process.command == nil || process.command.Process == nil {
		process.mu.Unlock()
		t.Fatal("real Qwen process has no native PID")
	}
	pid := process.command.Process.Pid
	process.mu.Unlock()
	group, err := syscall.Getpgid(pid)
	if err != nil || group != pid {
		t.Fatal("real Qwen process is not the leader of its dedicated process group")
	}
	return []int{group}
}

func waitRealCLIPlatformStopped(t *testing.T, groups []int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for _, group := range groups {
		for realCLIProcessGroupAlive(group) {
			if time.Now().After(deadline) {
				t.Fatal("macOS process group still contains a process after containment close")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func assertRealCLIPlatformRunning(t *testing.T, groups []int) {
	t.Helper()
	if len(groups) == 0 || !realCLIProcessGroupAlive(groups[0]) {
		t.Fatal("independent macOS process group did not survive sibling close")
	}
}

func realCLIProcessGroupAlive(group int) bool {
	err := syscall.Kill(-group, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
