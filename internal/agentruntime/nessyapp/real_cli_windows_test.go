//go:build nessy_real_cli && windows

package nessyapp

import (
	"errors"
	"sort"
	"sync"
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
		t.Fatal("real Nessy process has no native PID")
	}
	root := process.command.Process.Pid
	process.mu.Unlock()

	set, err := snapshotWindowsProcessTree(root)
	if err != nil {
		t.Fatal("snapshot Windows process tree")
	}
	return set
}

func snapshotWindowsProcessTree(root int) ([]int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	parents := make(map[int]int)
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	for {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if !errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				return nil, err
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
	sort.Ints(set)
	return set, nil
}

func startRealCLIPlatformMonitor(t *testing.T, process *Process) func() []int {
	t.Helper()
	process.mu.Lock()
	if process.command == nil || process.command.Process == nil {
		process.mu.Unlock()
		t.Fatal("real Nessy process has no native PID")
	}
	root := process.command.Process.Pid
	process.mu.Unlock()
	stop := make(chan struct{})
	done := make(chan struct{})
	seen := map[int]struct{}{root: {}}
	var mu sync.Mutex
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if set, err := snapshotWindowsProcessTree(root); err == nil {
				mu.Lock()
				for _, pid := range set {
					seen[pid] = struct{}{}
				}
				mu.Unlock()
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	stopMonitor := func() []int {
		once.Do(func() { close(stop) })
		<-done
		mu.Lock()
		defer mu.Unlock()
		result := make([]int, 0, len(seen))
		for pid := range seen {
			result = append(result, pid)
		}
		sort.Ints(result)
		return result
	}
	t.Cleanup(func() { _ = stopMonitor() })
	return stopMonitor
}

func waitRealCLIPlatformStopped(t *testing.T, processes []int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for _, pid := range processes {
		for {
			alive, err := nessyProcessAlive(pid)
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
	alive, err := nessyProcessAlive(processes[0])
	if err != nil || !alive {
		t.Fatal("independent Windows thread did not survive sibling close")
	}
}
