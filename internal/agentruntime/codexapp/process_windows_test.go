//go:build windows

package codexapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRuntimeAcceptsEquivalentShortWorkspacePath(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace directory requiring short alias")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	shortWorkspace := shortPathForTest(t, workspace)
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "runtime-interrupt-ignore")
	runtime, err := StartRuntime(os.Args[0], workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if _, err := runtime.StartThread(testThreadConfig(shortWorkspace)); err != nil {
		t.Fatalf("start thread with equivalent short workspace path: %v", err)
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

func TestProcessCloseKillsTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	t.Setenv("GO_WANT_CODEXAPP_FAKE", "process-tree")
	t.Setenv("STEPAN_CODEXAPP_PID_FILE", pidFile)
	process := NewProcess(os.Args[0], t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}

	var data []byte
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err = os.ReadFile(pidFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		_ = process.Close()
		t.Fatal(err)
	}
	var pids []int
	if err := json.Unmarshal(data, &pids); err != nil || len(pids) != 3 {
		_ = process.Close()
		t.Fatalf("pids = %v, %v", pids, err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		deadline := time.Now().Add(2 * time.Second)
		for {
			alive, err := codexAppProcessAlive(pid)
			if err != nil || !alive {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("process %d remains alive", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestRuntimeInterruptKillsTree(t *testing.T) {
	for _, scenario := range []string{"runtime-interrupt-ack", "runtime-interrupt-ignore"} {
		t.Run(scenario, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pids.json")
			t.Setenv("STEPAN_CODEXAPP_PID_FILE", pidFile)
			runtime, _, turnErr := startRuntimeTurn(t, scenario)
			if scenario == "runtime-interrupt-ignore" {
				runtime.grace = 100 * time.Millisecond
			}
			pids := waitRuntimePIDs(t, pidFile)
			if err := runtime.Interrupt(); err != nil {
				t.Fatal(err)
			}
			if err := <-turnErr; !errors.Is(err, ErrTurnInterrupted) {
				t.Fatalf("turn error = %v", err)
			}
			for _, pid := range pids {
				deadline := time.Now().Add(2 * time.Second)
				for {
					alive, err := codexAppProcessAlive(pid)
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
		})
	}
}

func waitRuntimePIDs(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var pids []int
			if json.Unmarshal(data, &pids) == nil && len(pids) == 3 {
				return pids
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake process tree did not report PIDs")
	return nil
}

func codexAppProcessAlive(pid int) (bool, error) {
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
