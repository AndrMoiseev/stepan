package store

import (
	"path/filepath"
	"testing"
)

func TestTransientStoreRetainsPublishedArtifactsAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".stepan")
	store, err := NewTransient(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := run.Publish("result", []byte("accepted output"))
	if err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := NewTransient(root)
	if err != nil {
		t.Fatal(err)
	}
	reopenedRun, err := reopenedStore.Open("run-1")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := reopenedRun.Read(reference)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "accepted output" {
		t.Fatalf("reopened artifact = %q, want accepted output", contents)
	}
}

func TestTransientStoreOpensSQLiteWithoutDurableSynchronization(t *testing.T) {
	store, err := NewTransient(filepath.Join(t.TempDir(), ".stepan"))
	if err != nil {
		t.Fatalf("NewTransient() error = %v", err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	state, err := OpenState(run)
	if err != nil {
		t.Fatalf("OpenState() error = %v", err)
	}
	defer state.Close()

	var synchronous int
	if err := state.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("read PRAGMA synchronous: %v", err)
	}
	if synchronous != 0 {
		t.Fatalf("PRAGMA synchronous = %d, want 0", synchronous)
	}
	var journalMode string
	if err := state.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read PRAGMA journal_mode: %v", err)
	}
	if journalMode != "memory" {
		t.Fatalf("PRAGMA journal_mode = %q, want memory", journalMode)
	}
}
