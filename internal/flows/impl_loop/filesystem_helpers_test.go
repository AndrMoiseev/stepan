package impl_loop

import (
	"os"
	"path/filepath"
	"testing"

	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func mustControllerStore(t *testing.T, root string) *runstore.Store {
	t.Helper()
	store, err := runstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func writeWorkspaceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
