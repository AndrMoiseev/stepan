package impl_loop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestControllerLockRejectsSecondProcessButAllowsStatusReadAndReleasesOnExit(t *testing.T) {
	storeRoot := t.TempDir()
	workCopy := t.TempDir()
	store := mustControllerStore(t, storeRoot)
	mustRecordControllerRun(t, store, "paused-run", workCopy, implementationstate.RunPaused)
	pausedRun, err := store.Open("paused-run")
	if err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(pausedRun.Path(), runstore.StateDatabaseFileName)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}

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
	if _, err := AcquireController(store, workCopy); !errors.Is(err, ErrControllerBusy) {
		t.Fatalf("second controller error = %v", err)
	}
	current, err := FindUnclosedRun(context.Background(), store, workCopy)
	if err != nil || current == nil || current.Identity.ID != "paused-run" || current.Status != implementationstate.RunPaused {
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

	lease, err := AcquireController(store, workCopy)
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
	lease, err := AcquireController(store, os.Getenv("STEPAN_LOCK_WORK_COPY"))
	if err != nil {
		t.Fatal(err)
	}
	_ = lease // os.Exit below deliberately skips Close to exercise OS cleanup.
	fmt.Println("LOCKED")
	_, _ = os.Stdin.Read(make([]byte, 1))
	os.Exit(0)
}

func TestAcquireNewRunControllerEnforcesPausedAndActiveButAllowsTerminalRuns(t *testing.T) {
	for _, status := range []implementationstate.RunStatus{implementationstate.RunActive, implementationstate.RunPaused} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := t.TempDir()
			mustRecordControllerRun(t, store, "open-run", workCopy, status)
			if _, err := AcquireNewRunController(context.Background(), store, workCopy); !errors.Is(err, ErrOpenRun) {
				t.Fatalf("error = %v", err)
			}
			// Failed new-start checking must not retain the process lock.
			lease, err := AcquireController(store, workCopy)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
		})
	}
	for _, status := range []implementationstate.RunStatus{implementationstate.RunClosed} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := t.TempDir()
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
	first, err := AcquireController(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireController(store, t.TempDir())
	if err != nil {
		t.Fatalf("second repository lock: %v", err)
	}
	defer second.Close()
}

func mustControllerStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustRecordControllerRun(t *testing.T, store *runstore.Store, id implementationstate.RunID, workCopy string, status implementationstate.RunStatus) {
	t.Helper()
	run, err := store.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(name implementationstate.EvidenceID) implementationstate.EvidenceRef {
		ref, err := run.Publish(name, []byte(name))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	model, err := implementationstate.NewRun(implementationstate.RunIdentity{
		ID: id, Change: "change", Repository: workCopy, WorkCopy: workCopy,
		Branch: "implementation", BaselineCommit: "baseline",
		BaselineState: publish("baseline"), Specification: publish("specification"),
		TaskList: publish("tasks"), Configuration: publish("configuration"),
	}, []implementationstate.Task{{ID: "task", Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case implementationstate.RunPaused:
		if err := model.Pause("paused"); err != nil {
			t.Fatal(err)
		}
	case implementationstate.RunClosed:
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
	workCopy := t.TempDir()
	mustRecordControllerRun(t, store, "run", workCopy, implementationstate.RunActive)
	state, err := FindUnclosedRun(context.Background(), store, filepath.Join(workCopy, "."))
	if err != nil || state == nil {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}
