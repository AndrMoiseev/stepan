//go:build qwen_real_cli && windows

package qwenapp

import (
	"errors"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func realCLIPlatformAssertionName() string {
	return "windows_job_objects_close_interrupt_and_no_descendants"
}

func realCLIPlatformProcessSet(t *testing.T, process *Process) []int {
	t.Helper()
	process.mu.Lock()
	if process.command == nil || process.command.Process == nil {
		process.mu.Unlock()
		t.Fatal("real Qwen process has no native PID")
	}
	root := process.command.Process.Pid
	process.mu.Unlock()

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal("snapshot Windows process tree")
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	parents := make(map[int]int)
	if err := windows.Process32First(snapshot, &entry); err != nil {
		t.Fatal("read Windows process snapshot")
	}
	for {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				t.Fatal("read Windows process snapshot")
			}
			break
		}
	}
	set := []int{root}
	known := map[int]struct{}{root: {}}
	for changed := true; changed; {
		changed = false
		for pid, parent := range parents {
			if _, included := known[pid]; included {
				continue
			}
			if _, included := known[parent]; included {
				known[pid] = struct{}{}
				set = append(set, pid)
				changed = true
			}
		}
	}
	return set
}

func waitRealCLIPlatformStopped(t *testing.T, processes []int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for _, pid := range processes {
		for {
			alive, err := qwenProcessAlive(pid)
			if err != nil {
				t.Fatal("inspect Windows process after containment close")
			}
			if !alive {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Windows Job Object left a process alive")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func assertRealCLIPlatformRunning(t *testing.T, processes []int) {
	t.Helper()
	if len(processes) == 0 {
		t.Fatal("Windows process set is empty")
	}
	alive, err := qwenProcessAlive(processes[0])
	if err != nil || !alive {
		t.Fatal("independent Windows thread did not survive sibling close")
	}
}
