// Package testfs provides an in-process workspace control for orchestration tests.
package testfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/git"
)

// Control is the fast test adapter at the workspace.Control
// seam. It observes and restores ordinary files for orchestration tests while
// dedicated Git adapter contract tests cover Git metadata, index,
// filters, symlinks and submodules.
type Control struct {
	mu     sync.Mutex
	states map[string]map[string]filesystemWorkspaceFile
}

var _ workspace.Control = (*Control)(nil)

type filesystemWorkspaceFile struct {
	contents []byte
	mode     fs.FileMode
}

// New creates an isolated, in-memory snapshot history for ordinary files.
func New() *Control {
	return &Control{states: make(map[string]map[string]filesystemWorkspaceFile)}
}

func (w *Control) Capture(_ context.Context, repository string) (git.Snapshot, error) {
	files, err := captureFilesystemWorkspace(repository)
	if err != nil {
		return git.Snapshot{}, err
	}
	digest := filesystemWorkspaceDigest(files)
	w.mu.Lock()
	w.states[digest] = cloneFilesystemWorkspace(files)
	w.mu.Unlock()
	return git.Snapshot{HeadOID: "test-head", HeadRef: "refs/heads/test", TreeOID: digest, IndexHash: "test-index", StatusHash: digest, SubmodulesHash: "test-submodules"}, nil
}

func (w *Control) Diff(_ context.Context, _ string, before, after git.Snapshot) (git.Difference, error) {
	w.mu.Lock()
	left, leftOK := w.states[before.TreeOID]
	right, rightOK := w.states[after.TreeOID]
	w.mu.Unlock()
	if !leftOK || !rightOK {
		return git.Difference{}, fmt.Errorf("filesystem workspace snapshot is unknown")
	}
	paths := changedFilesystemPaths(left, right)
	return git.Difference{
		Paths: paths, HeadChanged: before.HeadOID != after.HeadOID, HeadRefChanged: before.HeadRef != after.HeadRef,
		IndexChanged: before.IndexHash != after.IndexHash, StatusChanged: before.StatusHash != after.StatusHash,
		SubmodulesChanged: before.SubmodulesHash != after.SubmodulesHash,
	}, nil
}

func (w *Control) RestorePaths(ctx context.Context, repository string, before, current git.Snapshot, paths []string) (git.Snapshot, error) {
	actual, err := w.Capture(ctx, repository)
	if err != nil {
		return git.Snapshot{}, err
	}
	if actual.TreeOID != current.TreeOID {
		return git.Snapshot{}, git.ErrRepositoryDiverged
	}
	w.mu.Lock()
	original, ok := w.states[before.TreeOID]
	w.mu.Unlock()
	if !ok {
		return git.Snapshot{}, fmt.Errorf("filesystem workspace snapshot is unknown")
	}
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(path))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return git.Snapshot{}, fmt.Errorf("unsafe filesystem workspace path %q", path)
		}
		target := filepath.Join(repository, clean)
		file, exists := original[filepath.ToSlash(clean)]
		if !exists {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return git.Snapshot{}, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return git.Snapshot{}, err
		}
		if err := os.WriteFile(target, file.contents, file.mode.Perm()); err != nil {
			return git.Snapshot{}, err
		}
	}
	return w.Capture(ctx, repository)
}

func (w *Control) EnsureUnchanged(ctx context.Context, repository string, expected git.Snapshot) error {
	actual, err := w.Capture(ctx, repository)
	if err != nil {
		return err
	}
	if actual.TreeOID != expected.TreeOID {
		return git.ErrRepositoryDiverged
	}
	return nil
}

func (*Control) AssignmentDiff(context.Context, string, string) (string, error) {
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
