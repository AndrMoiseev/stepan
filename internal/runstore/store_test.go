package runstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
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

func TestPublishRetryAfterFinalBarrierFailureDoesNotReuseUnpublishedTarget(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}

	original := finalizePublication
	fail := true
	replaceFinalizerForTest(t, func(temporary, target string) error {
		err := original(temporary, target)
		if target == run.filePath("result") && fail {
			fail = false
			return errors.New("injected final publication failure")
		}
		return err
	})

	if _, err := run.Publish("result", []byte("durable payload")); err == nil {
		t.Fatal("publish succeeded despite final barrier failure")
	}
	target := run.filePath("result")
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication left reusable target: %v", err)
	}
	if _, err := os.Lstat(run.markerPath(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed publication left usable marker: %v", err)
	}

	reference, err := run.Publish("result", []byte("durable payload"))
	if err != nil {
		t.Fatalf("retry after failed barrier: %v", err)
	}
	if err := run.VerifyReference(reference); err != nil {
		t.Fatalf("retry reference is not durable: %v", err)
	}
}

func TestConcurrentSameIDPublishWaitsForWinningBarrier(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}

	original := finalizePublication
	entered := make(chan struct{})
	release := make(chan struct{})
	first := true
	replaceFinalizerForTest(t, func(temporary, target string) error {
		err := original(temporary, target)
		if target == run.filePath("same-result") && first {
			first = false
			close(entered)
			<-release
		}
		return err
	})

	type result struct {
		reference implementationstate.EvidenceRef
		err       error
	}
	winner := make(chan result, 1)
	loser := make(chan result, 1)
	go func() {
		reference, err := run.Publish("same-result", []byte("same bytes"))
		winner <- result{reference, err}
	}()
	<-entered
	go func() {
		reference, err := run.Publish("same-result", []byte("same bytes"))
		loser <- result{reference, err}
	}()
	select {
	case outcome := <-loser:
		t.Fatalf("loser returned before winning barrier: %+v", outcome)
	default:
	}
	close(release)
	firstResult := <-winner
	secondResult := <-loser
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent publications failed: winner=%v loser=%v", firstResult.err, secondResult.err)
	}
	if firstResult.reference != secondResult.reference {
		t.Fatalf("concurrent references differ: %#v != %#v", firstResult.reference, secondResult.reference)
	}
}

func TestReadUsesOneVerifiedHandle(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-1")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := run.Publish("result", []byte("original"))
	if err != nil {
		t.Fatal(err)
	}
	target := run.filePath(reference.ID)
	replaceBeforeOpenHookForTest(t, func(path string) {
		if path == target {
			if err := os.WriteFile(target, []byte("replaced"), 0o600); err != nil {
				t.Errorf("replace verified target: %v", err)
			}
		}
	})

	if _, err := run.Read(reference); !errors.Is(err, ErrReferenceIntegrity) {
		t.Fatalf("read after replacement error = %v, want integrity failure", err)
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

var runstoreTestHookMu sync.Mutex

func replaceFinalizerForTest(t *testing.T, replacement func(string, string) error) {
	t.Helper()
	runstoreTestHookMu.Lock()
	original := finalizePublication
	finalizePublication = replacement
	t.Cleanup(func() {
		finalizePublication = original
		runstoreTestHookMu.Unlock()
	})
}

func replaceBeforeOpenHookForTest(t *testing.T, replacement func(string)) {
	t.Helper()
	runstoreTestHookMu.Lock()
	original := beforeArtifactOpenHook
	beforeArtifactOpenHook = replacement
	t.Cleanup(func() {
		beforeArtifactOpenHook = original
		runstoreTestHookMu.Unlock()
	})
}
