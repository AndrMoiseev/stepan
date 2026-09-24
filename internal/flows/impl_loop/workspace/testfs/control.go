// Package testfs provides an in-process workspace control for orchestration tests.
package testfs

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

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/git"
)

// Control is the fast test adapter at the workspace.Control
// seam. It observes and restores ordinary files for orchestration tests while
// dedicated Git adapter contract tests cover Git metadata, index,
// filters, symlinks and submodules.
type Control struct {
	mu       sync.Mutex
	states   map[string]map[string]filesystemWorkspaceFile
	heads    map[string]workspace.CommitObservation
	commits  map[string]string
	sequence uint64
}

var _ workspace.Control = (*Control)(nil)

type filesystemWorkspaceFile struct {
	contents []byte
	mode     fs.FileMode
}

// New creates an isolated, in-memory snapshot history for ordinary files.
func New() *Control {
	return &Control{states: make(map[string]map[string]filesystemWorkspaceFile), heads: make(map[string]workspace.CommitObservation), commits: make(map[string]string)}
}

func (w *Control) Capture(ctx context.Context, repository string) (git.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return git.Snapshot{}, err
	}
	files, err := captureFilesystemWorkspace(repository)
	if err != nil {
		return git.Snapshot{}, err
	}
	digest := filesystemWorkspaceDigest(files)

	w.mu.Lock()
	defer w.mu.Unlock()
	w.states[digest] = cloneFilesystemWorkspace(files)
	head, ok := w.heads[repository]
	if !ok {
		w.sequence++
		head = workspace.CommitObservation{CommitID: fmt.Sprintf("test-head-%d", w.sequence), Tree: digest, Message: "initial"}
		w.heads[repository] = head
		w.commits[head.CommitID] = digest
	}
	return git.Snapshot{HeadOID: head.CommitID, HeadRef: "refs/heads/test", TreeOID: digest, IndexHash: head.Tree, StatusHash: digest, SubmodulesHash: "test-submodules"}, nil
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
	if !sameSnapshot(actual, current) {
		return git.Snapshot{}, git.ErrRepositoryDiverged
	}
	w.mu.Lock()
	original, ok := w.states[before.TreeOID]
	w.mu.Unlock()
	if !ok {
		return git.Snapshot{}, fmt.Errorf("filesystem workspace snapshot is unknown")
	}
	// Validate all targets before mutating any of them, including parent links.
	for _, path := range paths {
		if err := safeRestoreTarget(repository, path); err != nil {
			return git.Snapshot{}, err
		}
	}
	for _, path := range paths {
		clean := filepath.Clean(filepath.FromSlash(path))
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
		if err := os.Chmod(target, file.mode.Perm()); err != nil {
			return git.Snapshot{}, err
		}
	}
	return w.Capture(ctx, repository)
}

func safeRestoreTarget(repository, path string) error {
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: path %q", git.ErrRestoreUnsafe, path)
	}
	parts := strings.Split(clean, string(filepath.Separator))
	target := repository
	for i, part := range parts {
		target = filepath.Join(target, part)
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", git.ErrRestoreUnsafe, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: target %q is not an ordinary file path", git.ErrRestoreUnsafe, path)
		}
	}
	return nil
}

func (w *Control) EnsureUnchanged(ctx context.Context, repository string, expected git.Snapshot) error {
	actual, err := w.Capture(ctx, repository)
	if err != nil {
		return err
	}
	if !sameSnapshot(actual, expected) {
		return git.ErrRepositoryDiverged
	}
	return nil
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

func sameSnapshot(a, b git.Snapshot) bool {
	return a.HeadOID == b.HeadOID && a.HeadRef == b.HeadRef && a.TreeOID == b.TreeOID && a.IndexHash == b.IndexHash && a.StatusHash == b.StatusHash && a.SubmodulesHash == b.SubmodulesHash
}

// Compare reports changed paths while requiring the same checkout identity.
func (w *Control) Compare(ctx context.Context, repository string, before, after git.Snapshot) ([]string, error) {
	if before.HeadOID != after.HeadOID || before.HeadRef != after.HeadRef {
		return nil, git.ErrRepositoryDiverged
	}
	difference, err := w.Diff(ctx, repository, before, after)
	return difference.Paths, err
}
