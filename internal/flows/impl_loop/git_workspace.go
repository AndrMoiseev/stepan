package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

var (
	// ErrNewStartDirty means a new implementation run cannot take ownership of
	// a working copy that already has uncommitted work.  It is deliberately a
	// new-run precondition: a later resume validates its saved state instead.
	ErrNewStartDirty = errors.New("implementation new start requires a clean working copy")
	ErrDetachedHEAD  = errors.New("implementation requires a checked-out branch")
	ErrMainUnknown   = errors.New("implementation main branch is unknown")
	ErrMainBranch    = errors.New("implementation cannot run on the main branch")
)

// GitWorkspace is the immutable Git identity accepted for a new
// implementation run. It contains only information observed from Git; this
// preflight never creates a worktree, branch, or checkout.
type GitWorkspace struct {
	Root       string
	Branch     string
	MainBranch string
}

// FindGitRoot resolves the enclosing Git working tree for start. The returned
// path is absolute and canonical so it can safely identify one working copy in
// durable state later in the implementation flow.
func FindGitRoot(ctx context.Context, start string) (string, error) {
	return GitWorkspaceControl{}.FindRoot(ctx, start)
}

func gitFindRoot(ctx context.Context, start string) (string, error) {
	output, err := runGit(ctx, start, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find Git root from %q: %w", start, err)
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("find Git root from %q: git returned an empty path", start)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("make Git root absolute: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git root: %w", err)
	}
	return filepath.Clean(root), nil
}

// ValidateNewStart checks the Git-only prerequisites for starting a new run.
// It intentionally must not be used for resume: an own paused run can retain
// uncommitted implementation work, which is reconciled by later run-state
// work. No Git command here changes repository state.
func ValidateNewStart(ctx context.Context, start string, configuration implementationconfig.Configuration) (GitWorkspace, error) {
	return GitWorkspaceControl{}.ValidateNewStart(ctx, start, configuration)
}

func gitValidateNewStart(ctx context.Context, control RepositoryControl, start string, configuration implementationconfig.Configuration) (GitWorkspace, error) {
	root, err := control.FindRoot(ctx, start)
	if err != nil {
		return GitWorkspace{}, err
	}
	if err := requireCleanWorkingCopy(ctx, root); err != nil {
		return GitWorkspace{}, err
	}
	branch, err := currentBranch(ctx, root)
	if err != nil {
		return GitWorkspace{}, err
	}
	mainBranch, err := resolveMainBranch(ctx, root, configuration.MainBranch)
	if err != nil {
		return GitWorkspace{}, err
	}
	if branch == mainBranch {
		return GitWorkspace{}, fmt.Errorf("%w: current branch %q", ErrMainBranch, branch)
	}
	return GitWorkspace{Root: root, Branch: branch, MainBranch: mainBranch}, nil
}

func requireCleanWorkingCopy(ctx context.Context, root string) error {
	status, err := runGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("inspect Git working copy: %w", err)
	}
	if len(status) != 0 {
		return ErrNewStartDirty
	}
	return nil
}

func currentBranch(ctx context.Context, root string) (string, error) {
	output, err := runGit(ctx, root, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("inspect current Git branch: %w", err)
	}
	branch := strings.TrimSpace(string(output))
	if branch == "" {
		return "", fmt.Errorf("%w: check out a local implementation branch", ErrDetachedHEAD)
	}
	return branch, nil
}

func resolveMainBranch(ctx context.Context, root string, configured json.RawMessage) (string, error) {
	if len(configured) != 0 {
		var branch string
		if err := json.Unmarshal(configured, &branch); err != nil {
			return "", fmt.Errorf("%w: project implementation.main_branch must be a non-empty string", ErrMainUnknown)
		}
		branch, err := localBranchName(ctx, root, branch)
		if err != nil {
			return "", err
		}
		return branch, nil
	}

	output, err := runGit(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: set project implementation.main_branch because refs/remotes/origin/HEAD is unavailable", ErrMainUnknown)
	}
	fullBranch := strings.TrimSpace(string(output))
	const remotePrefix = "refs/remotes/origin/"
	if !strings.HasPrefix(fullBranch, remotePrefix) || len(fullBranch) == len(remotePrefix) {
		return "", fmt.Errorf("%w: set project implementation.main_branch because refs/remotes/origin/HEAD is invalid", ErrMainUnknown)
	}
	if _, err := runGit(ctx, root, "rev-parse", "--verify", fullBranch+"^{commit}"); err != nil {
		return "", fmt.Errorf("%w: set project implementation.main_branch because refs/remotes/origin/HEAD does not name a local branch", ErrMainUnknown)
	}
	return strings.TrimPrefix(fullBranch, remotePrefix), nil
}

func localBranchName(ctx context.Context, root, configured string) (string, error) {
	if configured == "" || strings.IndexFunc(configured, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w: project implementation.main_branch must be a non-empty local branch name", ErrMainUnknown)
	}
	const localPrefix = "refs/heads/"
	branch := configured
	if strings.HasPrefix(branch, localPrefix) {
		branch = strings.TrimPrefix(branch, localPrefix)
	} else if strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("%w: project implementation.main_branch must name refs/heads, not %q", ErrMainUnknown, configured)
	}
	if output, err := runGit(ctx, root, "check-ref-format", "--branch", branch); err != nil || strings.TrimSpace(string(output)) != branch {
		return "", fmt.Errorf("%w: project implementation.main_branch is not a valid local branch name", ErrMainUnknown)
	}
	return branch, nil
}

func runGit(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = gitEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// gitEnvironment removes variables that can make a read-only Git command
// inspect another repository, index, object store, or ref namespace. It also
// removes process-level Git configuration injection, which can set those same
// locations indirectly. The preflight's identity must come from directory,
// never its caller's Git process state. GIT_OPTIONAL_LOCKS=0 also keeps
// observation read-only.
func gitEnvironment() []string {
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
	environment := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		environment = append(environment, item)
	}
	return append(environment, "GIT_OPTIONAL_LOCKS=0")
}
