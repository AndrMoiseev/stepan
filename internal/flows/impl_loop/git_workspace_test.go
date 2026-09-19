//go:build git_integration

package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

func TestValidateNewStartFindsRootAndUsesConfiguredMainBranch(t *testing.T) {
	t.Parallel()
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	nested := filepath.Join(repository, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	workspace, err := ValidateNewStart(context.Background(), nested, configurationWithMain("refs/heads/main"))
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != (GitWorkspace{Root: wantRoot, Branch: "implementation", MainBranch: "main"}) {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestValidateNewStartFallsBackToLocalOriginHEAD(t *testing.T) {
	t.Parallel()
	repository := newGitWorkspace(t)
	gitFixture(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitFixture(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	switchToBranch(t, repository, "implementation")

	workspace, err := ValidateNewStart(context.Background(), repository, implementationconfig.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	if workspace.MainBranch != "main" || workspace.Branch != "implementation" {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestValidateNewStartRejectsUnknownMainBranchWithConfigurationGuidance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "missing origin HEAD"},
		{name: "dangling origin HEAD", setup: func(t *testing.T, repository string) {
			gitFixture(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/missing")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newGitWorkspace(t)
			switchToBranch(t, repository, "implementation")
			if test.setup != nil {
				test.setup(t, repository)
			}

			_, err := ValidateNewStart(context.Background(), repository, implementationconfig.Configuration{})
			if !errors.Is(err, ErrMainUnknown) || !strings.Contains(err.Error(), "main_branch") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateNewStartRejectsMainBranchFromConfiguredAndFallbackSources(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		configuration implementationconfig.Configuration
		setup         func(*testing.T, string)
	}{
		{name: "configured", configuration: configurationWithMain("main")},
		{name: "origin HEAD", setup: func(t *testing.T, repository string) {
			gitFixture(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
			gitFixture(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newGitWorkspace(t)
			if test.setup != nil {
				test.setup(t, repository)
			}
			_, err := ValidateNewStart(context.Background(), repository, test.configuration)
			if !errors.Is(err, ErrMainBranch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateNewStartRejectsDetachedHEAD(t *testing.T) {
	t.Parallel()
	repository := newGitWorkspace(t)
	gitFixture(t, repository, "checkout", "--detach", "--quiet", "HEAD")

	_, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
	if !errors.Is(err, ErrDetachedHEAD) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateNewStartRejectsDirtyWorkingCopyWithoutChangingItOrBranches(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "unstaged", mutate: func(t *testing.T, repository string) {
			writeGitWorkspaceFile(t, filepath.Join(repository, "tracked.txt"), "changed\n")
		}},
		{name: "staged", mutate: func(t *testing.T, repository string) {
			writeGitWorkspaceFile(t, filepath.Join(repository, "staged.txt"), "staged\n")
			gitFixture(t, repository, "add", "--", "staged.txt")
		}},
		{name: "untracked", mutate: func(t *testing.T, repository string) {
			writeGitWorkspaceFile(t, filepath.Join(repository, "untracked.txt"), "untracked\n")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newGitWorkspace(t)
			switchToBranch(t, repository, "implementation")
			test.mutate(t, repository)
			before := inspectWorkspace(t, repository)

			_, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
			if !errors.Is(err, ErrNewStartDirty) {
				t.Fatalf("error = %v", err)
			}
			if after := inspectWorkspace(t, repository); !reflect.DeepEqual(after, before) {
				t.Fatalf("preflight changed workspace:\n before: %#v\n  after: %#v", before, after)
			}
		})
	}
}

func TestValidateNewStartRejectsInvalidConfiguredMainBranch(t *testing.T) {
	t.Parallel()
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	for _, mainBranch := range []string{"null", `""`, "[]", `" main"`, `"main "`, `"feature branch"`, `"refs/heads/"`, `"refs/tags/main"`} {
		_, err := ValidateNewStart(context.Background(), repository, implementationconfig.Configuration{MainBranch: json.RawMessage(mainBranch)})
		if !errors.Is(err, ErrMainUnknown) || !strings.Contains(err.Error(), "main_branch") {
			t.Fatalf("main_branch %s error = %v", mainBranch, err)
		}
	}
}

func TestValidateNewStartUsesLocalBranchWhenTagHasTheSameName(t *testing.T) {
	t.Parallel()
	t.Run("local branch wins over tag", func(t *testing.T) {
		repository := newGitWorkspace(t)
		gitFixture(t, repository, "tag", "main")

		_, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
		if !errors.Is(err, ErrMainBranch) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("tag alone does not invalidate configured branch name", func(t *testing.T) {
		repository := newGitWorkspace(t)
		gitFixture(t, repository, "tag", "main")
		switchToBranch(t, repository, "implementation")
		gitFixture(t, repository, "branch", "--delete", "main")

		workspace, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
		if err != nil || workspace.MainBranch != "main" {
			t.Fatalf("workspace = %#v, error = %v", workspace, err)
		}
	})
}

func TestValidateNewStartAcceptsConfiguredMainBranchWithoutLocalRef(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		remoteRef bool
	}{
		{name: "feature-only checkout"},
		{name: "remote-only main", remoteRef: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newGitWorkspace(t)
			if test.remoteRef {
				gitFixture(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
			}
			switchToBranch(t, repository, "implementation")
			gitFixture(t, repository, "branch", "--delete", "main")

			workspace, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
			if err != nil {
				t.Fatal(err)
			}
			if workspace.Branch != "implementation" || workspace.MainBranch != "main" {
				t.Fatalf("workspace = %#v", workspace)
			}
		})
	}
}

func TestValidateNewStartIgnoresRepositorySelectingGitEnvironment(t *testing.T) {
	for _, test := range []struct {
		name  string
		value func(string, string) string
	}{
		{name: "GIT_DIR", value: func(_, alternateGit string) string { return alternateGit }},
		{name: "GIT_WORK_TREE", value: func(alternate, _ string) string { return alternate }},
		{name: "GIT_COMMON_DIR", value: func(_, alternateGit string) string { return alternateGit }},
		{name: "GIT_INDEX_FILE", value: func(_, alternateGit string) string { return filepath.Join(alternateGit, "index") }},
		{name: "GIT_OBJECT_DIRECTORY", value: func(_, alternateGit string) string { return filepath.Join(alternateGit, "objects") }},
		{name: "GIT_ALTERNATE_OBJECT_DIRECTORIES", value: func(_, alternateGit string) string { return filepath.Join(alternateGit, "objects") }},
		{name: "GIT_NAMESPACE", value: func(string, string) string { return "redirected" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := newGitWorkspace(t)
			switchToBranch(t, target, "implementation")
			alternate := newGitWorkspace(t)
			writeGitWorkspaceFile(t, filepath.Join(alternate, "tracked.txt"), "dirty alternative\n")
			alternateGit := strings.TrimSpace(gitFixture(t, alternate, "rev-parse", "--absolute-git-dir"))
			targetIndex := strings.TrimSpace(gitFixture(t, target, "rev-parse", "--git-path", "index"))
			if !filepath.IsAbs(targetIndex) {
				targetIndex = filepath.Join(target, targetIndex)
			}
			before, err := os.ReadFile(targetIndex)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(test.name, test.value(alternate, alternateGit))

			workspace, err := ValidateNewStart(context.Background(), target, configurationWithMain("main"))
			if err != nil {
				t.Fatal(err)
			}
			if workspace.Branch != "implementation" || workspace.MainBranch != "main" || workspace.Root != target {
				t.Fatalf("workspace = %#v, want target %q implementation/main", workspace, target)
			}
			after, err := os.ReadFile(targetIndex)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("preflight changed the real Git index")
			}
		})
	}
}

func TestValidateNewStartDoesNotHonorConfiguredSubmoduleIgnores(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		ignore string
		mutate func(*testing.T, string)
	}{
		{name: "content ignored as all", ignore: "all", mutate: dirtySubmoduleContent},
		{name: "content ignored as dirty", ignore: "dirty", mutate: dirtySubmoduleContent},
		{name: "commit ignored as all", ignore: "all", mutate: advanceSubmoduleCommit},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, submodule := newGitWorkspaceWithSubmodule(t)
			gitFixture(t, repository, "config", "submodule.module.ignore", test.ignore)
			test.mutate(t, submodule)
			if ignored := gitFixture(t, repository, "status", "--porcelain=v1"); ignored != "" {
				t.Fatalf("fixture is not ignored by policy %q: %q", test.ignore, ignored)
			}

			_, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
			if !errors.Is(err, ErrNewStartDirty) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type workspaceInspection struct {
	head, branch, status, worktrees string
	branches                        []string
}

func inspectWorkspace(t *testing.T, repository string) workspaceInspection {
	t.Helper()
	return workspaceInspection{
		head:      gitFixture(t, repository, "rev-parse", "HEAD"),
		branch:    gitFixture(t, repository, "symbolic-ref", "--short", "HEAD"),
		status:    gitFixture(t, repository, "status", "--porcelain=v1", "-z", "--untracked-files=all"),
		worktrees: gitFixture(t, repository, "worktree", "list", "--porcelain"),
		branches:  strings.Fields(gitFixture(t, repository, "for-each-ref", "--format=%(refname:short)", "refs/heads")),
	}
}

func configurationWithMain(branch string) implementationconfig.Configuration {
	encoded, _ := json.Marshal(branch)
	return implementationconfig.Configuration{MainBranch: encoded}
}

func newGitWorkspaceWithSubmodule(t *testing.T) (string, string) {
	t.Helper()
	repository := newGitWorkspace(t)
	submodule := filepath.Join(repository, "modules", "module")
	gitFixture(t, repository, "init", "--quiet", "--initial-branch=main", submodule)
	gitFixture(t, submodule, "config", "user.name", "Stepan Tests")
	gitFixture(t, submodule, "config", "user.email", "stepan-tests@example.invalid")
	writeGitWorkspaceFile(t, filepath.Join(submodule, "tracked.txt"), "initial\n")
	gitFixture(t, submodule, "add", "--", "tracked.txt")
	gitFixture(t, submodule, "commit", "--quiet", "-m", "initial")
	head := strings.TrimSpace(gitFixture(t, submodule, "rev-parse", "HEAD"))
	writeGitWorkspaceFile(t, filepath.Join(repository, ".gitmodules"), "[submodule \"module\"]\n\tpath = modules/module\n\turl = ./module\n")
	gitFixture(t, repository, "add", "--", ".gitmodules")
	gitFixture(t, repository, "update-index", "--add", "--cacheinfo", "160000,"+head+",modules/module")
	gitFixture(t, repository, "commit", "--quiet", "-m", "add module")
	switchToBranch(t, repository, "implementation")
	return repository, submodule
}

func dirtySubmoduleContent(t *testing.T, submodule string) {
	t.Helper()
	writeGitWorkspaceFile(t, filepath.Join(submodule, "tracked.txt"), "dirty content\n")
}

func advanceSubmoduleCommit(t *testing.T, submodule string) {
	t.Helper()
	writeGitWorkspaceFile(t, filepath.Join(submodule, "tracked.txt"), "next commit\n")
	gitFixture(t, submodule, "config", "user.name", "Stepan Tests")
	gitFixture(t, submodule, "config", "user.email", "stepan-tests@example.invalid")
	gitFixture(t, submodule, "add", "--", "tracked.txt")
	gitFixture(t, submodule, "commit", "--quiet", "-m", "next")
}

func switchToBranch(t *testing.T, repository, branch string) {
	t.Helper()
	gitFixture(t, repository, "switch", "--quiet", "-c", branch)
}
