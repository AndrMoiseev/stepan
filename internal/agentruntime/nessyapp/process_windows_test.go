//go:build windows

package nessyapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func makeDirectoryLink(link, target string) error {
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create test junction: %w: %s", err, output)
	}
	return nil
}

func TestCanonicalTargetWithinAcceptsShortRootAlias(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact directory requiring short alias")
	if err := os.MkdirAll(artifact, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(artifact, "existing.md")
	if err := os.WriteFile(target, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	shortArtifact := shortPathForTest(t, artifact)
	shortTarget := filepath.Join(shortArtifact, filepath.Base(target))
	got, err := canonicalTargetWithin(t.TempDir(), shortArtifact, shortTarget)
	if err != nil {
		t.Fatalf("canonical target through short root alias: %v", err)
	}
	want, err := canonicalExistingPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical target = %q, want %q", got, want)
	}
}

func TestCanonicalReadableTargetAcceptsShortRootAlias(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace directory requiring short alias")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(workspace, "input.md")
	if err := os.WriteFile(target, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}

	shortWorkspace := shortPathForTest(t, workspace)
	context := newFilePolicy(shortWorkspace, "").context("session", "turn")
	got, err := canonicalReadableTarget(context, filepath.Join(shortWorkspace, filepath.Base(target)))
	if err != nil {
		t.Fatalf("canonical readable target through short root alias: %v", err)
	}
	want, err := canonicalExistingPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("canonical readable target = %q, want %q", got, want)
	}
}

func shortPathForTest(t *testing.T, path string) string {
	t.Helper()
	longPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	length, err := windows.GetShortPathName(longPath, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Skipf("Windows short path is unavailable: %v", err)
	}
	if length == 0 || length >= uint32(len(buffer)) {
		t.Fatalf("invalid Windows short path length %d", length)
	}
	shortPath := filepath.Clean(windows.UTF16ToString(buffer[:length]))
	if shortPath == filepath.Clean(path) {
		t.Skip("Windows 8.3 aliases are disabled on this volume")
	}
	return shortPath
}

func TestCloseKillsNessyProcessTree(t *testing.T) {
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
		for {
			alive, err := nessyProcessAlive(pid)
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

func nessyProcessAlive(pid int) (bool, error) {
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
