package runstore

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestStateStoreRecordsSequentialJournalAndReopensCurrentProjection(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	model := newStoredModel(t)
	first, err := state.Record(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", first.Sequence)
	}
	if err := model.Pause("waiting for input"); err != nil {
		t.Fatal(err)
	}
	second, err := state.Record(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second sequence = %d, want 2", second.Sequence)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, sequence, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || current.Status != implementationstate.RunPaused || current.PauseReason != "waiting for input" {
		t.Fatalf("reopened projection = sequence %d, state %#v", sequence, current)
	}
	journalSequence, err := journalLastSequence(reopened.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if journalSequence != 2 {
		t.Fatalf("journal last sequence = %d, want 2", journalSequence)
	}
}

func TestStateStoreSyncsJournalBeforeProjectionAndRetriesPendingEventOnce(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	injected := errors.New("injected projection failure")
	replaceBeforeProjectionCommitHook(t, func(implementationstate.Event) error { return injected })
	model := newStoredModel(t)
	event, err := state.Record(context.Background(), model)
	if !errors.Is(err, injected) {
		t.Fatalf("Record() error = %v, want injected failure", err)
	}
	if event.Sequence != 1 {
		t.Fatalf("failed event sequence = %d, want 1", event.Sequence)
	}
	if sequence, journalErr := journalLastSequence(state.JournalPath()); journalErr != nil || sequence != 1 {
		t.Fatalf("journal after projection failure = sequence %d, error %v", sequence, journalErr)
	}
	if _, _, err := state.Current(context.Background()); !errors.Is(err, ErrCurrentStateUnavailable) {
		t.Fatalf("Current() after rolled-back projection error = %v, want unavailable", err)
	}

	beforeProjectionCommitHook = nil
	retried, err := state.Record(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Sequence != event.Sequence {
		t.Fatalf("retried event sequence = %d, want %d", retried.Sequence, event.Sequence)
	}
	if sequence, journalErr := journalLastSequence(state.JournalPath()); journalErr != nil || sequence != 1 {
		t.Fatalf("journal after retry = sequence %d, error %v", sequence, journalErr)
	}
}

func TestStateStoreAppliesIdenticalEventOnceAndRollsBackFailedTransaction(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	model := newStoredModel(t)
	event, err := implementationstate.NewRunStateEvent(1, model)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("rollback transaction")
	replaceBeforeProjectionCommitHook(t, func(implementationstate.Event) error { return injected })
	if err := state.ApplyJournalEvent(context.Background(), event); !errors.Is(err, injected) {
		t.Fatalf("ApplyJournalEvent() error = %v, want injected failure", err)
	}
	var count int
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("applied events after rollback = %d, want 0", count)
	}

	beforeProjectionCommitHook = nil
	if err := state.ApplyJournalEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := state.ApplyJournalEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("applied events after idempotent replay = %d, want 1", count)
	}
	_, sequence, err := state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 {
		t.Fatalf("current sequence = %d, want 1", sequence)
	}
}

func newStoredRun(t *testing.T) *Run {
	t.Helper()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func newStoredModel(t *testing.T) *implementationstate.Run {
	t.Helper()
	model, err := implementationstate.NewRun(implementationstate.RunIdentity{
		ID:             "run-1",
		Change:         "change",
		Repository:     "/repository",
		WorkCopy:       "/repository",
		Branch:         "feature",
		BaselineCommit: "base",
		BaselineState:  implementationstate.EvidenceRef{ID: "baseline", Digest: "baseline-digest"},
		Specification:  implementationstate.EvidenceRef{ID: "specification", Digest: "specification-digest"},
		TaskList:       implementationstate.EvidenceRef{ID: "tasks", Digest: "tasks-digest"},
		Configuration:  implementationstate.EvidenceRef{ID: "configuration", Digest: "configuration-digest"},
	}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

var stateStoreTestHookMu sync.Mutex

func replaceBeforeProjectionCommitHook(t *testing.T, replacement func(implementationstate.Event) error) {
	t.Helper()
	stateStoreTestHookMu.Lock()
	original := beforeProjectionCommitHook
	beforeProjectionCommitHook = replacement
	t.Cleanup(func() {
		beforeProjectionCommitHook = original
		stateStoreTestHookMu.Unlock()
	})
}

func TestStateStoreFilesStayInRunDirectory(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := os.Stat(state.DatabasePath()); err != nil {
		t.Fatalf("database path was not created: %v", err)
	}
}
