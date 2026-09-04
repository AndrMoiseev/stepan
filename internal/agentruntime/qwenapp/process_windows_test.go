//go:build windows

package qwenapp

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func makeDirectoryLink(link, target string) error {
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create test junction: %w: %s", err, output)
	}
	return nil
}

func TestCloseKillsQwenProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "tree")
	t.Setenv("STEPAN_QWENAPP_PID_FILE", pidFile)
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	pids := waitForQwenPIDs(t, pidFile)
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for {
			alive, err := qwenProcessAlive(pid)
			if err != nil {
				t.Fatal(err)
			}
			if !alive {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("process %d remains alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func qwenProcessAlive(pid int) (bool, error) {
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
