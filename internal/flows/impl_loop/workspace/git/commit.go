package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/git"
)

var (
	_ workspace.Committer      = Control{}
	_ workspace.CommitObserver = Control{}
)

func (Control) Commit(ctx context.Context, repository, message string) (workspace.CommitObservation, error) {
	if _, err := runGitMutation(ctx, repository, "add", "--all"); err != nil {
		return workspace.CommitObservation{}, err
	}
	if _, err := runGitMutation(ctx, repository, "commit", "-m", message); err != nil {
		return workspace.CommitObservation{}, err
	}
	commitID, err := runGitMutation(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	parent, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	tree, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	observedMessage, err := runGitMutation(ctx, repository, "show", "-s", "--format=%B", "HEAD")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	worktree, err := git.Capture(ctx, repository)
	if err != nil {
		return workspace.CommitObservation{}, fmt.Errorf("capture working copy after commit: %w", err)
	}
	return workspace.CommitObservation{
		CommitID: strings.TrimSpace(string(commitID)), ParentCommit: strings.TrimSpace(string(parent)),
		Tree: strings.TrimSpace(string(tree)), Message: strings.TrimRight(string(observedMessage), "\r\n"), Worktree: worktree,
	}, nil
}

func runGitMutation(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	return runGitMutationWithEnvironment(ctx, directory, gitMutationEnvironment(), arguments...)
}

func runGitMutationWithEnvironment(ctx context.Context, directory string, environment []string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// gitMutationEnvironment has the same repository-selection hardening as the
// read-only helper but intentionally omits GIT_OPTIONAL_LOCKS=0: index writes
// are required for a real commit.
func gitMutationEnvironment() []string {
	blocked := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
		"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
		"GIT_REPLACE_REF_BASE": true, "GIT_SHALLOW_FILE": true, "GIT_CEILING_DIRECTORIES": true,
		"GIT_OPTIONAL_LOCKS": true,
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		environment = append(environment, item)
	}
	return environment
}

func (Control) Observe(ctx context.Context, repository string) (workspace.CommitObservation, error) {
	commitID, err := runGitMutation(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	parents, err := runGitMutation(ctx, repository, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	parentFields := strings.Fields(string(parents))
	if len(parentFields) != 1 && len(parentFields) != 2 {
		return workspace.CommitObservation{}, errors.New("HEAD has an unexpected parent list")
	}
	if parentFields[0] != strings.TrimSpace(string(commitID)) {
		return workspace.CommitObservation{}, errors.New("HEAD parent list does not start with HEAD")
	}
	parent := ""
	if len(parentFields) == 2 {
		parent = parentFields[1]
	}
	tree, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	message, err := runGitMutation(ctx, repository, "show", "-s", "--format=%B", "HEAD")
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	worktree, err := git.Capture(ctx, repository)
	if err != nil {
		return workspace.CommitObservation{}, fmt.Errorf("capture working copy: %w", err)
	}
	return workspace.CommitObservation{
		CommitID: strings.TrimSpace(string(commitID)), ParentCommit: parent,
		Tree: strings.TrimSpace(string(tree)), Message: strings.TrimRight(string(message), "\r\n"), Worktree: worktree,
	}, nil
}
