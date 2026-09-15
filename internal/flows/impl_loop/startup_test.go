package impl_loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestDiscoverStartupRunFindsPausedWorkCopyWithoutOpeningProjection(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if err := run.AddRunOperation(implementationstate.Operation{
		ID: "initial-check", Kind: implementationstate.OperationCheck,
		Basis:       implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration},
		Description: "run required check lint",
	}); err != nil {
		t.Fatal(err)
	}
	if err := run.Pause("lint failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	projection := filepath.Join(journal.Path(), runstore.StateDatabaseFileName)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.OpenExisting(filepath.Dir(filepath.Dir(journal.Path())))
	if err != nil {
		t.Fatal(err)
	}

	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Run.Identity.ID != run.Identity.ID {
		t.Fatalf("discovered run = %#v, want %q", found, run.Identity.ID)
	}
	if found.Summary.Change != "change" || found.Summary.Stage != "initial required checks" || found.Summary.LastAction != "run required check lint" || found.Summary.StopReason != "lint failed" || found.Summary.Lifecycle != LifecyclePaused {
		t.Fatalf("startup summary = %#v", found.Summary)
	}
	panel := FormatStartupSummary(found.Summary)
	for _, want := range []string{"change: change", "stage: initial required checks", "last action: run required check lint", "stop reason: lint failed", "available: /resume /stop /status"} {
		if !strings.Contains(panel, want) {
			t.Fatalf("startup panel %q does not contain %q", panel, want)
		}
	}
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only startup discovery rebuilt projection: %v", err)
	}
}

func TestDiscoverStartupRunSkipsClosedRunAndTerminalStatuses(t *testing.T) {
	run, state, journal, workCopy := newInitialCheckRun(t)
	if err := run.Close("user stopped"); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.OpenExisting(filepath.Dir(filepath.Dir(journal.Path())))
	if err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverStartupRun(context.Background(), store, workCopy)
	if err != nil || found != nil {
		t.Fatalf("closed discovery = %#v, %v", found, err)
	}
	for _, status := range []implementationstate.RunStatus{implementationstate.RunClosed, implementationstate.RunSucceeded} {
		if isUnclosedStatus(status) {
			t.Fatalf("terminal status %q was selected as unclosed", status)
		}
	}
}
