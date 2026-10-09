//go:build darwin && process_integration

package processjob

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCloseSucceedsAfterLeaderExitsBeforeWait(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 0")
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if err := job.Prepare(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if err := job.Assign(command.Process); err != nil {
		t.Fatal(err)
	}
	// Keep the exited leader unreaped: Darwin can return EPERM when the
	// group's only member is a zombie, even though there is nothing to kill.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		process, err := unix.SysctlKinfoProc("kern.proc.pid", command.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if process.Proc.P_stat == 5 { // SZOMB from sys/proc.h
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("leader did not exit")
		case <-poll.C:
		}
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal("repeated close:", err)
	}
}

func TestCloseKillsRemainingGroupMemberAfterLeaderIsReaped(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	command := exec.Command("/bin/sh", "-c", "sleep 60 & printf '%s' \"$!\" > \"$STEPAN_PROCESSJOB_PID_FILE\"")
	command.Env = append(os.Environ(), "STEPAN_PROCESSJOB_PID_FILE="+pidFile)
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if err := job.Prepare(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := job.Assign(command.Process); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	persistentChild := awaitPID(t, pidFile)
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	assertStopped(t, persistentChild)
}

func awaitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			value := strings.TrimSpace(string(data))
			if value != "" {
				pid, err := strconv.Atoi(value)
				if err != nil || pid <= 1 {
					t.Fatalf("descendant PID %q: %v", data, err)
				}
				return pid
			}
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for descendant PID at %s", path)
		case <-poll.C:
		}
	}
}

func assertStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("remaining process-group member %d is still alive", pid)
		case <-poll.C:
		}
	}
}
