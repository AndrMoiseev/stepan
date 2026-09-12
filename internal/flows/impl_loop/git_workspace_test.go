package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

func TestValidateNewStartFindsRootAndUsesConfiguredMainBranch(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	nested := filepath.Join(repository, "nested", "directory")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	workspace, err := ValidateNewStart(context.Background(), nested, configurationWithMain("main"))
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
	repository := newGitWorkspace(t)
	git(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
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
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "missing origin HEAD"},
		{name: "dangling origin HEAD", setup: func(t *testing.T, repository string) {
			git(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/missing")
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
	for _, test := range []struct {
		name          string
		configuration implementationconfig.Configuration
		setup         func(*testing.T, string)
	}{
		{name: "configured", configuration: configurationWithMain("main")},
		{name: "origin HEAD", setup: func(t *testing.T, repository string) {
			git(t, repository, "update-ref", "refs/remotes/origin/main", "HEAD")
			git(t, repository, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
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
	repository := newGitWorkspace(t)
	git(t, repository, "checkout", "--detach", "--quiet", "HEAD")

	_, err := ValidateNewStart(context.Background(), repository, configurationWithMain("main"))
	if !errors.Is(err, ErrDetachedHEAD) {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateNewStartRejectsDirtyWorkingCopyWithoutChangingItOrBranches(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "unstaged", mutate: func(t *testing.T, repository string) {
			writeGitWorkspaceFile(t, filepath.Join(repository, "tracked.txt"), "changed\n")
		}},
		{name: "staged", mutate: func(t *testing.T, repository string) {
			writeGitWorkspaceFile(t, filepath.Join(repository, "staged.txt"), "staged\n")
			git(t, repository, "add", "--", "staged.txt")
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
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	for _, mainBranch := range []string{"null", `""`, "[]"} {
		_, err := ValidateNewStart(context.Background(), repository, implementationconfig.Configuration{MainBranch: json.RawMessage(mainBranch)})
		if !errors.Is(err, ErrMainUnknown) || !strings.Contains(err.Error(), "main_branch") {
			t.Fatalf("main_branch %s error = %v", mainBranch, err)
		}
	}
}

type workspaceInspection struct {
	head, branch, status, worktrees string
	branches                        []string
}

func inspectWorkspace(t *testing.T, repository string) workspaceInspection {
	t.Helper()
	return workspaceInspection{
		head:      git(t, repository, "rev-parse", "HEAD"),
		branch:    git(t, repository, "symbolic-ref", "--short", "HEAD"),
		status:    git(t, repository, "status", "--porcelain=v1", "-z", "--untracked-files=all"),
		worktrees: git(t, repository, "worktree", "list", "--porcelain"),
		branches:  strings.Fields(git(t, repository, "for-each-ref", "--format=%(refname:short)", "refs/heads")),
	}
}

func configurationWithMain(branch string) implementationconfig.Configuration {
	encoded, _ := json.Marshal(branch)
	return implementationconfig.Configuration{MainBranch: encoded}
}

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	git(t, repository, "init", "--quiet", "--initial-branch=main")
	git(t, repository, "config", "user.name", "Stepan Tests")
	git(t, repository, "config", "user.email", "stepan-tests@example.invalid")
	writeGitWorkspaceFile(t, filepath.Join(repository, "tracked.txt"), "initial\n")
	git(t, repository, "add", "--", "tracked.txt")
	git(t, repository, "commit", "--quiet", "-m", "initial")
	return repository
}

func switchToBranch(t *testing.T, repository, branch string) {
	t.Helper()
	git(t, repository, "switch", "--quiet", "-c", branch)
}

func writeGitWorkspaceFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
