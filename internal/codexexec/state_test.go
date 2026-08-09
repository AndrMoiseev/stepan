package codexexec

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStateAtomicallyReplacesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first := State{RunID: "запуск", InvocationID: "invocation", Status: "running"}
	if err := WriteState(path, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Status = "completed"
	second.LastSeq = 42
	if err := WriteState(path, second); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.LastSeq != 42 || got.RunID != "запуск" {
		t.Fatalf("state = %+v", got)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), "state.json.tmp-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary state files remain: %v, %v", temps, err)
	}
}

func TestSequencerPreservesStreamOrderWithoutRawData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := NewJournal(path, "run", "invocation")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	writers := []observedWriter{{"stdout", &stdout, journal}, {"stderr", &stderr, journal}}
	var group sync.WaitGroup
	for _, output := range writers {
		output := output
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				if _, err := output.Write([]byte("OPENAI_API_KEY=top-secret")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	group.Wait()
	if journal.LastSeq() != 200 {
		t.Fatalf("last seq = %d", journal.LastSeq())
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(path)
	if err != nil || len(events) != 200 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	counts := map[string]int{}
	for index, event := range events {
		if event.Seq != uint64(index+1) {
			t.Fatalf("seq = %d at index %d", event.Seq, index)
		}
		var payload struct {
			Stream string `json:"stream"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		counts[payload.Stream]++
	}
	if counts["stdout"] != 100 || counts["stderr"] != 100 {
		t.Fatalf("stream counts = %v", counts)
	}
	journalData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journalData, []byte("top-secret")) || bytes.Contains(journalData, []byte("environment")) {
		t.Fatal("journal contains raw output or environment")
	}
}

func TestReadEventsKeepsLastValidLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := NewJournal(path, "run", "invocation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Observe("stdout", io.Discard, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Observe("stderr", io.Discard, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`{"schema_version":1,"seq":3`)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(path)
	if err != nil || len(events) != 2 || events[1].Seq != 2 {
		t.Fatalf("events = %+v, %v", events, err)
	}
}
