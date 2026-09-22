//go:build git_integration

package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
)

func writeInitialOpenSpecPackage(t *testing.T, repository, change string) {
	t.Helper()
	for name, contents := range map[string]string{
		filepath.Join("openspec", "changes", change, "proposal.md"): "proposal\n",
		filepath.Join("openspec", "changes", change, "design.md"):   "design\n",
		filepath.Join("openspec", "changes", change, "tasks.md"):    "- [ ] source task\n",
		filepath.Join("openspec", "specs", ".gitkeep"):              "",
	} {
		path := filepath.Join(repository, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	changeSpecs := filepath.Join(repository, "openspec", "changes", change, "specs")
	if err := os.MkdirAll(changeSpecs, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestBeginNewChangeRejectsPreviouslyClosedRunAndLoadsOnlyFreshChange(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	repository := newGitWorkspace(t)
	writeInitialOpenSpecPackage(t, repository, "fresh")

	start, err := BeginNewChange(context.Background(), store, repository, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if start.Package.Change != "fresh" || start.Package.Tasks.Content != "- [ ] source task\n" {
		t.Fatalf("start package = %#v", start.Package)
	}
	if err := start.Lease.Close(); err != nil {
		t.Fatal(err)
	}

	mustRecordControllerRun(t, store, "closed-run", repository, implstate.RunClosed)
	if _, err := BeginNewChange(context.Background(), store, repository, "change"); !errors.Is(err, ErrChangeAlreadyStarted) {
		t.Fatalf("closed change start error = %v, want ErrChangeAlreadyStarted", err)
	}
}

func TestContinueOwnRunUsesOnlyCurrentWorkingCopyAndNeverClosedRun(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	ownerRepository := newGitWorkspace(t)
	otherRepository := newGitWorkspace(t)
	mustRecordControllerRun(t, store, "paused-run", ownerRepository, implstate.RunPaused)
	mustRecordControllerRun(t, store, "closed-run", otherRepository, implstate.RunClosed)

	resumed, err := ContinueOwnRun(context.Background(), store, ownerRepository)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Run.Identity.ID != "paused-run" || resumed.Run.Status != implstate.RunPaused || resumed.Journal.ID() != "paused-run" {
		t.Fatalf("resumed run = %#v", resumed.Run)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ContinueOwnRun(context.Background(), store, otherRepository); !errors.Is(err, ErrNoResumableRun) {
		t.Fatalf("closed run continuation error = %v, want ErrNoResumableRun", err)
	}
}

func TestForeignUnavailableRunsDoNotBlockCurrentWorkCopy(t *testing.T) {
	store := mustControllerStore(t, t.TempDir())
	current := newGitWorkspace(t)
	foreign := newGitWorkspace(t)
	mustRecordControllerRun(t, store, "foreign-closed", foreign, implstate.RunClosed)
	mustRecordControllerRun(t, store, "foreign-open", foreign, implstate.RunPaused)
	if err := os.RemoveAll(foreign); err != nil {
		t.Fatal(err)
	}
	writeInitialOpenSpecPackage(t, current, "fresh")
	start, err := BeginNewChange(context.Background(), store, current, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := start.Lease.Close(); err != nil {
		t.Fatal(err)
	}
}
