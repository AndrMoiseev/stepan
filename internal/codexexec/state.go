package codexexec

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

type State struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	InvocationID  string    `json:"invocation_id"`
	Status        string    `json:"status"`
	LastSeq       uint64    `json:"last_seq"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Seq           uint64          `json:"seq"`
	EventID       string          `json:"event_id"`
	Timestamp     time.Time       `json:"timestamp"`
	RunID         string          `json:"run_id"`
	TaskID        string          `json:"task_id,omitempty"`
	InvocationID  string          `json:"invocation_id,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
}

type Journal struct {
	mu           sync.Mutex
	file         *os.File
	runID        string
	invocationID string
	seq          uint64
}

func NewJournal(path, runID, invocationID string) (*Journal, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &Journal{file: file, runID: runID, invocationID: invocationID}, nil
}

func (j *Journal) Observe(stream string, destination io.Writer, data []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	n, err := destination.Write(data)
	if n > 0 {
		payload, marshalErr := json.Marshal(struct {
			Stream    string `json:"stream"`
			ByteCount int    `json:"byte_count"`
		}{stream, n})
		if marshalErr == nil {
			marshalErr = j.appendLocked("subprocess.output_observed", payload)
		}
		if err == nil {
			err = marshalErr
		}
	}
	return n, err
}

func (j *Journal) LastSeq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}

func (j *Journal) appendLocked(eventType string, payload json.RawMessage) error {
	eventID, err := randomID()
	if err != nil {
		return err
	}
	j.seq++
	event := Event{
		SchemaVersion: 1, Seq: j.seq, EventID: eventID, Timestamp: time.Now().UTC(),
		RunID: j.runID, InvocationID: j.invocationID, Type: eventType, Payload: payload,
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = j.file.Write(data)
	return err
}

func WriteState(path string, state State) error {
	state.SchemaVersion = 1
	state.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(path, state)
}

func ReadState(path string) (State, error) {
	file, err := os.Open(path)
	if err != nil {
		return State{}, err
	}
	defer file.Close()
	var state State
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return State{}, errors.New("state contains trailing data")
	}
	if state.SchemaVersion != 1 || state.RunID == "" {
		return State{}, errors.New("invalid state")
	}
	return state, nil
}

func ReadEvents(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var events []Event
	for {
		line, newline, tooLong, err := readLine(reader, MaxJSONLLineBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !newline {
			break
		}
		if tooLong {
			return nil, errors.New("journal line too long")
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, err
		}
		wantSeq := uint64(len(events) + 1)
		if event.SchemaVersion != 1 || event.Seq != wantSeq {
			return nil, fmt.Errorf("invalid journal sequence: got %d, want %d", event.Seq, wantSeq)
		}
		events = append(events, event)
	}
	return events, nil
}
