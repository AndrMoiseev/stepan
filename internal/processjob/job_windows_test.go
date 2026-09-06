//go:build windows

package processjob

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

func TestCloseKillsProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pids.json")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GO_WANT_PROCESSJOB_HELPER=tree-parent", "STEPAN_HELPER_PID_FILE="+pidFile)
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if err := job.Prepare(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("suspended child ran before Job assignment: %v", err)
	}
	if err := job.Assign(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}

	var data []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(pidFile)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	if err := json.Unmarshal(data, &pids); err != nil || len(pids) != 3 {
		t.Fatalf("pids = %v, %v", pids, err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal("second close:", err)
	}
	_ = cmd.Wait()
	for _, pid := range pids {
		deadline := time.Now().Add(2 * time.Second)
		for {
			alive, err := processAlive(pid)
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

func TestAssignFailureLeavesSuspendedChildSafeToKill(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GO_WANT_PROCESSJOB_HELPER=write-marker", "STEPAN_HELPER_MARKER="+marker)
	job, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := job.Prepare(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := job.Close(); err != nil {
		t.Fatal(err)
	}
	if err := job.Assign(cmd.Process); err == nil {
		t.Fatal("assign to closed Job unexpectedly succeeded")
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("suspended child ran during failed assignment cleanup: %v", err)
	}
}

func TestMain(m *testing.M) {
	switch os.Getenv("GO_WANT_PROCESSJOB_HELPER") {
	case "":
		os.Exit(m.Run())
	case "tree-parent":
		time.Sleep(50 * time.Millisecond)
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_WANT_PROCESSJOB_HELPER=tree-child")
		if err := cmd.Start(); err != nil {
			os.Exit(11)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("STEPAN_HELPER_PID_FILE")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(12)
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(30 * time.Second)
	case "tree-child":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), "GO_WANT_PROCESSJOB_HELPER=tree-grandchild")
		if err := cmd.Start(); err != nil {
			os.Exit(13)
		}
		data, err := json.Marshal([]int{os.Getppid(), os.Getpid(), cmd.Process.Pid})
		if err != nil || os.WriteFile(os.Getenv("STEPAN_HELPER_PID_FILE"), data, 0o600) != nil {
			os.Exit(14)
		}
		time.Sleep(30 * time.Second)
	case "tree-grandchild":
		time.Sleep(30 * time.Second)
	case "write-marker":
		if err := os.WriteFile(os.Getenv("STEPAN_HELPER_MARKER"), []byte("started"), 0o600); err != nil {
			os.Exit(15)
		}
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func processAlive(pid int) (bool, error) {
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
