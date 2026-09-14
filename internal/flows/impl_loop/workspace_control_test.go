package impl_loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
)

func TestGitWorkspaceControlContract(t *testing.T) {
	ctx := context.Background()
	repository := newSnapshotRepository(t)
	workspace := GitWorkspaceControl{}
	before, err := workspace.Capture(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(repository, "generated.go")
	if err := os.WriteFile(generated, []byte("package generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := workspace.Capture(ctx, repository)
	if err != nil {
		t.Fatal(err)
	}
	difference, err := workspace.Diff(ctx, repository, before, after)
	if err != nil || len(difference.Paths) != 1 || difference.Paths[0] != "generated.go" {
		t.Fatalf("difference = %#v, error %v", difference, err)
	}
	if err := workspace.EnsureUnchanged(ctx, repository, before); !errors.Is(err, gitsnapshot.ErrRepositoryDiverged) {
		t.Fatalf("changed workspace verification = %v", err)
	}
	restored, err := workspace.RestorePaths(ctx, repository, before, after, []string{"generated.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.EnsureUnchanged(ctx, repository, restored); err != nil {
		t.Fatalf("restored workspace verification = %v", err)
	}
	if _, err := os.Stat(generated); !os.IsNotExist(err) {
		t.Fatalf("untracked file survived targeted restore: %v", err)
	}
	if err := os.WriteFile(generated, []byte("package generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, err := workspace.AssignmentDiff(ctx, repository, before.HeadOID)
	if err != nil || !strings.Contains(patch, "generated.go") || !strings.Contains(patch, "package generated") {
		t.Fatalf("assignment patch omitted generated file: error=%v\n%s", err, patch)
	}
}

// filesystemWorkspaceControl is the fast test adapter at the WorkspaceControl
// seam. It observes and restores ordinary files for orchestration tests while
// dedicated GitWorkspaceControl contract tests cover Git metadata, index,
// filters, symlinks and submodules.
type filesystemWorkspaceControl struct {
	mu     sync.Mutex
	states map[string]map[string]filesystemWorkspaceFile
}

var _ WorkspaceControl = (*filesystemWorkspaceControl)(nil)

type ensureErrorWorkspaceControl struct {
	WorkspaceControl
	err error
}

func (w ensureErrorWorkspaceControl) EnsureUnchanged(context.Context, string, gitsnapshot.Snapshot) error {
	return w.err
}

type filesystemWorkspaceFile struct {
	contents []byte
	mode     fs.FileMode
}

func newFilesystemWorkspaceControl() *filesystemWorkspaceControl {
	return &filesystemWorkspaceControl{states: make(map[string]map[string]filesystemWorkspaceFile)}
}

func newFilesystemWorkspace(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return repository
}

func (w *filesystemWorkspaceControl) Capture(_ context.Context, repository string) (gitsnapshot.Snapshot, error) {
	files, err := captureFilesystemWorkspace(repository)
	if err != nil {
		return gitsnapshot.Snapshot{}, err
	}
	digest := filesystemWorkspaceDigest(files)
	w.mu.Lock()
	w.states[digest] = cloneFilesystemWorkspace(files)
	w.mu.Unlock()
	return gitsnapshot.Snapshot{HeadOID: "test-head", HeadRef: "refs/heads/test", TreeOID: digest, IndexHash: "test-index", StatusHash: digest, SubmodulesHash: "test-submodules"}, nil
}

func (w *filesystemWorkspaceControl) Diff(_ context.Context, _ string, before, after gitsnapshot.Snapshot) (gitsnapshot.Difference, error) {
	w.mu.Lock()
	left, leftOK := w.states[before.TreeOID]
	right, rightOK := w.states[after.TreeOID]
	w.mu.Unlock()
	if !leftOK || !rightOK {
		return gitsnapshot.Difference{}, fmt.Errorf("filesystem workspace snapshot is unknown")
	}
	paths := changedFilesystemPaths(left, right)
	return gitsnapshot.Difference{
		Paths: paths, HeadChanged: before.HeadOID != after.HeadOID, HeadRefChanged: before.HeadRef != after.HeadRef,
		IndexChanged: before.IndexHash != after.IndexHash, StatusChanged: before.StatusHash != after.StatusHash,
		SubmodulesChanged: before.SubmodulesHash != after.SubmodulesHash,
	}, nil
}

func (w *filesystemWorkspaceControl) RestorePaths(ctx context.Context, repository string, before, current gitsnapshot.Snapshot, paths []string) (gitsnapshot.Snapshot, error) {
	actual, err := w.Capture(ctx, repository)
	if err != nil {
		return gitsnapshot.Snapshot{}, err
	}
	if actual.TreeOID != current.TreeOID {
		return gitsnapshot.Snapshot{}, gitsnapshot.ErrRepositoryDiverged
	}
	w.mu.Lock()
	original, ok := w.states[before.TreeOID]
	w.mu.Unlock()
	if !ok {
		return gitsnapshot.Snapshot{}, fmt.Errorf("filesystem workspace snapshot is unknown")
	}
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(path))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return gitsnapshot.Snapshot{}, fmt.Errorf("unsafe filesystem workspace path %q", path)
		}
		target := filepath.Join(repository, clean)
		file, exists := original[filepath.ToSlash(clean)]
		if !exists {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return gitsnapshot.Snapshot{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return gitsnapshot.Snapshot{}, err
		}
		if err := os.WriteFile(target, file.contents, file.mode.Perm()); err != nil {
			return gitsnapshot.Snapshot{}, err
		}
	}
	return w.Capture(ctx, repository)
}

func (w *filesystemWorkspaceControl) EnsureUnchanged(ctx context.Context, repository string, expected gitsnapshot.Snapshot) error {
	actual, err := w.Capture(ctx, repository)
	if err != nil {
		return err
	}
	if actual.TreeOID != expected.TreeOID {
		return gitsnapshot.ErrRepositoryDiverged
	}
	return nil
}

func (*filesystemWorkspaceControl) AssignmentDiff(context.Context, string, string) (string, error) {
	return "Filesystem workspace changes are supplied through snapshots.", nil
}

func captureFilesystemWorkspace(repository string) (map[string]filesystemWorkspaceFile, error) {
	files := make(map[string]filesystemWorkspaceFile)
	err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && strings.EqualFold(entry.Name(), ".git") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = filesystemWorkspaceFile{contents: contents, mode: info.Mode()}
		return nil
	})
	return files, err
}

func filesystemWorkspaceDigest(files map[string]filesystemWorkspaceFile) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		file := files[path]
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(file.mode.String()))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(file.contents)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func changedFilesystemPaths(before, after map[string]filesystemWorkspaceFile) []string {
	seen := make(map[string]struct{}, len(before)+len(after))
	for path, left := range before {
		right, ok := after[path]
		if !ok || left.mode != right.mode || !bytes.Equal(left.contents, right.contents) {
			seen[path] = struct{}{}
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			seen[path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func cloneFilesystemWorkspace(files map[string]filesystemWorkspaceFile) map[string]filesystemWorkspaceFile {
	clone := make(map[string]filesystemWorkspaceFile, len(files))
	for path, file := range files {
		file.contents = bytes.Clone(file.contents)
		clone[path] = file
	}
	return clone
}
