package gitsnapshot

import (
	"bytes"
	"context"
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
	HeadOID string `json:"head_oid"`
	TreeOID string `json:"tree_oid"`
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
	index       []byte
	indexExists bool
	status      []byte
}

func Capture(ctx context.Context, repository string) (Snapshot, error) {
	return capture(ctx, repository, nil)
}

// Compare returns paths changed between two synthetic trees.
func Compare(ctx context.Context, repository string, before, after Snapshot) ([]string, error) {
	if !filepath.IsAbs(repository) {
		return nil, errors.New("repository path must be absolute")
	}
	if before.HeadOID != after.HeadOID {
		return nil, ErrRepositoryDiverged
	}
	output, err := git(ctx, repository, "", "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", before.TreeOID, after.TreeOID)
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
	if !filepath.IsAbs(repository) {
		return Snapshot{}, errors.New("repository path must be absolute")
	}
	before, err := readState(ctx, repository)
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
		if _, err := git(ctx, repository, index, "read-tree", before.head); err != nil {
			return nil, err
		}
		if _, err := git(ctx, repository, index, "add", "-A", "--", "."); err != nil {
			return nil, err
		}
		return git(ctx, repository, index, "write-tree")
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
	after, err := readState(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	if !bytes.Equal(tree, confirmedTree) || before.head != after.head || before.indexExists != after.indexExists || !bytes.Equal(before.index, after.index) || !bytes.Equal(before.status, after.status) {
		return Snapshot{}, ErrRepositoryDiverged
	}
	return Snapshot{HeadOID: before.head, TreeOID: strings.TrimSpace(string(confirmedTree))}, nil
}

func readState(ctx context.Context, repository string) (repositoryState, error) {
	head, err := git(ctx, repository, "", "rev-parse", "--verify", "HEAD")
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
	status, err := git(ctx, repository, "", "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return repositoryState{}, err
	}
	return repositoryState{strings.TrimSpace(string(head)), index, indexExists, status}, nil
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
	environment := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key := item
		if separator := strings.IndexByte(item, '='); separator >= 0 {
			key = item[:separator]
		}
		if strings.EqualFold(key, "GIT_INDEX_FILE") || strings.EqualFold(key, "GIT_OPTIONAL_LOCKS") {
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
