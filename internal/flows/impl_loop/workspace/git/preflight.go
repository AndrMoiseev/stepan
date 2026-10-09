package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

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

func gitValidateNewStart(ctx context.Context, control workspace.Repository, start string, configuration setting.Configuration) (workspace.Identity, error) {
	root, err := control.FindRoot(ctx, start)
	if err != nil {
		return workspace.Identity{}, err
	}
	if err := requireCleanWorkingCopy(ctx, root); err != nil {
		return workspace.Identity{}, err
	}
	branch, err := currentBranch(ctx, root)
	if err != nil {
		return workspace.Identity{}, err
	}
	configuredMain, err := configuration.MainBranchName()
	if err != nil {
		return workspace.Identity{}, fmt.Errorf("%w: %v", workspace.ErrMainUnknown, err)
	}
	mainBranch, err := resolveMainBranch(ctx, root, configuredMain)
	if err != nil {
		return workspace.Identity{}, err
	}
	if branch == mainBranch {
		return workspace.Identity{}, fmt.Errorf("%w: current branch %q", workspace.ErrMainBranch, branch)
	}
	return workspace.Identity{Root: root, Branch: branch, MainBranch: mainBranch}, nil
}

func requireCleanWorkingCopy(ctx context.Context, root string) error {
	status, err := runGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("inspect Git working copy: %w", err)
	}
	if len(status) != 0 {
		return workspace.ErrNewStartDirty
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
		return "", fmt.Errorf("%w: check out a local implementation branch", workspace.ErrDetachedHEAD)
	}
	return branch, nil
}

func resolveMainBranch(ctx context.Context, root, configured string) (string, error) {
	if configured != "" {
		branch, err := localBranchName(ctx, root, configured)
		if err != nil {
			return "", err
		}
		return branch, nil
	}

	output, err := runGit(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("%w: set project flows.impl_loop.main_branch because refs/remotes/origin/HEAD is unavailable", workspace.ErrMainUnknown)
	}
	fullBranch := strings.TrimSpace(string(output))
	const remotePrefix = "refs/remotes/origin/"
	if !strings.HasPrefix(fullBranch, remotePrefix) || len(fullBranch) == len(remotePrefix) {
		return "", fmt.Errorf("%w: set project flows.impl_loop.main_branch because refs/remotes/origin/HEAD is invalid", workspace.ErrMainUnknown)
	}
	if _, err := runGit(ctx, root, "rev-parse", "--verify", fullBranch+"^{commit}"); err != nil {
		return "", fmt.Errorf("%w: set project flows.impl_loop.main_branch because refs/remotes/origin/HEAD does not name a local branch", workspace.ErrMainUnknown)
	}
	return strings.TrimPrefix(fullBranch, remotePrefix), nil
}

func localBranchName(ctx context.Context, root, configured string) (string, error) {
	if configured == "" || strings.IndexFunc(configured, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%w: project flows.impl_loop.main_branch must be a non-empty local branch name", workspace.ErrMainUnknown)
	}
	const localPrefix = "refs/heads/"
	branch := configured
	if strings.HasPrefix(branch, localPrefix) {
		branch = strings.TrimPrefix(branch, localPrefix)
	} else if strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("%w: project flows.impl_loop.main_branch must name refs/heads, not %q", workspace.ErrMainUnknown, configured)
	}
	if output, err := runGit(ctx, root, "check-ref-format", "--branch", branch); err != nil || strings.TrimSpace(string(output)) != branch {
		return "", fmt.Errorf("%w: project flows.impl_loop.main_branch is not a valid local branch name", workspace.ErrMainUnknown)
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
