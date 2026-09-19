//go:build git_integration || process_integration

package impl_loop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestControllerLockRejectsSecondProcessButAllowsStatusReadAndReleasesOnExit(t *testing.T) {
	storeRoot := t.TempDir()
	workCopy := newGitWorkspace(t)
	nestedWorkCopy := filepath.Join(workCopy, "real", "subdirectory")
	if err := os.MkdirAll(nestedWorkCopy, 0o700); err != nil {
		t.Fatal(err)
	}
	store := mustControllerStore(t, storeRoot)
	mustRecordControllerRun(t, store, "paused-run", workCopy, implstate.RunPaused)
	pausedRun, err := store.Open("paused-run")
	if err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(pausedRun.Path(), runstore.StateDatabaseFileName)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestControllerLockHelper$")
	command.Env = append(os.Environ(), "STEPAN_LOCK_HELPER=1", "STEPAN_LOCK_STORE="+storeRoot, "STEPAN_LOCK_WORK_COPY="+nestedWorkCopy)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("helper did not acquire lock: %q (%v)", scanner.Text(), scanner.Err())
	}
	if _, err := AcquireController(context.Background(), store, workCopy); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("second controller error = %v", err)
	}
	current, err := FindUnclosedRun(context.Background(), store, workCopy)
	if err != nil || current == nil || current.Identity.ID != "paused-run" || current.Status != implstate.RunPaused {
		t.Fatalf("status while locked = %#v, %v", current, err)
	}
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status read rebuilt or changed projection: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	finished = true

	lease, err := AcquireController(context.Background(), store, workCopy)
	if err != nil {
		t.Fatalf("lock after helper exit: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestControllerLockHelper(t *testing.T) {
	if os.Getenv("STEPAN_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := runstore.New(os.Getenv("STEPAN_LOCK_STORE"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := AcquireController(context.Background(), store, os.Getenv("STEPAN_LOCK_WORK_COPY"))
	if err != nil {
		t.Fatal(err)
	}
	_ = lease // os.Exit below deliberately skips Close to exercise OS cleanup.
	fmt.Println("LOCKED")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0)
}

func TestAcquireNewRunControllerEnforcesPausedAndActiveButAllowsTerminalRuns(t *testing.T) {
	for _, status := range []implstate.RunStatus{implstate.RunActive, implstate.RunPaused} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := newGitWorkspace(t)
			nested := filepath.Join(workCopy, "real", "subdirectory")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			mustRecordControllerRun(t, store, "open-run", workCopy, status)
			if _, err := AcquireNewRunController(context.Background(), store, nested); !errors.Is(err, ErrOpenRun) {
				t.Fatalf("error = %v", err)
			}
			// Failed new-start checking must not retain the process lock.
			lease, err := AcquireController(context.Background(), store, workCopy)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
		})
	}
	for _, status := range []implstate.RunStatus{implstate.RunClosed} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := newGitWorkspace(t)
			mustRecordControllerRun(t, store, "terminal-run", workCopy, status)
			lease, err := AcquireNewRunController(context.Background(), store, workCopy)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
		})
	}
}

func TestControllerLocksForDifferentWorkingCopiesAreIndependent(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	first, err := AcquireController(context.Background(), store, newGitWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireController(context.Background(), store, newGitWorkspace(t))
	if err != nil {
		t.Fatalf("second repository lock: %v", err)
	}
	defer second.Close()
}

func TestDiscoverStartupRunKeepsLiveActiveOwnerNonResumable(t *testing.T) {
	storeRoot := t.TempDir()
	workCopy := newGitWorkspace(t)
	store := mustControllerStore(t, storeRoot)
	mustRecordControllerRun(t, store, "active-run", workCopy, implstate.RunActive)

	command := exec.Command(os.Args[0], "-test.run=^TestControllerLockHelper$")
	command.Env = append(os.Environ(), "STEPAN_LOCK_HELPER=1", "STEPAN_LOCK_STORE="+storeRoot, "STEPAN_LOCK_WORK_COPY="+workCopy)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "LOCKED" {
		t.Fatalf("helper did not acquire lock: %q (%v)", scanner.Text(), scanner.Err())
	}
	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.RecoveryRequired || found.Summary.Lifecycle != LifecycleActive {
		t.Fatalf("live owner startup = %#v", found)
	}
	for _, hint := range CommandsForLifecycle(found.Summary.Lifecycle) {
		if hint.Command == CommandResume {
			t.Fatalf("live active owner exposed /resume: %#v", found.Summary)
		}
	}
	if _, _, err := RecoverOwnRun(context.Background(), store, workCopy); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("recovery while a controller owns the run = %v", err)
	}
	current, err := FindUnclosedRun(context.Background(), store, workCopy)
	if err != nil || current.Status != implstate.RunActive {
		t.Fatalf("discovery changed live active state = %#v, %v", current, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverOwnRunTurnsOrphanedActiveStateIntoExplicitPause(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	workCopy := newGitWorkspace(t)
	mustRecordControllerRun(t, store, "orphaned-active", workCopy, implstate.RunActive)
	recovered, control, err := RecoverOwnRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if control == nil || recovered.Run.Status != implstate.RunPaused || !strings.Contains(recovered.Run.PauseReason, "interrupted") {
		t.Fatalf("recovered active run = %#v", recovered.Run)
	}
	current, err := FindUnclosedRun(context.Background(), store, workCopy)
	if err != nil || current.Status != implstate.RunPaused {
		t.Fatalf("recovery did not durably pause interrupted run = %#v, %v", current, err)
	}
}

func onlyControllerLockEntry(t *testing.T, store *runstore.Store) string {
	t.Helper()
	directory := filepath.Join(store.Root(), "controller-locks")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("lock entries = %d, want 1", len(entries))
	}
	return filepath.Join(directory, entries[0].Name())
}

func mustRecordControllerRun(t *testing.T, store *runstore.Store, id implstate.RunID, workCopy string, status implstate.RunStatus) {
	t.Helper()
	run, err := store.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(name implstate.EvidenceID) implstate.EvidenceRef {
		ref, err := run.Publish(name, []byte(name))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	model, err := implstate.NewRun(implstate.RunIdentity{
		ID: id, Change: "change", Repository: workCopy, WorkCopy: workCopy,
		Branch: "implementation", BaselineCommit: "baseline",
		BaselineState: publish("baseline"), Specification: publish("specification"),
		TaskList: publish("tasks"), Configuration: publish("configuration"),
	}, []implstate.Task{{ID: "task", Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case implstate.RunPaused:
		if err := model.Pause("paused"); err != nil {
			t.Fatal(err)
		}
	case implstate.RunClosed:
		if err := model.Close("closed"); err != nil {
			t.Fatal(err)
		}
	}
	state, err := runstore.OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
}

func TestFindUnclosedRunCanonicalizesWorkingCopy(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	workCopy := newGitWorkspace(t)
	nested := filepath.Join(workCopy, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	mustRecordControllerRun(t, store, "run", workCopy, implstate.RunActive)
	state, err := FindUnclosedRun(context.Background(), store, nested)
	if err != nil || state == nil {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}
