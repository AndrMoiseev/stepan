//go:build darwin

package qwenapp

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func makeDirectoryLink(link, target string) error { return os.Symlink(target, link) }

func TestCloseKillsQwenProcessGroup(t *testing.T) {
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
		for qwenProcessAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d remains alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func runQwenTreeFake() int {
	switch os.Getenv("STEPAN_QWENAPP_TREE_LEVEL") {
	case "":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_QWENAPP_TREE_LEVEL=child")
		if err := command.Start(); err != nil {
			return 51
		}
		time.Sleep(30 * time.Second)
	case "child":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_QWENAPP_TREE_LEVEL=grandchild")
		if err := command.Start(); err != nil {
			return 52
		}
		data, err := json.Marshal([]int{os.Getppid(), os.Getpid(), command.Process.Pid})
		if err != nil || os.WriteFile(os.Getenv("STEPAN_QWENAPP_PID_FILE"), data, 0o600) != nil {
			return 53
		}
		time.Sleep(30 * time.Second)
	case "grandchild":
		time.Sleep(30 * time.Second)
	default:
		return 54
	}
	return 0
}

func waitForQwenPIDs(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			var pids []int
			if json.Unmarshal(data, &pids) == nil && len(pids) == 3 {
				return pids
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake Qwen process group did not report PIDs")
	return nil
}

func qwenProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
