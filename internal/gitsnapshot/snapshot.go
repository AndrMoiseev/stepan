package gitsnapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrRepositoryDiverged = errors.New("REPOSITORY_DIVERGED")
var ErrOutsideBoundary = errors.New("changes outside allowed root")

type Snapshot struct {
	HeadOID        string `json:"head_oid"`
	HeadRef        string `json:"head_ref"`
	TreeOID        string `json:"tree_oid"`
	IndexHash      string `json:"index_hash"`
	StatusHash     string `json:"status_hash"`
	SubmodulesHash string `json:"submodules_hash"`
}

// Difference describes every repository dimension changed during one
// operation. Paths exclude changes already present in the before snapshot;
// submodule changes are reported separately because their internal paths do
// not belong to the superproject tree.
type Difference struct {
	Paths             []string
	HeadChanged       bool
	HeadRefChanged    bool
	IndexChanged      bool
	StatusChanged     bool
	SubmodulesChanged bool
}

type BoundaryError struct {
	Paths []string
}

func (err *BoundaryError) Error() string {
	return fmt.Sprintf("%v: %s", ErrOutsideBoundary, strings.Join(err.Paths, ", "))
}

func (err *BoundaryError) Unwrap() error { return ErrOutsideBoundary }

type repositoryState struct {
	head        string
	headRef     string
	index       []byte
	indexExists bool
	status      []byte
}

func Capture(ctx context.Context, repository string) (Snapshot, error) {
	return capture(ctx, repository, nil)
}

// Compare returns paths changed between two synthetic trees.
func Compare(ctx context.Context, repository string, before, after Snapshot) ([]string, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return nil, err
	}
	if before.HeadOID != after.HeadOID || before.HeadRef != after.HeadRef {
		return nil, ErrRepositoryDiverged
	}
	output, err := git(ctx, root, "", "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", before.TreeOID, after.TreeOID)
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("git diff-tree returned a non-NUL-terminated path list")
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path, err := normalizeRelativePath(string(part))
		if err != nil {
			return nil, fmt.Errorf("invalid changed path %q: %w", part, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// Diff returns file, HEAD, index, and submodule changes attributable to the
// interval between before and after. It permits a changed HEAD so callers can
// describe operations that intentionally create a commit.
func Diff(ctx context.Context, repository string, before, after Snapshot) (Difference, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Difference{}, err
	}
	output, err := git(ctx, root, "", "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", before.TreeOID, after.TreeOID)
	if err != nil {
		return Difference{}, err
	}
	paths, err := parseChangedPaths(output)
	if err != nil {
		return Difference{}, err
	}
	return Difference{
		Paths:             paths,
		HeadChanged:       before.HeadOID != after.HeadOID,
		HeadRefChanged:    before.HeadRef != after.HeadRef,
		IndexChanged:      before.IndexHash != after.IndexHash,
		StatusChanged:     before.StatusHash != after.StatusHash,
		SubmodulesChanged: before.SubmodulesHash != after.SubmodulesHash,
	}, nil
}

// EnsureUnchanged verifies the complete repository fingerprint before the next
// operation. It reports divergence without attempting repair.
func EnsureUnchanged(ctx context.Context, repository string, expected Snapshot) error {
	actual, err := Capture(ctx, repository)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrRepositoryDiverged
	}
	return nil
}

func parseChangedPaths(output []byte) ([]string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("git diff-tree returned a non-NUL-terminated path list")
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path, err := normalizeRelativePath(string(part))
		if err != nil {
			return nil, fmt.Errorf("invalid changed path %q: %w", part, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// CheckBoundary rejects changed paths outside allowedRoot.
func CheckBoundary(changedPaths []string, allowedRoot string) error {
	root, err := normalizeRelativePath(allowedRoot)
	if err != nil {
		return fmt.Errorf("invalid allowed root: %w", err)
	}
	prefix := root + "/"
	outside := make([]string, 0)
	for _, changedPath := range changedPaths {
		path, err := normalizeRelativePath(changedPath)
		if err != nil {
			return fmt.Errorf("invalid changed path %q: %w", changedPath, err)
		}
		if !strings.HasPrefix(path, prefix) {
			outside = append(outside, path)
		}
	}
	if len(outside) != 0 {
		return &BoundaryError{Paths: outside}
	}
	return nil
}

func normalizeRelativePath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", errors.New("path must be relative")
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes repository")
	}
	return filepath.ToSlash(path), nil
}

func capture(ctx context.Context, repository string, betweenCaptures func() error) (Snapshot, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	before, err := readState(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	beforeSubmodules, err := captureSubmodules(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	tempDir, err := os.MkdirTemp("", "stepan-index-")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(tempDir)
	index := filepath.Join(tempDir, "index")
	captureTree := func() ([]byte, error) {
		// Reset stat data so same-size/timestamp changes are hashed again.
		if _, err := git(ctx, root, index, "read-tree", before.head); err != nil {
			return nil, err
		}
		if _, err := git(ctx, root, index, "add", "-A", "--", "."); err != nil {
			return nil, err
		}
		return git(ctx, root, index, "write-tree")
	}
	tree, err := captureTree()
	if err != nil {
		return Snapshot{}, err
	}
	if betweenCaptures != nil {
		if err := betweenCaptures(); err != nil {
			return Snapshot{}, err
		}
	}
	confirmedTree, err := captureTree()
	if err != nil {
		return Snapshot{}, err
	}
	after, err := readState(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	afterSubmodules, err := captureSubmodules(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	if !bytes.Equal(tree, confirmedTree) || before.head != after.head || before.headRef != after.headRef || before.indexExists != after.indexExists || !bytes.Equal(before.index, after.index) || !bytes.Equal(before.status, after.status) || beforeSubmodules != afterSubmodules {
		return Snapshot{}, ErrRepositoryDiverged
	}
	return Snapshot{
		HeadOID:        before.head,
		HeadRef:        before.headRef,
		TreeOID:        strings.TrimSpace(string(confirmedTree)),
		IndexHash:      hashIndex(before.index, before.indexExists),
		StatusHash:     hashStatus(before.status),
		SubmodulesHash: beforeSubmodules,
	}, nil
}

func hashIndex(index []byte, exists bool) string {
	marker := byte(0)
	if exists {
		marker = 1
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte{marker})
	_, _ = hash.Write(index)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func hashStatus(status []byte) string {
	hash := sha256.Sum256(status)
	return fmt.Sprintf("%x", hash[:])
}

func repositoryRoot(ctx context.Context, repository string) (string, error) {
	if !filepath.IsAbs(repository) {
		return "", errors.New("repository path must be absolute")
	}
	root, err := git(ctx, repository, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find Git root from %q: %w", repository, err)
	}
	path := strings.TrimSpace(string(root))
	if path == "" {
		return "", errors.New("git returned an empty repository root")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make Git root absolute: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git root: %w", err)
	}
	return filepath.Clean(path), nil
}

// captureSubmodules recursively fingerprints every populated submodule. A
// superproject's status only records that a submodule is dirty; it cannot
// distinguish two different dirty states inside that submodule.
func captureSubmodules(ctx context.Context, repository string) (string, error) {
	entries, err := git(ctx, repository, "", "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, entry := range bytes.Split(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		fields := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(fields) != 2 || !bytes.HasPrefix(fields[0], []byte("160000 ")) {
			continue
		}
		path, err := normalizeRelativePath(string(fields[1]))
		if err != nil {
			return "", fmt.Errorf("invalid submodule path %q: %w", fields[1], err)
		}
		child := filepath.Join(repository, filepath.FromSlash(path))
		childRoot, err := repositoryRoot(ctx, child)
		if err != nil {
			// An unpopulated submodule has no worktree to fingerprint.
			continue
		}
		canonicalChild, err := filepath.EvalSymlinks(child)
		if err != nil {
			return "", fmt.Errorf("canonicalize submodule %q: %w", path, err)
		}
		if filepath.Clean(canonicalChild) != childRoot {
			continue
		}
		snapshot, err := capture(ctx, childRoot, nil)
		if err != nil {
			return "", fmt.Errorf("capture submodule %q: %w", path, err)
		}
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.HeadOID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.HeadRef))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.TreeOID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.IndexHash))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.StatusHash))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(snapshot.SubmodulesHash))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func readState(ctx context.Context, repository string) (repositoryState, error) {
	head, err := git(ctx, repository, "", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return repositoryState{}, err
	}
	headRef, err := git(ctx, repository, "", "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return repositoryState{}, err
	}
	indexPathData, err := git(ctx, repository, "", "rev-parse", "--git-path", "index")
	if err != nil {
		return repositoryState{}, err
	}
	indexPath := strings.TrimSpace(string(indexPathData))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repository, indexPath)
	}
	index, err := os.ReadFile(indexPath)
	indexExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return repositoryState{}, err
	}
	// Status catches worktree state that a synthetic top-level tree cannot
	// represent, notably a dirty nested submodule. Ignored files are omitted:
	// they do not participate in the Git working-copy contract.
	status, err := git(ctx, repository, "", "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return repositoryState{}, err
	}
	return repositoryState{strings.TrimSpace(string(head)), strings.TrimSpace(string(headRef)), index, indexExists, status}, nil
}

func git(ctx context.Context, repository, index string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repository
	command.Env = gitEnvironment(index)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func gitEnvironment(index string) []string {
	blocked := map[string]bool{
		"GIT_DIR":                          true,
		"GIT_WORK_TREE":                    true,
		"GIT_COMMON_DIR":                   true,
		"GIT_INDEX_FILE":                   true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_NAMESPACE":                    true,
		"GIT_REPLACE_REF_BASE":             true,
		"GIT_SHALLOW_FILE":                 true,
		"GIT_CEILING_DIRECTORIES":          true,
		"GIT_OPTIONAL_LOCKS":               true,
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		environment = append(environment, item)
	}
	environment = append(environment, "GIT_OPTIONAL_LOCKS=0")
	if index != "" {
		environment = append(environment, "GIT_INDEX_FILE="+index)
	}
	return environment
}
