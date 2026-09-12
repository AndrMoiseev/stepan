package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestStateStoreRechecksEvidenceAfterJournalSyncBeforeProjection(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	original := afterJournalSyncHook
	defer func() { afterJournalSyncHook = original }()

	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := newStoredModel(t, run)
	afterJournalSyncHook = func() error {
		return os.WriteFile(run.filePath(model.Identity.TaskList.ID), []byte("altered after journal sync"), 0o600)
	}

	event, err := state.Record(context.Background(), model)
	if !errors.Is(err, ErrStateReference) || !errors.Is(err, ErrReferenceIntegrity) || event.Sequence != 1 {
		t.Fatalf("Record() = event %#v, error %v; want durable but unapplied reference failure", event, err)
	}
	if state.pending == nil {
		t.Fatal("pending event was cleared after rejected projection")
	}
	if sequence, err := journalLastSequence(state.JournalPath()); err != nil || sequence != 1 {
		t.Fatalf("durable journal after rejected projection = sequence %d, error %v", sequence, err)
	}
	if _, _, err := state.Current(context.Background()); !errors.Is(err, ErrCurrentStateUnavailable) {
		t.Fatalf("Current() after rejected projection = %v, want unavailable", err)
	}
	var count int
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("applied events after rejected projection = %d, error %v", count, err)
	}

	afterJournalSyncHook = nil
	if err := os.WriteFile(run.filePath(model.Identity.TaskList.ID), []byte(model.Identity.TaskList.ID), 0o600); err != nil {
		t.Fatal(err)
	}
	if retried, err := state.Record(context.Background(), model); err != nil || retried.Sequence != 1 {
		t.Fatalf("pending retry after evidence restoration = %#v, error %v", retried, err)
	}
	if sequence, err := journalLastSequence(state.JournalPath()); err != nil || sequence != 1 {
		t.Fatalf("journal after safe retry = sequence %d, error %v", sequence, err)
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

func TestStateStoreReappliesCanonicalJournalEventExactlyOnce(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	if _, err := state.Record(context.Background(), newStoredModel(t, run)); err != nil {
		t.Fatal(err)
	}
	journalData, err := os.ReadFile(state.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	before, beforeSequence, err := state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeData, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}

	state.writer.Lock()
	state.mu.Lock()
	err = state.apply(context.Background(), journalData)
	state.mu.Unlock()
	state.writer.Unlock()
	if err != nil {
		t.Fatalf("reapply canonical journal event: %v", err)
	}
	assertStateStoreProjectionUnchanged(t, state, beforeData, beforeSequence)

	conflicting, err := eventFromData(journalData)
	if err != nil {
		t.Fatal(err)
	}
	if err := conflicting.State.Pause("conflicting same-sequence event"); err != nil {
		t.Fatal(err)
	}
	conflictingData, err := marshalEvent(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	state.writer.Lock()
	state.mu.Lock()
	err = state.apply(context.Background(), conflictingData)
	state.mu.Unlock()
	state.writer.Unlock()
	if !errors.Is(err, ErrJournalSequence) {
		t.Fatalf("apply conflicting event error = %v, want ErrJournalSequence", err)
	}
	assertStateStoreProjectionUnchanged(t, state, beforeData, beforeSequence)
}

func assertStateStoreProjectionUnchanged(t *testing.T, state *StateStore, wantData []byte, wantSequence uint64) {
	t.Helper()
	var count int
	if err := state.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("applied events = %d, want 1", count)
	}
	current, sequence, err := state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence != wantSequence {
		t.Fatalf("current sequence = %d, want %d", sequence, wantSequence)
	}
	currentData, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentData, wantData) {
		t.Fatalf("current projection changed: got %s, want %s", currentData, wantData)
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
		if _, err := model.StartAssignmentAttempt("assignment", "operation"); err != nil {
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

func replaceAfterJournalSyncHook(t *testing.T, replacement func() error) {
	t.Helper()
	stateStoreTestHookMu.Lock()
	original := afterJournalSyncHook
	afterJournalSyncHook = replacement
	t.Cleanup(func() {
		afterJournalSyncHook = original
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

func TestStateStoreRecoversMissingAndCorruptProjectionFromJournal(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{name: "missing", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newStoredRun(t)
			state, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			model := newStoredModel(t, run)
			if _, err := state.Record(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			if err := model.Pause("recover projection"); err != nil {
				t.Fatal(err)
			}
			if _, err := state.Record(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}
			test.damage(t, state.DatabasePath())

			recovered, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			current, sequence, err := recovered.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if sequence != 2 || current.Status != implementationstate.RunPaused {
				t.Fatalf("recovered projection = sequence %d, state %#v", sequence, current)
			}
			if _, err := recovered.Record(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			_, sequence, err = recovered.Current(context.Background())
			if err != nil || sequence != 3 {
				t.Fatalf("continued recovered projection = sequence %d, error %v", sequence, err)
			}
		})
	}
}

func TestStateStoreRecoveryDiscardsOnlyIncompleteJournalTail(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newStoredModel(t, run)
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(state.JournalPath(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"sequence":2`); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(state.DatabasePath()); err != nil {
		t.Fatal(err)
	}

	recovered, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if sequence, err := journalLastSequence(recovered.JournalPath()); err != nil || sequence != 1 {
		t.Fatalf("journal after tail recovery = sequence %d, error %v", sequence, err)
	}
	data, err := os.ReadFile(recovered.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatalf("journal tail was not durably discarded: %q", data)
	}
	if err := model.Pause("continued after tail recovery"); err != nil {
		t.Fatal(err)
	}
	if event, err := recovered.Record(context.Background(), model); err != nil || event.Sequence != 2 {
		t.Fatalf("continued record = %#v, error %v", event, err)
	}
}

func TestStateStoreRecoveryRejectsMiddleJournalCorruptionAndMissingEvidence(t *testing.T) {
	t.Run("complete malformed record", func(t *testing.T) {
		run := newStoredRun(t)
		state, err := OpenState(run)
		if err != nil {
			t.Fatal(err)
		}
		model := newStoredModel(t, run)
		if _, err := state.Record(context.Background(), model); err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(state.JournalPath(), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("not json\n"); err != nil {
			t.Fatal(err)
		}
		if err := model.Pause("event after corruption"); err != nil {
			t.Fatal(err)
		}
		event, err := implementationstate.NewRunStateEvent(2, model)
		if err != nil {
			t.Fatal(err)
		}
		data, err := marshalEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(state.DatabasePath()); err != nil {
			t.Fatal(err)
		}
		_, err = OpenState(run)
		if !errors.Is(err, ErrJournalSequence) || !strings.Contains(err.Error(), "byte") {
			t.Fatalf("OpenState() error = %v, want actionable journal corruption", err)
		}
	})

	t.Run("altered related file", func(t *testing.T) {
		run := newStoredRun(t)
		state, err := OpenState(run)
		if err != nil {
			t.Fatal(err)
		}
		model := newStoredModel(t, run)
		if _, err := state.Record(context.Background(), model); err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(run.filePath(model.Identity.TaskList.ID), []byte("altered"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(state.DatabasePath()); err != nil {
			t.Fatal(err)
		}
		_, err = OpenState(run)
		if !errors.Is(err, ErrStateReference) || !errors.Is(err, ErrReferenceIntegrity) {
			t.Fatalf("OpenState() error = %v, want altered related-file diagnostic", err)
		}
	})

	t.Run("missing related file", func(t *testing.T) {
		run := newStoredRun(t)
		state, err := OpenState(run)
		if err != nil {
			t.Fatal(err)
		}
		model := newStoredModel(t, run)
		if _, err := state.Record(context.Background(), model); err != nil {
			t.Fatal(err)
		}
		if err := state.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(run.filePath(model.Identity.TaskList.ID)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(state.DatabasePath()); err != nil {
			t.Fatal(err)
		}
		_, err = OpenState(run)
		if !errors.Is(err, ErrStateReference) || !errors.Is(err, ErrReferenceUnavailable) {
			t.Fatalf("OpenState() error = %v, want missing related-file diagnostic", err)
		}
	})
}

func TestStateStoreRecoveryReplaysEachDurableFailureBoundaryOnce(t *testing.T) {
	for _, test := range []struct {
		name    string
		install func(t *testing.T)
	}{
		{name: "before journal fsync", install: func(t *testing.T) { beforeJournalSyncHook = func() error { return errors.New("before journal fsync") } }},
		{name: "after journal fsync", install: func(t *testing.T) { afterJournalSyncHook = func() error { return errors.New("after journal fsync") } }},
		{name: "before sqlite transaction", install: func(t *testing.T) {
			beforeProjectionTransactionHook = func(implementationstate.Event) error { return errors.New("before sqlite transaction") }
		}},
		{name: "after sqlite transaction", install: func(t *testing.T) {
			afterProjectionTransactionHook = func(implementationstate.Event) error { return errors.New("after sqlite transaction") }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateStoreTestHookMu.Lock()
			defer stateStoreTestHookMu.Unlock()
			beforeJournalSyncHook, afterJournalSyncHook = nil, nil
			beforeProjectionTransactionHook, afterProjectionTransactionHook = nil, nil
			t.Cleanup(func() {
				beforeJournalSyncHook, afterJournalSyncHook = nil, nil
				beforeProjectionTransactionHook, afterProjectionTransactionHook = nil, nil
			})

			run := newStoredRun(t)
			state, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			test.install(t)
			model := newStoredModel(t, run)
			if _, err := state.Record(context.Background(), model); err == nil {
				t.Fatal("Record() error = nil, want injected failure")
			}
			beforeJournalSyncHook, afterJournalSyncHook = nil, nil
			beforeProjectionTransactionHook, afterProjectionTransactionHook = nil, nil
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}

			recovered, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			_, sequence, err := recovered.Current(context.Background())
			if err != nil || sequence != 1 {
				t.Fatalf("recovered sequence = %d, error %v", sequence, err)
			}
			var count int
			if err := recovered.db.QueryRow("SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil || count != 1 {
				t.Fatalf("applied durable event count = %d, error %v", count, err)
			}
		})
	}
}

func TestStateStoreKeepsExistingProjectionUntilReplacementPublishes(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	original := publishReplacementProjection
	t.Cleanup(func() { publishReplacementProjection = original })

	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newStoredModel(t, run)
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := model.Pause("durable but not projected"); err != nil {
		t.Fatal(err)
	}
	event, err := implementationstate.NewRunStateEvent(2, model)
	if err != nil {
		t.Fatal(err)
	}
	data, err := marshalEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendJournal(state.JournalPath(), data); err != nil {
		t.Fatal(err)
	}
	publishReplacementProjection = func(string, string) error { return errors.New("replacement publication failed") }
	if _, err := OpenState(run); err == nil {
		t.Fatal("OpenState() error = nil, want replacement publication failure")
	}

	db, err := openProjection(state.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	var sequence int
	if err := db.QueryRow("SELECT last_applied_sequence FROM current_state WHERE id = 1").Scan(&sequence); err != nil || sequence != 1 {
		t.Fatalf("last usable projection = sequence %d, error %v", sequence, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	publishReplacementProjection = original
	recovered, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	_, sequence64, err := recovered.Current(context.Background())
	if err != nil || sequence64 != 2 {
		t.Fatalf("published replacement = sequence %d, error %v", sequence64, err)
	}
}

func TestPublishProjectionGroupRestoresSidecarsWhenMainPublicationFails(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	original := publishReplacementProjection
	defer func() { publishReplacementProjection = original }()

	directory := t.TempDir()
	temporary := filepath.Join(directory, "replacement.sqlite")
	target := filepath.Join(directory, StateDatabaseFileName)
	if err := os.WriteFile(temporary, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range projectionSidecars(target) {
		if err := os.WriteFile(sidecar, []byte(filepath.Base(sidecar)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	publishReplacementProjection = func(string, string) error { return errors.New("main replacement failed") }
	if err := publishProjectionGroup(temporary, target); err == nil {
		t.Fatal("publishProjectionGroup() error = nil, want publication failure")
	}
	if previous, err := os.ReadFile(target); err != nil || !bytes.Equal(previous, []byte("previous")) {
		t.Fatalf("main file after failed replacement = %q, error %v", previous, err)
	}
	for _, sidecar := range projectionSidecars(target) {
		if restored, err := os.ReadFile(sidecar); err != nil || !bytes.Equal(restored, []byte(filepath.Base(sidecar))) {
			t.Fatalf("sidecar %s after failed replacement = %q, error %v", filepath.Base(sidecar), restored, err)
		}
	}
}

func TestStateStoreRecoveryRechecksEvidenceImmediatelyBeforeReplay(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	original := beforeRecoveryReplayHook
	defer func() { beforeRecoveryReplayHook = original }()

	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newStoredModel(t, run)
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	originalProjection := []byte("corrupt projection retained on failed recovery")
	if err := os.WriteFile(state.DatabasePath(), originalProjection, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeRecoveryReplayHook = func() error {
		return os.WriteFile(run.filePath(model.Identity.TaskList.ID), []byte("altered after validation"), 0o600)
	}
	_, err = OpenState(run)
	if !errors.Is(err, ErrStateReference) || !errors.Is(err, ErrReferenceIntegrity) {
		t.Fatalf("OpenState() error = %v, want replay-time reference integrity failure", err)
	}
	if retained, err := os.ReadFile(state.DatabasePath()); err != nil || !bytes.Equal(retained, originalProjection) {
		t.Fatalf("projection after failed replay = %q, error %v; want no publication", retained, err)
	}
}

func TestRecordAssignmentAttemptStartPersistsAmbiguousStartBeforeRetry(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newStoredModel(t, run)
	document := publishTestReference(t, run, "brief")
	basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
	if err := model.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: document}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddOperation("assignment", implementationstate.Operation{ID: "review", Kind: implementationstate.OperationReview, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterAssignmentReview}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}

	first, event, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "review")
	if err != nil {
		t.Fatal(err)
	}
	if first != (implementationstate.OperationAttempt{Number: 1, SemanticRound: 1}) || event.Sequence != 2 {
		t.Fatalf("durable first attempt = %#v, event %d", first, event.Sequence)
	}
	// Deliberately do not add a result: this represents a process death after
	// the durable start and before it can establish whether dispatch occurred.
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, sequence, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || restarted.Assignments[0].Counters.AssignmentReview != 1 || len(restarted.Assignments[0].Operations[0].Attempts) != 1 {
		t.Fatalf("ambiguous start was not retained: sequence=%d assignment=%#v", sequence, restarted.Assignments[0])
	}
	second, event, err := reopened.RecordAssignmentAttemptStart(context.Background(), restarted, "assignment", "review")
	if err != nil {
		t.Fatal(err)
	}
	if second != (implementationstate.OperationAttempt{Number: 2, SemanticRound: 1}) || event.Sequence != 3 {
		t.Fatalf("technical retry = %#v, event %d; want same semantic round", second, event.Sequence)
	}
}

func TestRecordLimitedAttemptPersistsPauseBeforeAnExceededRound(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	model := attemptStartModel(t, run)
	basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"review-1", "review-2"} {
		if err := model.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationReview, BriefID: "attempt-brief", Basis: basis, Counter: implementationstate.CycleCounterAssignmentReview}); err != nil {
			t.Fatal(err)
		}
	}
	limits := implementationstate.CycleLimits{AssignmentReview: 1, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 10, TechnicalAttempts: 3, FinalReview: 3}
	if _, event, err := state.RecordAssignmentAttemptStartWithLimits(context.Background(), model, "assignment", "review-1", limits); err != nil || event.Sequence != 1 {
		t.Fatalf("last permitted start = event %#v, error %v", event, err)
	}
	if attempt, event, err := state.RecordAssignmentAttemptStartWithLimits(context.Background(), model, "assignment", "review-2", limits); !errors.Is(err, implementationstate.ErrLimitExceeded) || attempt != (implementationstate.OperationAttempt{}) || event.Sequence != 2 {
		t.Fatalf("exceeded start = attempt %#v, event %#v, error %v", attempt, event, err)
	}
	if model.Status != implementationstate.RunPaused || model.LimitPause == nil || len(model.Assignments[0].Operations[2].Attempts) != 0 {
		t.Fatalf("caller did not retain durable pre-dispatch pause: %#v", model)
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
	if err != nil || sequence != 2 || current.Status != implementationstate.RunPaused || current.LimitPause == nil {
		t.Fatalf("reopened limit pause = state %#v, sequence %d, error %v", current, sequence, err)
	}
}

func TestRecordExplorerLimitPauseResetsOnlyItsEpisodeOnResume(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := attemptStartModel(t, run)
	basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
	for _, operation := range []implementationstate.Operation{
		{ID: "explorer-review", Kind: implementationstate.OperationAgent, BriefID: "attempt-brief", Basis: basis, Counter: implementationstate.CycleCounterExplorer, Episode: "review"},
		{ID: "explorer-implementation-1", Kind: implementationstate.OperationAgent, BriefID: "attempt-brief", Basis: basis, Counter: implementationstate.CycleCounterExplorer, Episode: "implementation"},
		{ID: "explorer-implementation-2", Kind: implementationstate.OperationAgent, BriefID: "attempt-brief", Basis: basis, Counter: implementationstate.CycleCounterExplorer, Episode: "implementation"},
	} {
		if err := model.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
	}
	limits := implementationstate.CycleLimits{AssignmentReview: 3, MandatoryChecks: 3, ChecksRequested: 5, BriefRefinement: 3, Explorer: 1, TechnicalAttempts: 3, FinalReview: 3}
	for _, operationID := range []implementationstate.OperationID{"explorer-review", "explorer-implementation-1"} {
		if _, event, err := state.RecordAssignmentAttemptStartWithLimits(context.Background(), model, "assignment", operationID, limits); err != nil || event.Sequence == 0 {
			t.Fatalf("permitted explorer %s = event %#v, error %v", operationID, event, err)
		}
	}
	if _, event, err := state.RecordAssignmentAttemptStartWithLimits(context.Background(), model, "assignment", "explorer-implementation-2", limits); !errors.Is(err, implementationstate.ErrLimitExceeded) || event.Sequence == 0 {
		t.Fatalf("exceeded explorer = event %#v, error %v", event, err)
	}
	if err := model.Validate(); err != nil {
		t.Fatalf("durable explorer limit state is invalid: %v", err)
	}
	if model.LimitPause == nil || model.LimitPause.Counter != implementationstate.CycleCounterExplorer || model.LimitPause.Episode != "implementation" {
		t.Fatalf("explorer limit pause = %#v", model.LimitPause)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Resume(); err != nil {
		t.Fatal(err)
	}
	if counters := current.Assignments[0].Counters; counters.Explorer["implementation"] != 0 || counters.Explorer["review"] != 1 {
		t.Fatalf("explorer reset was not episode-specific: %#v", counters)
	}
	if _, err := reopened.Record(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if got, _, err := reopened.RecordAssignmentAttemptStartWithLimits(context.Background(), current, "assignment", "explorer-implementation-2", limits); err != nil || got.SemanticRound != 1 {
		t.Fatalf("explorer after episode reset = %#v, %v", got, err)
	}
	if err := current.Validate(); err != nil {
		t.Fatalf("resumed explorer state is invalid: %v", err)
	}
}

func TestCounterNoneAttemptStartsPersistAcrossStoreRestart(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newStoredModel(t, run)
	document := publishTestReference(t, run, "brief")
	basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
	if err := model.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "brief", Number: 1, Document: document}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddOperation("assignment", implementationstate.Operation{ID: "agent", Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddRunOperation(implementationstate.Operation{ID: "run-agent", Kind: implementationstate.OperationAgent, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if attempt, _, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "agent"); err != nil || attempt != (implementationstate.OperationAttempt{Number: 1}) {
		t.Fatalf("first assignment CounterNone attempt = %#v, %v", attempt, err)
	}
	if attempt, _, err := state.RecordRunAttemptStart(context.Background(), model, "run-agent"); err != nil || attempt != (implementationstate.OperationAttempt{Number: 1}) {
		t.Fatalf("first run CounterNone attempt = %#v, %v", attempt, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if attempt, _, err := reopened.RecordAssignmentAttemptStart(context.Background(), restarted, "assignment", "agent"); err != nil || attempt != (implementationstate.OperationAttempt{Number: 2}) {
		t.Fatalf("assignment CounterNone attempt after restart = %#v, %v", attempt, err)
	}
	if attempt, _, err := reopened.RecordRunAttemptStart(context.Background(), restarted, "run-agent"); err != nil || attempt != (implementationstate.OperationAttempt{Number: 2}) {
		t.Fatalf("run CounterNone attempt after restart = %#v, %v", attempt, err)
	}
}

func TestRecordAttemptStartLeavesCallerUntouchedUntilDurableAndRetriesPendingStart(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := attemptStartModel(t, run)
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}

	closed := *model
	if err := closed.Close("closed"); err != nil {
		t.Fatal(err)
	}
	beforeClosed := mustJSON(t, &closed)
	if _, _, err := state.RecordAssignmentAttemptStart(context.Background(), &closed, "assignment", "agent"); !errors.Is(err, implementationstate.ErrInvalidTransition) {
		t.Fatalf("closed attempt start error = %v", err)
	}
	if got := mustJSON(t, &closed); !bytes.Equal(got, beforeClosed) {
		t.Fatal("closed caller changed before journal append")
	}

	invalid := *model
	invalid.Identity.Change = ""
	beforeInvalid := mustJSON(t, &invalid)
	if _, _, err := state.RecordAssignmentAttemptStart(context.Background(), &invalid, "assignment", "agent"); !errors.Is(err, implementationstate.ErrInvalidState) {
		t.Fatalf("invalid attempt start error = %v", err)
	}
	if got := mustJSON(t, &invalid); !bytes.Equal(got, beforeInvalid) {
		t.Fatal("invalid caller changed before journal append")
	}
	if sequence, err := journalLastSequence(state.JournalPath()); err != nil || sequence != 1 {
		t.Fatalf("pre-journal failures changed journal: sequence=%d, error=%v", sequence, err)
	}

	replaceBeforeProjectionCommitHook(t, func(implementationstate.Event) error { return errors.New("injected projection failure") })
	first, event, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "agent")
	if err == nil || first != (implementationstate.OperationAttempt{Number: 1}) || event.Sequence != 2 {
		t.Fatalf("post-journal start = %#v, event=%d, error=%v", first, event.Sequence, err)
	}
	if got := model.Assignments[0].Operations[0].Attempts; len(got) != 1 || got[0] != first {
		t.Fatalf("caller did not receive canonical durable attempt: %#v", got)
	}
	beforeProjectionCommitHook = nil
	second, retried, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "agent")
	if err != nil || second != first || retried.Sequence != 2 {
		t.Fatalf("pending retry = %#v, event=%d, error=%v; want original attempt", second, retried.Sequence, err)
	}
	if got := model.Assignments[0].Operations[0].Attempts; len(got) != 1 || got[0] != first {
		t.Fatalf("pending retry incremented attempt: %#v", got)
	}
	if current, sequence, err := state.Current(context.Background()); err != nil || sequence != 2 || len(current.Assignments[0].Operations[0].Attempts) != 1 {
		t.Fatalf("pending retry projection = sequence=%d state=%#v error=%v", sequence, current, err)
	}
	if sequence, err := journalLastSequence(state.JournalPath()); err != nil || sequence != 2 {
		t.Fatalf("pending retry journal = sequence=%d, error=%v", sequence, err)
	}
}

func TestRecordAttemptStartKeepsAfterJournalSyncFailurePending(t *testing.T) {
	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	model := attemptStartModel(t, run)
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	staleEvent, err := implementationstate.NewRunStateEvent(1, model)
	if err != nil {
		t.Fatal(err)
	}
	stale := staleEvent.State
	replaceAfterJournalSyncHook(t, func() error { return errors.New("after journal sync") })
	first, event, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "agent")
	if err == nil || first != (implementationstate.OperationAttempt{Number: 1}) || event.Sequence != 2 {
		t.Fatalf("after-sync attempt = %#v, event=%d, error=%v", first, event.Sequence, err)
	}
	if got := model.Assignments[0].Operations[0].Attempts; len(got) != 1 || got[0] != first {
		t.Fatalf("caller did not retain durably appended attempt: %#v", got)
	}
	if sequence, err := journalLastSequence(state.JournalPath()); err != nil || sequence != 2 {
		t.Fatalf("after-sync durable journal = sequence=%d, error=%v", sequence, err)
	}
	if _, err := state.Record(context.Background(), stale); !errors.Is(err, ErrPendingEvent) || state.pending == nil {
		t.Fatalf("stale Record() = %v, pending=%#v; want retained pending event", err, state.pending)
	}
	afterJournalSyncHook = nil
	second, retried, err := state.RecordAssignmentAttemptStart(context.Background(), model, "assignment", "agent")
	if err != nil || second != first || retried.Sequence != 2 {
		t.Fatalf("after-sync pending retry = %#v, event=%d, error=%v", second, retried.Sequence, err)
	}
	if got := model.Assignments[0].Operations[0].Attempts; len(got) != 1 || got[0] != first {
		t.Fatalf("after-sync retry incremented attempt: %#v", got)
	}
}

func attemptStartModel(t *testing.T, run *Run) *implementationstate.Run {
	t.Helper()
	model := newStoredModel(t, run)
	document := publishTestReference(t, run, "attempt-brief")
	basis := implementationstate.AcceptanceBasis{Specification: model.Identity.Specification, Configuration: model.Identity.Configuration}
	if err := model.StartAssignment("assignment", []implementationstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddBriefVersion("assignment", implementationstate.BriefVersion{ID: "attempt-brief", Number: 1, Document: document}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddOperation("assignment", implementationstate.Operation{ID: "agent", Kind: implementationstate.OperationAgent, BriefID: "attempt-brief", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	return model
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStateStoreRecoveryRemovesHotSQLiteJournalBeforePublishingReplacement(t *testing.T) {
	for _, test := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{name: "missing main", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt main", damage: func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newStoredRun(t)
			state, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			model := newStoredModel(t, run)
			if _, err := state.Record(context.Background(), model); err != nil {
				t.Fatal(err)
			}
			if err := state.Close(); err != nil {
				t.Fatal(err)
			}

			createHotSQLiteJournal(t, state.DatabasePath())
			if err := model.Pause("second durable event"); err != nil {
				t.Fatal(err)
			}
			event, err := implementationstate.NewRunStateEvent(2, model)
			if err != nil {
				t.Fatal(err)
			}
			data, err := marshalEvent(event)
			if err != nil {
				t.Fatal(err)
			}
			if err := appendJournal(state.JournalPath(), data); err != nil {
				t.Fatal(err)
			}
			test.damage(t, state.DatabasePath())

			recovered, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			if current, sequence, err := recovered.Current(context.Background()); err != nil || sequence != 2 || current.Status != implementationstate.RunPaused {
				t.Fatalf("recovered hot-journal projection = sequence %d, state %#v, error %v", sequence, current, err)
			}
			if _, err := os.Stat(state.DatabasePath() + "-journal"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale rollback journal after recovery = %v, want absent", err)
			}
			if err := recovered.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenState(run)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, sequence, err := reopened.Current(context.Background()); err != nil || sequence != 2 {
				t.Fatalf("reopened replacement after hot journal = sequence %d, error %v", sequence, err)
			}
		})
	}
}

// TestStateStoreHotJournalHelper is run as a separate process so killing it
// leaves a real SQLite rollback journal, rather than a synthetic sidecar.
func TestStateStoreHotJournalHelper(t *testing.T) {
	path := os.Getenv("STEPAN_HOT_JOURNAL_PATH")
	if path == "" {
		return
	}
	db, err := openProjection(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode = DELETE; BEGIN IMMEDIATE; UPDATE current_state SET state_json = randomblob(length(state_json)) WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("STEPAN_HOT_JOURNAL_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func createHotSQLiteJournal(t *testing.T, databasePath string) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "hot-journal-ready")
	command := exec.Command(os.Args[0], "-test.run=^TestStateStoreHotJournalHelper$", "-test.v")
	command.Env = append(os.Environ(), "STEPAN_HOT_JOURNAL_PATH="+databasePath, "STEPAN_HOT_JOURNAL_READY="+ready)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for helper to create a hot SQLite journal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(databasePath + "-journal"); err != nil {
		t.Fatalf("hot SQLite journal was not created: %v", err)
	}
}

func TestStateStoreRecoveryStreamsLargeJournal(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	original := publishReplacementProjection
	defer func() { publishReplacementProjection = original }()

	run := newStoredRun(t)
	state, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	model := newLargeStoredModel(t, run, 500, 256)
	journalFile, err := os.Create(state.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	var journalBytes int64
	for sequence := uint64(1); sequence <= 128; sequence++ {
		event, err := implementationstate.NewRunStateEvent(sequence, model)
		if err != nil {
			t.Fatal(err)
		}
		data, err := marshalEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journalFile.Write(data); err != nil {
			t.Fatal(err)
		}
		journalBytes += int64(len(data))
	}
	if err := journalFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(state.DatabasePath()); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var during runtime.MemStats
	publishReplacementProjection = func(temporary, target string) error {
		runtime.GC()
		runtime.ReadMemStats(&during)
		return original(temporary, target)
	}

	recovered, err := OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, sequence, err := recovered.Current(context.Background()); err != nil || sequence != 128 {
		t.Fatalf("large streamed recovery = sequence %d, error %v", sequence, err)
	}
	if journalBytes < 8<<20 {
		t.Fatalf("large journal = %d bytes, want at least 8 MiB", journalBytes)
	}
	if during.HeapAlloc > before.HeapAlloc+uint64(journalBytes/4) {
		t.Fatalf("recovery retained %d heap bytes for a %d-byte journal; want bounded streaming memory", during.HeapAlloc-before.HeapAlloc, journalBytes)
	}
}

func newLargeStoredModel(t *testing.T, run *Run, taskCount, titleSize int) *implementationstate.Run {
	t.Helper()
	model := newStoredModel(t, run)
	model.Tasks = make([]implementationstate.Task, taskCount)
	model.LeafStatus = make(map[implementationstate.TaskID]implementationstate.TaskStatus, taskCount)
	for index := range model.Tasks {
		id := implementationstate.TaskID("task-" + strconv.Itoa(index))
		model.Tasks[index] = implementationstate.Task{ID: id, Order: index, Title: strings.Repeat("title", titleSize/5)}
		model.LeafStatus[id] = implementationstate.TaskPending
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	return model
}
