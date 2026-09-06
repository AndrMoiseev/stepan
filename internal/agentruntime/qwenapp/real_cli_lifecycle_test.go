//go:build qwen_real_cli && (windows || darwin)

package qwenapp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func testRealCLIPlatformLifecycle(t *testing.T, fixture realCLIFixture) {
	schema := constantObjectSchema(map[string]string{"status": "ok"})
	runtime := startRealCLIRuntime(t, fixture, schema)
	first := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema, "Return status ok when asked.")
	second := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactTwo, schema, "Return status ok when asked.")
	firstBackend, secondBackend := realCLIBackend(t, first), realCLIBackend(t, second)
	if firstBackend.process == secondBackend.process || firstBackend.process.job == secondBackend.process.job {
		t.Fatal("two logical threads share a process or containment owner")
	}
	assertRealCLIStartup(t, firstBackend, fixture, fixture.artifactOne)
	assertRealCLIStartup(t, secondBackend, fixture, fixture.artifactTwo)
	if firstBackend.connection.SessionID() == secondBackend.connection.SessionID() {
		t.Fatal("two logical threads share an ACP session identity")
	}
	firstSet := realCLIPlatformProcessSet(t, firstBackend.process)
	secondSet := realCLIPlatformProcessSet(t, secondBackend.process)
	stopFirstMonitor := startRealCLIPlatformMonitor(t, firstBackend.process)
	stopSecondMonitor := startRealCLIPlatformMonitor(t, secondBackend.process)
	if processSetsOverlap(firstSet, secondSet) {
		t.Fatal("two logical threads share a native process tree")
	}
	firstBeforeClose := realCLIDirectorySnapshot(t, fixture.artifactOne)
	if err := runtime.CloseThread(first); err != nil {
		t.Fatal("close first real-CLI thread")
	}
	firstSet = mergeProcessSets(firstSet, stopFirstMonitor())
	waitRealCLIPlatformStopped(t, firstSet, 5*time.Second)
	assertRealCLIPlatformRunning(t, secondSet)
	time.Sleep(500 * time.Millisecond)
	if !equalRealCLISnapshot(firstBeforeClose, realCLIDirectorySnapshot(t, fixture.artifactOne)) {
		t.Fatal("closed thread produced a late artifact write")
	}

	before := realCLIDirectorySnapshot(t, fixture.artifactTwo)
	turnDone := make(chan error, 1)
	go func() {
		_, err := runtime.RunTurn(second, "Keep this turn active by repeatedly using glob until interrupted; do not write any file. If uninterrupted, return status ok.")
		turnDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.mu.Lock()
		active := runtime.active != nil
		runtime.mu.Unlock()
		if active {
			break
		}
		select {
		case <-turnDone:
			t.Fatal("cancel probe completed before the interrupt checkpoint")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel probe did not become active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	secondSet = mergeProcessSets(secondSet, realCLIPlatformProcessSet(t, secondBackend.process))
	interruptStarted := time.Now()
	if err := stopRealCLIRuntimeBounded(runtime); err != nil {
		t.Fatal("interrupt real-CLI runtime")
	}
	secondSet = mergeProcessSets(secondSet, stopSecondMonitor())
	if time.Since(interruptStarted) > interruptGracePeriod+5*time.Second {
		t.Fatal("global interrupt exceeded its bounded grace and cleanup window")
	}
	waitRealCLIPlatformStopped(t, secondSet, 5*time.Second)
	select {
	case err := <-turnDone:
		if err == nil || (!errors.Is(err, agentruntime.ErrTurnInterrupted) && !errors.Is(err, agentruntime.ErrRuntimeClosed)) {
			t.Fatal("active turn did not terminate as interrupted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted turn did not terminate")
	}
	if _, err := runtime.RunTurn(second, "late"); !errors.Is(err, agentruntime.ErrTurnInterrupted) {
		t.Fatal("global interrupt did not invalidate the surviving handle")
	}
	time.Sleep(500 * time.Millisecond)
	if !equalRealCLISnapshot(before, realCLIDirectorySnapshot(t, fixture.artifactTwo)) {
		t.Fatal("artifact root changed after the global interrupt checkpoint")
	}
	testRealCLIGlobalClose(t, fixture, schema)
}

func testRealCLIGlobalClose(t *testing.T, fixture realCLIFixture, schema json.RawMessage) {
	runtime := startRealCLIRuntime(t, fixture, schema)
	first := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema, "Return status ok when asked.")
	second := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactTwo, schema, "Return status ok when asked.")
	firstSet := realCLIPlatformProcessSet(t, realCLIBackend(t, first).process)
	secondSet := realCLIPlatformProcessSet(t, realCLIBackend(t, second).process)
	stopFirstMonitor := startRealCLIPlatformMonitor(t, realCLIBackend(t, first).process)
	stopSecondMonitor := startRealCLIPlatformMonitor(t, realCLIBackend(t, second).process)
	beforeOne := realCLIDirectorySnapshot(t, fixture.artifactOne)
	beforeTwo := realCLIDirectorySnapshot(t, fixture.artifactTwo)
	if err := closeRealCLIRuntimeBounded(runtime); err != nil {
		t.Fatal("bounded global runtime close failed")
	}
	firstSet = mergeProcessSets(firstSet, stopFirstMonitor())
	secondSet = mergeProcessSets(secondSet, stopSecondMonitor())
	waitRealCLIPlatformStopped(t, firstSet, 5*time.Second)
	waitRealCLIPlatformStopped(t, secondSet, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	if !equalRealCLISnapshot(beforeOne, realCLIDirectorySnapshot(t, fixture.artifactOne)) ||
		!equalRealCLISnapshot(beforeTwo, realCLIDirectorySnapshot(t, fixture.artifactTwo)) {
		t.Fatal("global close checkpoint was followed by a late artifact write")
	}
}

func mergeProcessSets(sets ...[]int) []int {
	seen := make(map[int]struct{})
	var result []int
	for _, set := range sets {
		for _, value := range set {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func processSetsOverlap(left, right []int) bool {
	seen := make(map[int]struct{}, len(left))
	for _, value := range left {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; ok {
			return true
		}
	}
	return false
}

type realCLIFileState struct {
	Size    int64
	ModTime int64
}

func realCLIDirectorySnapshot(t *testing.T, root string) map[string]realCLIFileState {
	t.Helper()
	result := make(map[string]realCLIFileState)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = realCLIFileState{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal("snapshot real-CLI artifact root")
	}
	return result
}

func equalRealCLISnapshot(left, right map[string]realCLIFileState) bool {
	if len(left) != len(right) {
		return false
	}
	for name, state := range left {
		if right[name] != state {
			return false
		}
	}
	return true
}
