//go:build windows

package checkexec

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func assertProcessStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		alive, err := windowsProcessAlive(pid)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("descendant process %d remains alive", pid)
		case <-poll.C:
		}
	}
}

func windowsProcessAlive(pid int) (bool, error) {
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, syscall.Errno(87)) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(handle)
	state, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, err
	}
	return state == syscall.WAIT_TIMEOUT, nil
}
