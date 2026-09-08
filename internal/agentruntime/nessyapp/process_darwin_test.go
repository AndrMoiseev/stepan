//go:build darwin

package nessyapp

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func makeDirectoryLink(link, target string) error { return os.Symlink(target, link) }

func TestCloseKillsNessyProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	t.Setenv("GO_WANT_NESSYAPP_FAKE", "tree")
	t.Setenv("STEPAN_NESSYAPP_PID_FILE", pidFile)
	process := NewProcess(Config{AuthToken: testAuthToken(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	pids := waitForNessyPIDs(t, pidFile)
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(3 * time.Second)
		for nessyProcessAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d remains alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func nessyProcessAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
