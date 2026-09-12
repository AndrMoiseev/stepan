package runstore

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestStateStoreRecordsSequentialJournalAndReopensCurrentProjection(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	model := newStoredModel(t, run)
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
	model := newStoredModel(t, run)
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

	model := newStoredModel(t, run)
	injected := errors.New("rollback transaction")
	replaceBeforeProjectionCommitHook(t, func(implementationstate.Event) error { return injected })
	_, err = state.Record(context.Background(), model)
	if !errors.Is(err, injected) {
		t.Fatalf("Record() error = %v, want injected failure", err)
	}
	var count int
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("applied events after rollback = %d, want 0", count)
	}

	beforeProjectionCommitHook = nil
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("applied events after retry = %d, want 1", count)
	}
	_, sequence, err := state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 {
		t.Fatalf("current sequence = %d, want 1", sequence)
	}
}

func TestStateStoreFailedRecordReturnsDefensiveCanonicalEvent(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	replaceBeforeProjectionCommitHook(t, func(implementationstate.Event) error {
		return errors.New("projection failure")
	})
	model := newStoredModel(t, run)
	failed, err := state.Record(context.Background(), model)
	if err == nil {
		t.Fatal("Record() error = nil, want injected failure")
	}
	if err := failed.State.Pause("mutated returned event"); err != nil {
		t.Fatal(err)
	}
	beforeProjectionCommitHook = nil
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	current, _, err := state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != implementationstate.RunActive {
		t.Fatalf("current state was affected by returned event mutation: %#v", current)
	}
}

func TestStateStoreTwoHandlesRefreshSequenceForIdenticalAndDifferingStates(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*implementationstate.Run) error
	}{
		{name: "identical", mutate: func(*implementationstate.Run) error { return nil }},
		{name: "differing", mutate: func(run *implementationstate.Run) error { return run.Pause("second handle") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newStoredRun(t)
			firstStore, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer firstStore.Close()
			secondStore, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer secondStore.Close()

			firstModel := newStoredModel(t, run)
			secondModel := newStoredModel(t, run)
			if err := test.mutate(secondModel); err != nil {
				t.Fatal(err)
			}
			first, err := firstStore.Record(context.Background(), firstModel)
			if err != nil {
				t.Fatal(err)
			}
			second, err := secondStore.Record(context.Background(), secondModel)
			if err != nil {
				t.Fatal(err)
			}
			if first.Sequence != 1 || second.Sequence != 2 {
				t.Fatalf("two-handle sequences = %d, %d; want 1, 2", first.Sequence, second.Sequence)
			}
		})
	}
}

func TestStateStoreSerializesConcurrentHandleWrites(t *testing.T) {
	run := newStoredRun(t)
	firstStore, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.Close()
	secondStore, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	replaceBeforeJournalAppendHook(t, func() {
		once.Do(func() {
			close(started)
			<-release
		})
	})
	firstModel := newStoredModel(t, run)
	secondModel := newStoredModel(t, run)
	firstResult := make(chan recordResult, 1)
	secondResult := make(chan recordResult, 1)
	go func() {
		event, err := firstStore.Record(context.Background(), firstModel)
		firstResult <- recordResult{event: event, err: err}
	}()
	<-started
	go func() {
		event, err := secondStore.Record(context.Background(), secondModel)
		secondResult <- recordResult{event: event, err: err}
	}()
	select {
	case result := <-secondResult:
		t.Fatalf("second writer completed before first journal append: %#v", result)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	first := <-firstResult
	second := <-secondResult
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent records = %#v, %#v", first, second)
	}
	if first.event.Sequence != 1 || second.event.Sequence != 2 {
		t.Fatalf("concurrent sequences = %d, %d; want 1, 2", first.event.Sequence, second.event.Sequence)
	}
}

type recordResult struct {
	event implementationstate.Event
	err   error
}

func TestStateStoreRejectsWrongOrUnavailableEvidenceBeforeJournalAppend(t *testing.T) {
	t.Run("wrong run", func(t *testing.T) {
		source := newStoredRun(t)
		model := newStoredModel(t, source)
		store, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		target, err := store.Create("run-2")
		if err != nil {
			t.Fatal(err)
		}
		state, err := OpenState(target)
		if err != nil {
			t.Fatal(err)
		}
		defer state.Close()
		if _, err := state.Record(context.Background(), model); !errors.Is(err, ErrRunIdentity) {
			t.Fatalf("Record() error = %v, want wrong run", err)
		}
		assertJournalEmpty(t, state)
	})

	t.Run("missing identity reference", func(t *testing.T) {
		run := newStoredRun(t)
		state, err := OpenState(run)
		if err != nil {
			t.Fatal(err)
		}
		defer state.Close()
		model := newStoredModel(t, run)
		model.Identity.BaselineState = implementationstate.EvidenceRef{ID: "missing", Digest: strings.Repeat("0", 64)}
		if _, err := state.Record(context.Background(), model); !errors.Is(err, ErrReferenceUnavailable) {
			t.Fatalf("Record() error = %v, want unavailable evidence", err)
		}
		assertJournalEmpty(t, state)
	})

	t.Run("altered nested result evidence", func(t *testing.T) {
		run := newStoredRun(t)
		state, err := OpenState(run)
		if err != nil {
			t.Fatal(err)
		}
		defer state.Close()
		model := newStoredModel(t, run)
		document := publishTestReference(t, run, "brief")
		nested := publishTestReference(t, run, "nested-result")
		if err := model.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
			t.Fatal(err)
		}
		if err := model.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: document}); err != nil {
			t.Fatal(err)
		}
		basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
		if err := model.AddOperation("assignment", implementationstate.Operation{ID: "operation", Kind: implementationstate.OperationCheck, BriefID: "brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
		if err := model.AddResult("assignment", implementationstate.OperationResult{ID: "result", OperationID: "operation", Status: implementationstate.ResultSucceeded, State: model.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{nested}}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(run.filePath(nested.ID), []byte("altered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Record(context.Background(), model); !errors.Is(err, ErrReferenceIntegrity) {
			t.Fatalf("Record() error = %v, want altered nested evidence", err)
		}
		assertJournalEmpty(t, state)
	})
}

func assertJournalEmpty(t *testing.T, state *StateStore) {
	t.Helper()
	sequence, err := journalLastSequence(state.JournalPath())
	if err != nil || sequence != 0 {
		t.Fatalf("journal after rejected record = sequence %d, error %v", sequence, err)
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

func newStoredModel(t *testing.T, run *Run) *implementationstate.Run {
	t.Helper()
	baseline := publishTestReference(t, run, "baseline")
	specification := publishTestReference(t, run, "specification")
	tasks := publishTestReference(t, run, "tasks")
	configuration := publishTestReference(t, run, "configuration")
	model, err := implementationstate.NewRun(implementationstate.RunIdentity{
		ID:             "run-1",
		Change:         "change",
		Repository:     "/repository",
		WorkCopy:       "/repository",
		Branch:         "feature",
		BaselineCommit: "base",
		BaselineState:  baseline,
		Specification:  specification,
		TaskList:       tasks,
		Configuration:  configuration,
	}, []implementationstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func publishTestReference(t *testing.T, run *Run, id implementationstate.EvidenceID) implementationstate.EvidenceRef {
	t.Helper()
	reference, err := run.Publish(id, []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	return reference
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

func replaceBeforeJournalAppendHook(t *testing.T, replacement func()) {
	t.Helper()
	stateStoreTestHookMu.Lock()
	original := beforeJournalAppendHook
	beforeJournalAppendHook = replacement
	t.Cleanup(func() {
		beforeJournalAppendHook = original
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
