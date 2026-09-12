//go:build !windows

package runstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishProjectionGroupQuarantinesSidecarsAfterUnixRenameSyncFailure(t *testing.T) {
	stateStoreTestHookMu.Lock()
	defer stateStoreTestHookMu.Unlock()
	originalSync := syncReplacementDirectory
	defer func() { syncReplacementDirectory = originalSync }()

	directory := t.TempDir()
	temporary := filepath.Join(directory, "replacement.sqlite")
	target := filepath.Join(directory, StateDatabaseFileName)
	if err := os.WriteFile(temporary, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range projectionSidecars(target) {
		if err := os.WriteFile(sidecar, []byte(filepath.Base(sidecar)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	injected := errors.New("directory sync after rename")
	syncReplacementDirectory = func(string) error { return injected }
	err := publishProjectionGroup(temporary, target)
	var replacementErr *projectionReplacementError
	if !errors.As(err, &replacementErr) || !replacementErr.mainReplaced || !errors.Is(err, injected) {
		t.Fatalf("publishProjectionGroup() error = %v, want post-rename sync failure", err)
	}
	if published, err := os.ReadFile(target); err != nil || !bytes.Equal(published, []byte("replacement")) {
		t.Fatalf("main file after post-rename failure = %q, error %v", published, err)
	}
	for _, sidecar := range projectionSidecars(target) {
		if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("sidecar %s was re-paired after post-rename failure: %v", filepath.Base(sidecar), err)
		}
		backup := temporary + ".previous-" + filepath.Base(sidecar)
		if quarantined, err := os.ReadFile(backup); err != nil || !bytes.Equal(quarantined, []byte(filepath.Base(sidecar))) {
			t.Fatalf("quarantined sidecar %s = %q, error %v", filepath.Base(sidecar), quarantined, err)
		}
	}
}
