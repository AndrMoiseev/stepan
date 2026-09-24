package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
)

func TestAcquireNewRunControllerEnforcesPausedAndActiveButAllowsTerminalRuns(t *testing.T) {
	for _, status := range []implstate.RunStatus{implstate.RunActive, implstate.RunPaused} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := newFilesystemWorkspace(t)
			nested := filepath.Join(workCopy, "real", "subdirectory")
			if err := os.MkdirAll(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			mustRecordControllerRun(t, store, "open-run", workCopy, status)
			if _, err := AcquireNewRunControllerWithWorkspace(context.Background(), testfs.Directory(workCopy), store, nested); !errors.Is(err, ErrOpenRun) {
				t.Fatalf("error = %v", err)
			}
			// Failed new-start checking must not retain the process lock.
			lease, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
		})
	}
	for _, status := range []implstate.RunStatus{implstate.RunClosed} {
		t.Run(string(status), func(t *testing.T) {
			store := mustControllerStore(t, t.TempDir())
			workCopy := newFilesystemWorkspace(t)
			mustRecordControllerRun(t, store, "terminal-run", workCopy, status)
			lease, err := AcquireNewRunControllerWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
			if err != nil {
				t.Fatal(err)
			}
			_ = lease.Close()
		})
	}
}

func TestControllerLocksForDifferentWorkingCopiesAreIndependent(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	first, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(""), store, newFilesystemWorkspace(t))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireControllerWithWorkspace(context.Background(), testfs.Directory(""), store, newFilesystemWorkspace(t))
	if err != nil {
		t.Fatalf("second repository lock: %v", err)
	}
	defer second.Close()
}

func TestRecoverOwnRunTurnsOrphanedActiveStateIntoExplicitPause(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	workCopy := newFilesystemWorkspace(t)
	mustRecordControllerRun(t, store, "orphaned-active", workCopy, implstate.RunActive)
	recovered, control, err := RecoverOwnRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if control == nil || recovered.Run.Status != implstate.RunPaused || !strings.Contains(recovered.Run.PauseReason, "interrupted") {
		t.Fatalf("recovered active run = %#v", recovered.Run)
	}
	current, err := FindUnclosedRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, workCopy)
	if err != nil || current.Status != implstate.RunPaused {
		t.Fatalf("recovery did not durably pause interrupted run = %#v, %v", current, err)
	}
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
	workCopy := newFilesystemWorkspace(t)
	nested := filepath.Join(workCopy, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	mustRecordControllerRun(t, store, "run", workCopy, implstate.RunActive)
	state, err := FindUnclosedRunWithWorkspace(context.Background(), testfs.Directory(workCopy), store, nested)
	if err != nil || state == nil {
		t.Fatalf("state = %#v, error = %v", state, err)
	}
}
