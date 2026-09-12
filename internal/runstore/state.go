package runstore

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	_ "modernc.org/sqlite"
)

const (
	// JournalFileName is the append-only primary record for a run.
	JournalFileName = "events.jsonl"
	// StateDatabaseFileName is the replaceable SQLite current-state projection.
	StateDatabaseFileName = "state.sqlite"
)

var (
	// ErrCurrentStateUnavailable means that no event has been projected yet.
	ErrCurrentStateUnavailable = errors.New("implementation run current state is unavailable")
	// ErrJournalSequence reports a journal whose clean sequence cannot be used
	// to append another event. Recovery of damaged journals belongs to 3.4.
	ErrJournalSequence = errors.New("invalid implementation event journal sequence")
	// ErrPendingEvent reports an attempt to append a distinct event after the
	// journal was durable but its SQLite projection failed.
	ErrPendingEvent = errors.New("implementation event awaits projection")
	// ErrRunIdentity reports state intended for a different run directory.
	ErrRunIdentity = errors.New("implementation state belongs to a different run")
	// ErrStateReference reports a state event that names unpublished or altered
	// run-local evidence.
	ErrStateReference = errors.New("implementation state has unavailable evidence")
)

// StateStore owns the append-only event journal and its SQLite projection for
// one run. It serializes writers in-process; operating-system process locking
// is added by the controller layer.
type StateStore struct {
	journalPath  string
	databasePath string
	db           *sql.DB
	run          *Run
	writer       *sync.Mutex

	mu      sync.Mutex
	pending *pendingEvent
}

type pendingEvent struct {
	data []byte
}

// OpenState opens the durable state layers for an existing run layout. JSONL
// is authoritative: an absent, invalid, or stale projection is rebuilt from
// complete journal records before the handle is returned.
func OpenState(run *Run) (*StateStore, error) {
	if run == nil {
		return nil, fmt.Errorf("%w: nil run", ErrUnsafePath)
	}
	if err := requireDirectory(run.directory); err != nil {
		return nil, err
	}
	writer, err := stateWriterLock(run.directory)
	if err != nil {
		return nil, err
	}
	writer.Lock()
	defer writer.Unlock()

	journalPath := filepath.Join(run.directory, JournalFileName)
	if err := requireRegularOrAbsent(journalPath); err != nil {
		return nil, err
	}
	store := &StateStore{journalPath: journalPath, databasePath: filepath.Join(run.directory, StateDatabaseFileName), run: run, writer: writer}
	journal, err := store.validateJournal()
	if err != nil {
		return nil, err
	}
	if journal.discardTail {
		if err := discardJournalTail(journalPath, journal.validBytes); err != nil {
			return nil, err
		}
	}

	if err := requireRegularOrAbsent(store.databasePath); err != nil {
		return nil, err
	}

	if db, current, err := openCurrentProjection(store.databasePath, store.journalPath); err == nil && current {
		store.db = db
		return store, nil
	} else if db != nil {
		_ = db.Close()
	}
	if err := rebuildProjection(context.Background(), store, journal); err != nil {
		return nil, err
	}
	db, err := openProjection(store.databasePath)
	if err != nil {
		return nil, err
	}
	store.db = db
	return store, nil
}

// JournalPath reports the fixed JSONL location for this run.
func (s *StateStore) JournalPath() string {
	if s == nil {
		return ""
	}
	return s.journalPath
}

// DatabasePath reports the fixed SQLite projection location for this run.
func (s *StateStore) DatabasePath() string {
	if s == nil {
		return ""
	}
	return s.databasePath
}

// Close releases the SQLite projection. The JSONL journal has no retained
// handle, so every append has its own fsync and close boundary.
func (s *StateStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.writer != nil {
		s.writer.Lock()
		defer s.writer.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

// Record writes one new event to JSONL and synchronizes it before applying the
// event to SQLite. If projection fails after the journal is durable, retrying
// Record with the same state applies that exact event without a second line.
func (s *StateStore) Record(ctx context.Context, state *implementationstate.Run) (implementationstate.Event, error) {
	if s == nil || s.db == nil || s.writer == nil {
		return implementationstate.Event{}, fmt.Errorf("%w: nil state store", ErrUnsafePath)
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	if event, pending, err := s.applyPendingLocked(ctx, state); pending {
		return event, err
	}

	lastSeq, err := s.refreshCleanSequence(ctx)
	if err != nil {
		return implementationstate.Event{}, err
	}
	if err := s.verifyStateReferences(state); err != nil {
		return implementationstate.Event{}, err
	}
	event, err := implementationstate.NewRunStateEvent(lastSeq+1, state)
	if err != nil {
		return implementationstate.Event{}, err
	}
	data, err := marshalEvent(event)
	if err != nil {
		return implementationstate.Event{}, err
	}
	journal := appendJournalResult(s.journalPath, data)
	if !journal.durable {
		return implementationstate.Event{}, journal.err
	}
	s.pending = &pendingEvent{data: bytes.Clone(data)}
	if journal.err != nil {
		return eventWithError(s.pending.data, journal.err)
	}
	if err := s.applyCanonical(ctx, s.pending.data, true); err != nil {
		return eventWithError(s.pending.data, err)
	}
	s.pending = nil
	event, err = cloneEventFromData(data)
	return event, err
}

// resolvePending applies a previously durable event without creating a new
// journal record. It is used by the attempt-start boundary so a caller can
// retry the same start after a projection-only failure.
func (s *StateStore) resolvePending(ctx context.Context, state *implementationstate.Run) (implementationstate.Event, bool, error) {
	if s == nil || s.db == nil || s.writer == nil {
		return implementationstate.Event{}, false, fmt.Errorf("%w: nil state store", ErrUnsafePath)
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyPendingLocked(ctx, state)
}

func (s *StateStore) applyPendingLocked(ctx context.Context, state *implementationstate.Run) (implementationstate.Event, bool, error) {
	if s.pending == nil {
		return implementationstate.Event{}, false, nil
	}
	pending, err := eventFromData(s.pending.data)
	if err != nil {
		return implementationstate.Event{}, true, err
	}
	if err := s.verifyStateReferences(state); err != nil {
		return implementationstate.Event{}, true, err
	}
	event, err := implementationstate.NewRunStateEvent(pending.Sequence, state)
	if err != nil {
		return implementationstate.Event{}, true, err
	}
	data, err := marshalEvent(event)
	if err != nil {
		return implementationstate.Event{}, true, err
	}
	if !bytes.Equal(data, s.pending.data) {
		return implementationstate.Event{}, true, ErrPendingEvent
	}
	if err := s.applyCanonical(ctx, s.pending.data, true); err != nil {
		event, cloneErr := cloneEventFromData(s.pending.data)
		if cloneErr != nil {
			return implementationstate.Event{}, true, errors.Join(err, cloneErr)
		}
		return event, true, err
	}
	s.pending = nil
	event, err = cloneEventFromData(data)
	return event, true, err
}

// RecordAssignmentAttemptStart reserves and durably records an assignment
// attempt before its external agent or command is dispatched. It changes the
// caller only after the attempt has reached durable JSONL. Callers MUST NOT
// dispatch when this method returns an error.
func (s *StateStore) RecordAssignmentAttemptStart(ctx context.Context, state *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	return s.recordAttemptStart(ctx, state, func(candidate *implementationstate.Run) (implementationstate.OperationAttempt, error) {
		return candidate.StartAssignmentAttempt(assignmentID, operationID)
	}, func(current *implementationstate.Run) (implementationstate.OperationAttempt, bool) {
		return assignmentLastAttempt(current, assignmentID, operationID)
	})
}

// RecordAssignmentAttemptStartWithLimits persists either a reserved attempt or
// the limit pause that prevented it before a caller can dispatch external
// work. The returned event is non-zero for a durably recorded limit pause.
func (s *StateStore) RecordAssignmentAttemptStartWithLimits(ctx context.Context, state *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, limits implementationstate.CycleLimits) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	return s.recordLimitedAttemptStart(ctx, state, func(candidate *implementationstate.Run) (implementationstate.OperationAttempt, error) {
		return candidate.StartAssignmentAttemptWithLimits(assignmentID, operationID, limits)
	}, func(current *implementationstate.Run) (implementationstate.OperationAttempt, bool) {
		return assignmentLastAttempt(current, assignmentID, operationID)
	})
}

// RecordRunAttemptStart is the final-review counterpart of
// RecordAssignmentAttemptStart. It provides the same record-before-dispatch
// boundary for a run-level operation.
func (s *StateStore) RecordRunAttemptStart(ctx context.Context, state *implementationstate.Run, operationID implementationstate.OperationID) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	return s.recordAttemptStart(ctx, state, func(candidate *implementationstate.Run) (implementationstate.OperationAttempt, error) {
		return candidate.StartRunAttempt(operationID)
	}, func(current *implementationstate.Run) (implementationstate.OperationAttempt, bool) {
		return runLastAttempt(current, operationID)
	})
}

// RecordRunAttemptStartWithLimits is the final-review counterpart of
// RecordAssignmentAttemptStartWithLimits.
func (s *StateStore) RecordRunAttemptStartWithLimits(ctx context.Context, state *implementationstate.Run, operationID implementationstate.OperationID, limits implementationstate.CycleLimits) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	return s.recordLimitedAttemptStart(ctx, state, func(candidate *implementationstate.Run) (implementationstate.OperationAttempt, error) {
		return candidate.StartRunAttemptWithLimits(operationID, limits)
	}, func(current *implementationstate.Run) (implementationstate.OperationAttempt, bool) {
		return runLastAttempt(current, operationID)
	})
}

func (s *StateStore) recordAttemptStart(ctx context.Context, state *implementationstate.Run, start func(*implementationstate.Run) (implementationstate.OperationAttempt, error), last func(*implementationstate.Run) (implementationstate.OperationAttempt, bool)) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	if state == nil {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, fmt.Errorf("%w: nil run state", implementationstate.ErrInvalidState)
	}
	if event, pending, err := s.resolvePending(ctx, state); pending {
		if err != nil {
			return implementationstate.OperationAttempt{}, event, err
		}
		if attempt, found := last(state); found {
			return attempt, event, nil
		}
	}
	candidateEvent, err := implementationstate.NewRunStateEvent(1, state)
	if err != nil {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, err
	}
	candidate := candidateEvent.State
	attempt, err := start(candidate)
	if err != nil {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, err
	}
	event, err := s.Record(ctx, candidate)
	if event.Sequence != 0 {
		*state = *candidate
	}
	return attempt, event, err
}

func (s *StateStore) recordLimitedAttemptStart(ctx context.Context, state *implementationstate.Run, start func(*implementationstate.Run) (implementationstate.OperationAttempt, error), last func(*implementationstate.Run) (implementationstate.OperationAttempt, bool)) (implementationstate.OperationAttempt, implementationstate.Event, error) {
	if state == nil {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, fmt.Errorf("%w: nil run state", implementationstate.ErrInvalidState)
	}
	if event, pending, err := s.resolvePending(ctx, state); pending {
		if err != nil {
			return implementationstate.OperationAttempt{}, event, err
		}
		if attempt, found := last(state); found {
			return attempt, event, nil
		}
	}
	candidateEvent, err := implementationstate.NewRunStateEvent(1, state)
	if err != nil {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, err
	}
	candidate := candidateEvent.State
	attempt, startErr := start(candidate)
	if startErr != nil && !errors.Is(startErr, implementationstate.ErrLimitExceeded) {
		return implementationstate.OperationAttempt{}, implementationstate.Event{}, startErr
	}
	event, recordErr := s.Record(ctx, candidate)
	if event.Sequence != 0 {
		*state = *candidate
	}
	if startErr != nil {
		return attempt, event, errors.Join(startErr, recordErr)
	}
	return attempt, event, recordErr
}

func assignmentLastAttempt(state *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID) (implementationstate.OperationAttempt, bool) {
	for _, assignment := range state.Assignments {
		if assignment.ID != assignmentID {
			continue
		}
		for _, operation := range assignment.Operations {
			if operation.ID == operationID && len(operation.Attempts) != 0 {
				return operation.Attempts[len(operation.Attempts)-1], true
			}
		}
	}
	return implementationstate.OperationAttempt{}, false
}

func runLastAttempt(state *implementationstate.Run, operationID implementationstate.OperationID) (implementationstate.OperationAttempt, bool) {
	for _, operation := range state.RunOperations {
		if operation.ID == operationID && len(operation.Attempts) != 0 {
			return operation.Attempts[len(operation.Attempts)-1], true
		}
	}
	return implementationstate.OperationAttempt{}, false
}

// Current returns the current SQLite projection and the sequence that produced
// it. It never reads the journal, keeping JSONL as the recovery source rather
// than a second cache on normal reads.
func (s *StateStore) Current(ctx context.Context) (*implementationstate.Run, uint64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("%w: nil state store", ErrUnsafePath)
	}
	var sequence int64
	var stateJSON []byte
	err := s.db.QueryRowContext(ctx, "SELECT last_applied_sequence, state_json FROM current_state WHERE id = 1").Scan(&sequence, &stateJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrCurrentStateUnavailable
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read current state projection: %w", err)
	}
	if sequence <= 0 {
		return nil, 0, fmt.Errorf("%w: non-positive projected sequence", ErrJournalSequence)
	}
	var state implementationstate.Run
	if err := json.Unmarshal(stateJSON, &state); err != nil {
		return nil, 0, fmt.Errorf("decode current state projection: %w", err)
	}
	if err := state.Validate(); err != nil {
		return nil, 0, fmt.Errorf("validate current state projection: %w", err)
	}
	return &state, uint64(sequence), nil
}

func (s *StateStore) initialize(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		PRAGMA journal_mode = DELETE;
		PRAGMA synchronous = FULL;
		CREATE TABLE IF NOT EXISTS applied_events (
			sequence INTEGER PRIMARY KEY,
			event_json BLOB NOT NULL
		);
		CREATE TABLE IF NOT EXISTS current_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			last_applied_sequence INTEGER NOT NULL,
			state_json BLOB NOT NULL
		);`)
	if err != nil {
		return fmt.Errorf("initialize state projection: %w", err)
	}
	return nil
}

// openCurrentProjection leaves a usable database open only when it is a
// byte-for-byte projection of the accepted journal. Any failure is recoverable
// from that journal, so callers rebuild instead of trusting a partial view.
func openCurrentProjection(path, journalPath string) (*sql.DB, bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	db, err := openProjection(path)
	if err != nil {
		return nil, false, err
	}
	current, err := projectionMatchesJournal(context.Background(), db, journalPath)
	if err != nil || !current {
		return db, false, err
	}
	return db, true, nil
}

func openProjection(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state projection: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

func projectionMatchesJournal(ctx context.Context, db *sql.DB, journalPath string) (bool, error) {
	for _, pragma := range []string{"PRAGMA integrity_check", "PRAGMA quick_check"} {
		var check string
		if err := db.QueryRowContext(ctx, pragma).Scan(&check); err != nil || check != "ok" {
			return false, err
		}
	}
	var expectedCount int
	var lastEvent implementationstate.Event
	haveEvent := false
	_, err := scanJournal(journalPath, func(_ int64, data []byte, event implementationstate.Event) error {
		expectedCount++
		var stored []byte
		if err := db.QueryRowContext(ctx, "SELECT event_json FROM applied_events WHERE sequence = ?", event.Sequence).Scan(&stored); err != nil {
			return err
		}
		if !bytes.Equal(stored, data) {
			return errProjectionMismatch
		}
		lastEvent = event
		haveEvent = true
		return nil
	})
	if err != nil {
		if errors.Is(err, errProjectionMismatch) {
			return false, nil
		}
		return false, err
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM applied_events").Scan(&count); err != nil {
		return false, err
	}
	if count != expectedCount {
		return false, nil
	}
	if !haveEvent {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM current_state").Scan(&count); err != nil || count != 0 {
			return false, err
		}
		return true, nil
	}
	var sequence int64
	var stateJSON []byte
	if err := db.QueryRowContext(ctx, "SELECT last_applied_sequence, state_json FROM current_state WHERE id = 1").Scan(&sequence, &stateJSON); err != nil {
		return false, err
	}
	if sequence != int64(lastEvent.Sequence) {
		return false, nil
	}
	want, err := json.Marshal(lastEvent.State)
	if err != nil || !bytes.Equal(stateJSON, want) {
		return false, err
	}
	return true, nil
}

func rebuildProjection(ctx context.Context, store *StateStore, journal journalContents) (err error) {
	temporary, err := os.CreateTemp(store.run.directory, ".state-rebuild-*")
	if err != nil {
		return fmt.Errorf("create replacement state projection: %w", err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("close replacement state projection: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("prepare replacement state projection: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	db, err := openProjection(temporaryPath)
	if err != nil {
		return err
	}
	replacement := &StateStore{journalPath: store.journalPath, databasePath: temporaryPath, db: db, run: store.run}
	if err := replacement.initialize(ctx); err != nil {
		_ = db.Close()
		return err
	}
	if beforeRecoveryReplayHook != nil {
		if err := beforeRecoveryReplayHook(); err != nil {
			_ = db.Close()
			return err
		}
	}
	rebuiltJournal, err := scanJournal(store.journalPath, func(_ int64, data []byte, event implementationstate.Event) error {
		if err := replacement.applyCanonical(ctx, data, true); err != nil {
			return fmt.Errorf("rebuild state projection at journal event %d: %w", event.Sequence, err)
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return err
	}
	if rebuiltJournal.lastSequence != journal.lastSequence || rebuiltJournal.validBytes != journal.validBytes || rebuiltJournal.discardTail {
		_ = db.Close()
		return fmt.Errorf("%w: journal changed while rebuilding projection", ErrJournalSequence)
	}
	if err := db.Close(); err != nil {
		return fmt.Errorf("close replacement state projection: %w", err)
	}
	if err := syncProjectionFile(temporaryPath); err != nil {
		return fmt.Errorf("sync replacement state projection: %w", err)
	}
	for _, sidecar := range projectionSidecars(temporaryPath) {
		if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("replacement state projection left SQLite sidecar %s", filepath.Base(sidecar))
			}
			return fmt.Errorf("inspect replacement SQLite sidecar %s: %w", filepath.Base(sidecar), err)
		}
	}
	if err := publishProjectionGroup(temporaryPath, store.databasePath); err != nil {
		return fmt.Errorf("publish replacement state projection: %w", err)
	}
	published, err := openProjection(store.databasePath)
	if err != nil {
		return fmt.Errorf("reopen published state projection: %w", err)
	}
	defer published.Close()
	if current, err := projectionMatchesJournal(ctx, published, store.journalPath); err != nil {
		return fmt.Errorf("verify published state projection: %w", err)
	} else if !current {
		return fmt.Errorf("verify published state projection: %w", ErrJournalSequence)
	}
	return nil
}

// publishProjectionGroup prevents a crash-left SQLite sidecar from being
// paired with a newly rebuilt main database. The replacement is fully built,
// closed, and synced before this touches an old sidecar, so a publication
// failure before the final replacement keeps the prior main projection.
func publishProjectionGroup(temporary, target string) error {
	backups, err := moveProjectionSidecarsAside(temporary, target)
	if err != nil {
		return err
	}
	if err := publishReplacementProjection(temporary, target); err != nil {
		var replacementErr *projectionReplacementError
		if errors.As(err, &replacementErr) && replacementErr.mainReplaced {
			// The new main database already has its final name. Restoring a
			// rollback journal beside it could roll old pages into the new
			// projection, so leave the backups quarantined for explicit repair.
			return err
		}
		return errors.Join(err, restoreProjectionSidecars(backups))
	}
	for _, backup := range backups {
		_ = os.Remove(backup.backup)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return fmt.Errorf("sync SQLite sidecar cleanup: %w", err)
	}
	return nil
}

// projectionReplacementError distinguishes a failed replacement attempt from
// a failed durability barrier after the main file already has its new name.
// Only the former may safely restore the old SQLite sidecar group.
type projectionReplacementError struct {
	err          error
	mainReplaced bool
}

func (e *projectionReplacementError) Error() string { return e.err.Error() }
func (e *projectionReplacementError) Unwrap() error { return e.err }

func projectionSidecars(target string) []string {
	return []string{target + "-journal", target + "-wal", target + "-shm"}
}

type projectionSidecarBackup struct {
	original string
	backup   string
}

func moveProjectionSidecarsAside(temporary, target string) ([]projectionSidecarBackup, error) {
	var backups []projectionSidecarBackup
	for _, sidecar := range projectionSidecars(target) {
		if err := requireRegularOrAbsent(sidecar); err != nil {
			return nil, errors.Join(err, restoreProjectionSidecars(backups))
		}
		if _, err := os.Lstat(sidecar); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, errors.Join(err, restoreProjectionSidecars(backups))
		}
		backup := temporary + ".previous-" + filepath.Base(sidecar)
		if err := requireRegularOrAbsent(backup); err != nil {
			return nil, errors.Join(err, restoreProjectionSidecars(backups))
		}
		if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				err = fmt.Errorf("replacement SQLite sidecar backup already exists: %s", filepath.Base(backup))
			}
			return nil, errors.Join(err, restoreProjectionSidecars(backups))
		}
		if err := os.Rename(sidecar, backup); err != nil {
			return nil, errors.Join(fmt.Errorf("move stale SQLite sidecar %s aside: %w", filepath.Base(sidecar), err), restoreProjectionSidecars(backups))
		}
		backups = append(backups, projectionSidecarBackup{original: sidecar, backup: backup})
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return nil, errors.Join(fmt.Errorf("sync SQLite sidecar isolation: %w", err), restoreProjectionSidecars(backups))
	}
	return backups, nil
}

func restoreProjectionSidecars(backups []projectionSidecarBackup) error {
	var err error
	for index := len(backups) - 1; index >= 0; index-- {
		backup := backups[index]
		if moveErr := os.Rename(backup.backup, backup.original); moveErr != nil && !errors.Is(moveErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("restore SQLite sidecar %s: %w", filepath.Base(backup.original), moveErr))
		}
	}
	if len(backups) != 0 {
		err = errors.Join(err, syncDirectory(filepath.Dir(backups[0].original)))
	}
	return err
}

func (s *StateStore) apply(ctx context.Context, data []byte) (err error) {
	return s.applyCanonical(ctx, data, true)
}

func (s *StateStore) applyCanonical(ctx context.Context, data []byte, verifyReferences bool) (err error) {
	event, err := eventFromData(data)
	if err != nil {
		return err
	}
	if verifyReferences {
		if err := s.verifyStateReferences(event.State); err != nil {
			return err
		}
	}
	state, err := event.Apply(nil)
	if err != nil {
		return err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode current state projection: %w", err)
	}
	if beforeProjectionTransactionHook != nil {
		if err := beforeProjectionTransactionHook(event); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start state projection transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	lastApplied, err := s.lastApplied(ctx, tx)
	if err != nil {
		return err
	}
	if int64(event.Sequence) <= lastApplied {
		var appliedData []byte
		if err := tx.QueryRowContext(ctx, "SELECT event_json FROM applied_events WHERE sequence = ?", event.Sequence).Scan(&appliedData); err != nil {
			return fmt.Errorf("read previously applied event: %w", err)
		}
		if !bytes.Equal(appliedData, data) {
			return fmt.Errorf("%w: event %d differs from already applied event", ErrJournalSequence, event.Sequence)
		}
		return tx.Commit()
	}
	if int64(event.Sequence) != lastApplied+1 {
		return fmt.Errorf("%w: event %d follows %d", ErrJournalSequence, event.Sequence, lastApplied)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO applied_events(sequence, event_json) VALUES (?, ?)", event.Sequence, data); err != nil {
		return fmt.Errorf("record applied event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO current_state(id, last_applied_sequence, state_json) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET last_applied_sequence = excluded.last_applied_sequence, state_json = excluded.state_json`, event.Sequence, stateJSON); err != nil {
		return fmt.Errorf("update current state projection: %w", err)
	}
	if beforeProjectionCommitHook != nil {
		if err := beforeProjectionCommitHook(event); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state projection: %w", err)
	}
	if afterProjectionTransactionHook != nil {
		if err := afterProjectionTransactionHook(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *StateStore) lastApplied(ctx context.Context, query queryer) (int64, error) {
	if query == nil {
		query = s.db
	}
	var sequence int64
	err := query.QueryRowContext(ctx, "SELECT last_applied_sequence FROM current_state WHERE id = 1").Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read last applied event: %w", err)
	}
	return sequence, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func marshalEvent(event implementationstate.Event) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode journal event: %w", err)
	}
	return append(data, '\n'), nil
}

func eventFromData(data []byte) (implementationstate.Event, error) {
	var event implementationstate.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return implementationstate.Event{}, fmt.Errorf("decode journal event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return implementationstate.Event{}, err
	}
	return event, nil
}

func cloneEventFromData(data []byte) (implementationstate.Event, error) {
	return eventFromData(bytes.Clone(data))
}

func eventWithError(data []byte, operationErr error) (implementationstate.Event, error) {
	event, err := cloneEventFromData(data)
	if err != nil {
		return implementationstate.Event{}, errors.Join(operationErr, err)
	}
	return event, operationErr
}

// journalAppendResult distinguishes a definite pre-durability failure from a
// failure after the exact event bytes reached durable storage. The latter must
// remain pending: blindly treating it as absent could repeat an external
// action after a crash.
type journalAppendResult struct {
	durable bool
	err     error
}

func appendJournal(path string, data []byte) error {
	return appendJournalResult(path, data).err
}

func appendJournalResult(path string, data []byte) journalAppendResult {
	if err := requireRegularOrAbsent(path); err != nil {
		return journalAppendResult{err: err}
	}
	if beforeJournalAppendHook != nil {
		beforeJournalAppendHook()
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return journalAppendResult{err: fmt.Errorf("open event journal: %w", err)}
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return journalAppendResult{err: fmt.Errorf("append event journal: %w", err)}
	}
	if beforeJournalSyncHook != nil {
		if err := beforeJournalSyncHook(); err != nil {
			file.Close()
			return journalAppendResult{err: err}
		}
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return journalAppendResult{err: fmt.Errorf("sync event journal: %w", err)}
	}
	if err := file.Close(); err != nil {
		return journalAppendResult{durable: true, err: fmt.Errorf("close event journal: %w", err)}
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return journalAppendResult{durable: true, err: fmt.Errorf("sync event journal directory: %w", err)}
	}
	if afterJournalSyncHook != nil {
		if err := afterJournalSyncHook(); err != nil {
			return journalAppendResult{durable: true, err: err}
		}
	}
	return journalAppendResult{durable: true}
}

func journalLastSequence(path string) (uint64, error) {
	journal, err := scanJournal(path, nil)
	return journal.lastSequence, err
}

// journalContents contains only the bounded metadata needed to continue a
// journal. Event bytes and decoded snapshots are passed to callbacks while a
// single record is live and are never retained for the full history.
type journalContents struct {
	lastSequence uint64
	validBytes   int64
	discardTail  bool
}

var errProjectionMismatch = errors.New("state projection does not match journal")

// scanJournal walks complete JSONL records in order. callback receives the
// exact newline-terminated canonical bytes and their starting byte offset.
// The only tolerated tear is an unterminated final record; any completed bad
// record is reported with its deterministic byte position.
func scanJournal(path string, callback func(offset int64, data []byte, event implementationstate.Event) error) (journalContents, error) {
	var journal journalContents
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, fmt.Errorf("open event journal: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	for {
		offset := journal.validBytes
		data, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			journal.discardTail = len(data) != 0
			return journal, nil
		}
		if readErr != nil {
			return journal, fmt.Errorf("read event journal at byte %d: %w", offset, readErr)
		}
		if len(data) == 1 {
			return journal, fmt.Errorf("%w: empty complete record at byte %d", ErrJournalSequence, offset)
		}
		event, err := eventFromData(data[:len(data)-1])
		if err != nil {
			return journal, fmt.Errorf("%w: malformed or invalid complete record at byte %d: %w", ErrJournalSequence, offset, err)
		}
		if event.Sequence != journal.lastSequence+1 {
			return journal, fmt.Errorf("%w: event at byte %d has sequence %d, want %d", ErrJournalSequence, offset, event.Sequence, journal.lastSequence+1)
		}
		if callback != nil {
			if err := callback(offset, data, event); err != nil {
				return journal, err
			}
		}
		journal.lastSequence = event.Sequence
		journal.validBytes += int64(len(data))
	}
}

func discardJournalTail(path string, validBytes int64) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open incomplete event journal tail: %w", err)
	}
	if err := file.Truncate(validBytes); err != nil {
		file.Close()
		return fmt.Errorf("discard incomplete event journal tail: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync shortened event journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close shortened event journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync shortened event journal directory: %w", err)
	}
	return nil
}

func requireRegularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrUnsafePath, path)
	}
	return nil
}

func (s *StateStore) refreshCleanSequence(ctx context.Context) (uint64, error) {
	journal, err := s.validateJournal()
	if err != nil {
		return 0, err
	}
	if journal.discardTail {
		if err := discardJournalTail(s.journalPath, journal.validBytes); err != nil {
			return 0, err
		}
	}
	lastApplied, err := s.lastApplied(ctx, nil)
	if err != nil {
		return 0, err
	}
	if lastApplied < 0 || uint64(lastApplied) != journal.lastSequence {
		return 0, fmt.Errorf("%w: journal and projection differ", ErrJournalSequence)
	}
	return journal.lastSequence, nil
}

// validateJournal performs a full streaming validation before any destructive
// tail discard or projection replacement.
func (s *StateStore) validateJournal() (journalContents, error) {
	verified := make(map[implementationstate.EvidenceRef]struct{})
	return scanJournal(s.journalPath, func(_ int64, _ []byte, event implementationstate.Event) error {
		if err := s.verifyStateReferencesSeen(event.State, verified); err != nil {
			return fmt.Errorf("verify journal event %d references: %w", event.Sequence, err)
		}
		return nil
	})
}

func (s *StateStore) verifyStateReferences(state *implementationstate.Run) error {
	return s.verifyStateReferencesSeen(state, make(map[implementationstate.EvidenceRef]struct{}))
}

func (s *StateStore) verifyStateReferencesSeen(state *implementationstate.Run, verified map[implementationstate.EvidenceRef]struct{}) error {
	if state == nil || state.Identity.ID != s.run.ID() {
		return ErrRunIdentity
	}
	for _, reference := range stateEvidenceRefs(state) {
		if _, ok := verified[reference]; ok {
			continue
		}
		if err := s.run.VerifyReference(reference); err != nil {
			return fmt.Errorf("%w: %w", ErrStateReference, err)
		}
		verified[reference] = struct{}{}
	}
	return nil
}

func stateEvidenceRefs(state *implementationstate.Run) []implementationstate.EvidenceRef {
	references := []implementationstate.EvidenceRef{
		state.Identity.BaselineState,
		state.Identity.Specification,
		state.Identity.TaskList,
		state.Identity.Configuration,
		state.CurrentState,
	}
	appendBasis := func(basis implementationstate.AcceptanceBasis) {
		references = append(references, basis.Specification, basis.Configuration)
	}
	for _, assignment := range state.Assignments {
		for _, brief := range assignment.Briefs {
			references = append(references, brief.Document)
		}
		for _, operation := range assignment.Operations {
			appendBasis(operation.Basis)
		}
		for _, result := range assignment.Results {
			references = append(references, result.State)
			appendBasis(result.Basis)
			references = append(references, result.Evidence...)
		}
		appendAcceptanceReferences(&references, assignment.Acceptance, appendBasis)
		for index := range assignment.AcceptanceHistory {
			appendAcceptanceReferences(&references, &assignment.AcceptanceHistory[index], appendBasis)
		}
		if assignment.Commit != nil {
			references = append(references, assignment.Commit.State)
			appendBasis(assignment.Commit.Basis)
		}
	}
	for _, operation := range state.RunOperations {
		appendBasis(operation.Basis)
	}
	for _, result := range state.RunResults {
		references = append(references, result.State)
		appendBasis(result.Basis)
		references = append(references, result.Evidence...)
	}
	for _, evidence := range append([]implementationstate.FinalAcceptanceEvidence{derefFinalAcceptance(state.FinalAcceptance)}, state.FinalAcceptanceHistory...) {
		if evidence.State.ID == "" {
			continue
		}
		references = append(references, evidence.State)
		appendBasis(evidence.Basis)
	}
	return references
}

func appendAcceptanceReferences(references *[]implementationstate.EvidenceRef, acceptance *implementationstate.AcceptanceEvidence, appendBasis func(implementationstate.AcceptanceBasis)) {
	if acceptance == nil {
		return
	}
	*references = append(*references, acceptance.State)
	appendBasis(acceptance.Basis)
}

func derefFinalAcceptance(evidence *implementationstate.FinalAcceptanceEvidence) implementationstate.FinalAcceptanceEvidence {
	if evidence == nil {
		return implementationstate.FinalAcceptanceEvidence{}
	}
	return *evidence
}

var stateWriterLocks sync.Map

func stateWriterLock(path string) (*sync.Mutex, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve run state path: %w", err)
	}
	canonical = filepath.Clean(canonical)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	created := &sync.Mutex{}
	actual, _ := stateWriterLocks.LoadOrStore(canonical, created)
	return actual.(*sync.Mutex), nil
}

var beforeProjectionCommitHook func(implementationstate.Event) error
var beforeJournalAppendHook func()
var beforeJournalSyncHook func() error
var afterJournalSyncHook func() error
var beforeProjectionTransactionHook func(implementationstate.Event) error
var afterProjectionTransactionHook func(implementationstate.Event) error
var publishReplacementProjection = replaceProjectionFile
var beforeRecoveryReplayHook func() error
var syncReplacementDirectory = syncDirectory
