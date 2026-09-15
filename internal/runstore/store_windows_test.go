//go:build windows && process_integration

package runstore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

// A junction requires no Developer Mode or symbolic-link privilege, and is a
// reparse-point escape that Lstat must reject just like a symbolic link.
func makeRunstoreJunction(link, target string) error {
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create runstore junction: %w: %s", err, output)
	}
	return nil
}

func TestRejectsTraversalAndJunctionedRunComponents(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []implementationstate.RunID{"", "..", "../other", `..\\other`, "nested/run"} {
		if _, err := store.Create(id); !errors.Is(err, ErrInvalidRunID) {
			t.Fatalf("Create(%q) error = %v, want invalid run ID", id, err)
		}
	}

	if err := makeRunstoreJunction(filepath.Join(store.RunsRoot(), "linked-run"), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("linked-run"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open junctioned run error = %v, want unsafe path", err)
	}
}

func TestRejectsJunctionedFilesAndArtifactTargets(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-files")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(run.FilesPath()); err != nil {
		t.Fatal(err)
	}
	if err := makeRunstoreJunction(run.FilesPath(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("run-files"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open junctioned files path error = %v, want unsafe path", err)
	}

	run, err = store.Create("run-artifact")
	if err != nil {
		t.Fatal(err)
	}
	reference := implementationstate.EvidenceRef{ID: "artifact", Digest: sha256Hex([]byte("contents"))}
	target := run.filePath(reference.ID)
	if err := os.WriteFile(run.markerPath(target), []byte(reference.Digest+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := makeRunstoreJunction(target, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := run.VerifyReference(reference); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("verify junctioned artifact error = %v, want unsafe path", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := run.VerifyReference(reference); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("verify directory artifact error = %v, want unsafe path", err)
	}
}
