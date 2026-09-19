//go:build darwin

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
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("descendant process %d remains alive", pid)
		case <-poll.C:
		}
	}
}
