package runstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
)

// StateStore owns the append-only event journal and its SQLite projection for
// one run. It serializes writers in-process; operating-system process locking
// is added by the controller layer.
type StateStore struct {
	journalPath  string
	databasePath string
	db           *sql.DB

	mu      sync.Mutex
	lastSeq uint64
	pending *pendingEvent
}

type pendingEvent struct {
	event implementationstate.Event
	data  []byte
}

// OpenState opens the durable state layers for an existing run layout. It
// intentionally requires a clean, already-projected journal: replay after a
// lost database or an interrupted journal write is recovery work for 3.4.
func OpenState(run *Run) (*StateStore, error) {
	if run == nil {
		return nil, fmt.Errorf("%w: nil run", ErrUnsafePath)
	}
	if err := requireDirectory(run.directory); err != nil {
		return nil, err
	}

	journalPath := filepath.Join(run.directory, JournalFileName)
	if err := requireRegularOrAbsent(journalPath); err != nil {
		return nil, err
	}
	lastSeq, err := journalLastSequence(journalPath)
	if err != nil {
		return nil, err
	}

	databasePath := filepath.Join(run.directory, StateDatabaseFileName)
	if err := requireRegularOrAbsent(databasePath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open state projection: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &StateStore{journalPath: journalPath, databasePath: databasePath, db: db, lastSeq: lastSeq}
	if err := store.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	lastApplied, err := store.lastApplied(context.Background(), nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	if lastApplied < 0 || uint64(lastApplied) != lastSeq {
		db.Close()
		return nil, fmt.Errorf("%w: journal and projection differ", ErrJournalSequence)
	}
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
	return s.db.Close()
}

// Record writes one new event to JSONL and synchronizes it before applying the
// event to SQLite. If projection fails after the journal is durable, retrying
// Record with the same state applies that exact event without a second line.
func (s *StateStore) Record(ctx context.Context, state *implementationstate.Run) (implementationstate.Event, error) {
	if s == nil || s.db == nil {
		return implementationstate.Event{}, fmt.Errorf("%w: nil state store", ErrUnsafePath)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending != nil {
		event, err := implementationstate.NewRunStateEvent(s.pending.event.Sequence, state)
		if err != nil {
			return implementationstate.Event{}, err
		}
		data, err := marshalEvent(event)
		if err != nil {
			return implementationstate.Event{}, err
		}
		if !bytes.Equal(data, s.pending.data) {
			return implementationstate.Event{}, ErrPendingEvent
		}
		if err := s.apply(ctx, s.pending.event, s.pending.data); err != nil {
			return s.pending.event, err
		}
		event = s.pending.event
		s.pending = nil
		return event, nil
	}

	event, err := implementationstate.NewRunStateEvent(s.lastSeq+1, state)
	if err != nil {
		return implementationstate.Event{}, err
	}
	data, err := marshalEvent(event)
	if err != nil {
		return implementationstate.Event{}, err
	}
	if err := appendJournal(s.journalPath, data); err != nil {
		return implementationstate.Event{}, err
	}
	s.lastSeq = event.Sequence
	s.pending = &pendingEvent{event: event, data: data}
	if err := s.apply(ctx, event, data); err != nil {
		return event, err
	}
	s.pending = nil
	return event, nil
}

// ApplyJournalEvent transactionally applies a previously durable event. It is
// idempotent only for byte-identical event records, which is the replay entry
// point used by recovery in the next storage task.
func (s *StateStore) ApplyJournalEvent(ctx context.Context, event implementationstate.Event) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: nil state store", ErrUnsafePath)
	}
	data, err := marshalEvent(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(ctx, event, data)
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

func (s *StateStore) apply(ctx context.Context, event implementationstate.Event, data []byte) (err error) {
	if err := event.Validate(); err != nil {
		return err
	}
	state, err := event.Apply(nil)
	if err != nil {
		return err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode current state projection: %w", err)
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

func appendJournal(path string, data []byte) error {
	if err := requireRegularOrAbsent(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open event journal: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("append event journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync event journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close event journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync event journal directory: %w", err)
	}
	return nil
}

func journalLastSequence(path string) (uint64, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open event journal: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, fmt.Errorf("read event journal: %w", err)
	}
	if len(data) == 0 {
		return 0, nil
	}
	if data[len(data)-1] != '\n' {
		return 0, fmt.Errorf("%w: incomplete final event", ErrJournalSequence)
	}
	var last uint64
	for _, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var event implementationstate.Event
		if len(line) == 0 || json.Unmarshal(line, &event) != nil || event.Validate() != nil || event.Sequence != last+1 {
			return 0, ErrJournalSequence
		}
		last = event.Sequence
	}
	return last, nil
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

var beforeProjectionCommitHook func(implementationstate.Event) error
