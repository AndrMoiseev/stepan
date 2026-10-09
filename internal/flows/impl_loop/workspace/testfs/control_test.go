package testfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestTargetedRestorePreservesOtherChanges(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	write := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	control := New()
	write("protected.txt", "original")
	before, err := control.Capture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	write("protected.txt", "changed")
	write("generated.txt", "new")
	write("allowed.txt", "keep")
	after, err := control.Capture(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.EnsureUnchanged(ctx, root, before); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("changed workspace: %v", err)
	}
	restored, err := control.RestorePaths(ctx, root, before, after, []string{"protected.txt", "generated.txt"})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := control.Diff(ctx, root, before, restored)
	if err != nil || !reflect.DeepEqual(diff.Paths, []string{"allowed.txt"}) {
		t.Fatalf("remaining changes = %v, error = %v", diff.Paths, err)
	}
	if err := control.EnsureUnchanged(ctx, root, restored); err != nil {
		t.Fatal(err)
	}
	write("allowed.txt", "newer work")
	if _, err := control.RestorePaths(ctx, root, before, restored, []string{"allowed.txt"}); !errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("restore with stale snapshot: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "allowed.txt"))
	if err != nil || string(contents) != "newer work" {
		t.Fatalf("stale restore changed current work: %q, %v", contents, err)
	}
}
