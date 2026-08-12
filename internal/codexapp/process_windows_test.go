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
)

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
