package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
)

func TestUserPauseAndClosePreserveCommittedAndUncommittedWork(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	writeWorkspaceFile(t, filepath.Join(repository, "committed.txt"), "committed work\n")

	disk := testfs.New()
	if _, err := disk.Capture(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	committed, err := disk.Commit(context.Background(), repository, "preserved commit")
	if err != nil {
		t.Fatal(err)
	}

	workingPath := filepath.Join(repository, "uncommitted.txt")
	writeWorkspaceFile(t, workingPath, "allowed unfinished work\n")

	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("user-control-git")
	if err != nil {
		t.Fatal(err)
	}
	run, stateStore := newUserControlRun(t, journal, repository)
	defer stateStore.Close()
	control, err := NewUserRunControl(run, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Pause(context.Background(), "user paused"); err != nil {
		t.Fatal(err)
	}
	if err := control.Close(context.Background(), "user closed"); err != nil {
		t.Fatal(err)
	}

	observed, err := disk.Observe(context.Background(), repository)
	if err != nil || observed.CommitID != committed.CommitID {
		t.Fatalf("pause/close changed commit: %#v, %v", observed, err)
	}

	contents, err := os.ReadFile(workingPath)
	if err != nil || string(contents) != "allowed unfinished work\n" {
		t.Fatalf("pause/close removed uncommitted work: %q, %v", contents, err)
	}

	persisted, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil || persisted.Status != implstate.RunClosed || persisted.Status == implstate.RunSucceeded {
		t.Fatalf("terminal result was not a durable non-success close: %#v, %v", persisted, err)
	}
}

func newUserControlRun(t *testing.T, journal *runstore.Run, repository string) (*implstate.Run, *runstore.StateStore) {
	t.Helper()
	publish := func(id implstate.EvidenceID) implstate.EvidenceRef {
		ref, err := journal.Publish(id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	run, err := implstate.NewRun(implstate.RunIdentity{
		ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "implementation", BaselineCommit: "base",
		BaselineState: publish("baseline"), Specification: publish("specification"), TaskList: publish("tasks"), Configuration: publish("configuration"),
	}, []implstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	return run, stateStore
}
