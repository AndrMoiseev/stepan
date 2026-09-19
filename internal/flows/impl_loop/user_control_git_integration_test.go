//go:build git_integration

package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestUserPauseAndClosePreserveCommittedAndUncommittedGitWork(t *testing.T) {
	repository := newGitWorkspace(t)
	gitFixture(t, repository, "checkout", "-b", "implementation")
	writeGitWorkspaceFile(t, filepath.Join(repository, "committed.txt"), "committed work\n")
	gitFixture(t, repository, "add", "--", "committed.txt")
	gitFixture(t, repository, "commit", "--quiet", "-m", "preserved commit")
	head := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD"))
	workingPath := filepath.Join(repository, "uncommitted.txt")
	writeGitWorkspaceFile(t, workingPath, "allowed unfinished work\n")

	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("user-control-git")
	if err != nil {
		t.Fatal(err)
	}
	run, stateStore := newUserControlGitRun(t, journal, repository)
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

	if got := strings.TrimSpace(gitFixture(t, repository, "rev-parse", "HEAD")); got != head {
		t.Fatalf("pause/close rewrote committed work: HEAD=%s, want %s", got, head)
	}
	contents, err := os.ReadFile(workingPath)
	if err != nil || string(contents) != "allowed unfinished work\n" {
		t.Fatalf("pause/close removed uncommitted work: %q, %v", contents, err)
	}
	if status := gitFixture(t, repository, "status", "--porcelain=v1"); !strings.Contains(status, "?? uncommitted.txt") {
		t.Fatalf("uncommitted work was not retained: %q", status)
	}
	persisted, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil || persisted.Status != implementationstate.RunClosed || persisted.Status == implementationstate.RunSucceeded {
		t.Fatalf("terminal result was not a durable non-success close: %#v, %v", persisted, err)
	}
}

func newUserControlGitRun(t *testing.T, journal *runstore.Run, repository string) (*implementationstate.Run, *runstore.StateStore) {
	t.Helper()
	publish := func(id implementationstate.EvidenceID) implementationstate.EvidenceRef {
		ref, err := journal.Publish(id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	run, err := implementationstate.NewRun(implementationstate.RunIdentity{
		ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "implementation", BaselineCommit: "base",
		BaselineState: publish("baseline"), Specification: publish("specification"), TaskList: publish("tasks"), Configuration: publish("configuration"),
	}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	return run, stateStore
}
