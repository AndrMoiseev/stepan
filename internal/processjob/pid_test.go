package processjob

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestAwaitPIDWaitsForContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.pid")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		finished := make(chan struct{})
		defer func() { <-finished }()
		go func() {
			defer close(finished)
			time.Sleep(10 * time.Millisecond)
			if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
				t.Error(err)
			}
		}()
		if pid := awaitPID(t, path); pid != 12345 {
			t.Fatalf("PID = %d", pid)
		}
	})
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
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for descendant PID at %s", path)
		case <-poll.C:
		}
	}
}
