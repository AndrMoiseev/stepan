//go:build !windows || process_integration

package impl_loop

import (
	"os"
	"path/filepath"
	"testing"

	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func onlyControllerLockEntry(t *testing.T, store *runstore.Store) string {
	t.Helper()
	directory := filepath.Join(store.Root(), "controller-locks")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("lock entries = %d, want 1", len(entries))
	}
	return filepath.Join(directory, entries[0].Name())
}
