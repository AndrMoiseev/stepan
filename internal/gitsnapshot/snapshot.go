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

type Snapshot struct {
	HeadOID string `json:"head_oid"`
	TreeOID string `json:"tree_oid"`
}

type repositoryState struct {
	head        string
	index       []byte
	indexExists bool
	status      []byte
}

func Capture(ctx context.Context, repository string) (Snapshot, error) {
	return capture(ctx, repository, nil)
}

func capture(ctx context.Context, repository string, afterAdd func() error) (Snapshot, error) {
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
	if _, err := git(ctx, repository, index, "read-tree", "HEAD"); err != nil {
		return Snapshot{}, err
	}
	if _, err := git(ctx, repository, index, "add", "-A", "--", "."); err != nil {
		return Snapshot{}, err
	}
	if afterAdd != nil {
		if err := afterAdd(); err != nil {
			return Snapshot{}, err
		}
	}
	tree, err := git(ctx, repository, index, "write-tree")
	if err != nil {
		return Snapshot{}, err
	}
	after, err := readState(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	if before.head != after.head || before.indexExists != after.indexExists || !bytes.Equal(before.index, after.index) || !bytes.Equal(before.status, after.status) {
		return Snapshot{}, ErrRepositoryDiverged
	}
	return Snapshot{HeadOID: before.head, TreeOID: strings.TrimSpace(string(tree))}, nil
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
