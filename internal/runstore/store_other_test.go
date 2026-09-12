//go:build !windows

package runstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

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

	link := filepath.Join(store.RunsRoot(), "linked-run")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("linked-run"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("open symlinked run error = %v, want unsafe path", err)
	}
}
