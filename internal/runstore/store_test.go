package runstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestRunLayoutAndRelatedFilesSurviveOpenAndClosedRuns(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), ".stepan"))
	if err != nil {
		t.Fatal(err)
	}

	openRun, err := store.Create("run-open")
	if err != nil {
		t.Fatal(err)
	}
	closedRun, err := store.Create("run-closed")
	if err != nil {
		t.Fatal(err)
	}
	openReference, err := openRun.Publish("open-log", []byte("still resumable"))
	if err != nil {
		t.Fatal(err)
	}
	closedReference, err := closedRun.Publish("closed-brief", []byte("terminal run history"))
	if err != nil {
		t.Fatal(err)
	}

	if want := filepath.Join(store.RunsRoot(), "run-open"); openRun.Path() != want {
		t.Fatalf("open run path = %q, want %q", openRun.Path(), want)
	}
	if _, err := os.Stat(filepath.Join(closedRun.Path(), FilesDirectoryName)); err != nil {
		t.Fatalf("closed files directory: %v", err)
	}

	// A new Store instance represents a later process after either a pause or
	// closure. Neither case invokes removal from this layer.
	reopenedStore, err := New(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		id        implementationstate.RunID
		reference implementationstate.EvidenceRef
		want      string
	}{
		{"run-open", openReference, "still resumable"},
		{"run-closed", closedReference, "terminal run history"},
	} {
		run, err := reopenedStore.Open(check.id)
		if err != nil {
			t.Fatalf("open %s: %v", check.id, err)
		}
		got, err := run.Read(check.reference)
		if err != nil {
			t.Fatalf("read %s: %v", check.id, err)
		}
		if string(got) != check.want {
			t.Fatalf("contents for %s = %q, want %q", check.id, got, check.want)
		}
	}
}

func TestReferenceRequiresPublishedUnchangedFile(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	missing := implementationstate.EvidenceRef{ID: "not-published", Digest: sha256Hex([]byte("missing"))}
	if err := run.VerifyReference(missing); !errors.Is(err, ErrReferenceUnavailable) {
		t.Fatalf("verify missing reference error = %v, want unavailable", err)
	}

	reference, err := run.Publish("check-log", []byte("accepted output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run.filePath(reference.ID), []byte("changed after publication"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run.VerifyReference(reference); !errors.Is(err, ErrReferenceIntegrity) {
		t.Fatalf("verify changed reference error = %v, want integrity failure", err)
	}
	if _, err := run.Publish("check-log", []byte("another result")); !errors.Is(err, ErrConflictingPublication) {
		t.Fatalf("republish changed ID error = %v, want conflict", err)
	}
}

func TestPublishCompletesBeforeReferenceCanBeReturned(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	reader := &gatedReader{started: started, release: release, data: []byte("complete result")}
	type publication struct {
		reference implementationstate.EvidenceRef
		err       error
	}
	published := make(chan publication, 1)
	go func() {
		reference, err := run.PublishReader("result", reader)
		published <- publication{reference, err}
	}()
	<-started

	// The eventual target must not appear while the producer has not supplied
	// the whole result, so a future JSONL writer cannot obtain a usable ref.
	if _, err := os.Lstat(run.filePath("result")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact appeared before complete publication: %v", err)
	}
	select {
	case result := <-published:
		t.Fatalf("publish returned before source completed: %+v", result)
	default:
	}

	close(release)
	result := <-published
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := run.VerifyReference(result.reference); err != nil {
		t.Fatalf("returned reference was not published: %v", err)
	}
}

func TestRejectsTraversalAndSymlinkedRunComponents(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []implementationstate.RunID{"", "..", "../other", `..\\other`, "nested/run"} {
		if _, err := store.Create(id); !errors.Is(err, ErrInvalidRunID) {
			t.Fatalf("Create(%q) error = %v, want invalid run ID", id, err)
		}
	}

	target := t.TempDir()
	link := filepath.Join(store.RunsRoot(), "linked-run")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("creating a symlink is unavailable: %v", err)
	}
	if _, err := store.Open("linked-run"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open symlinked run error = %v, want unsafe path", err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type gatedReader struct {
	started chan<- struct{}
	release <-chan struct{}
	data    []byte
	read    bool
}

func (r *gatedReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	close(r.started)
	<-r.release
	return copy(p, r.data), nil
}

var _ io.Reader = (*gatedReader)(nil)
