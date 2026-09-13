package impl_loop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestCheckWorkspaceBeforeOperationPausesOnUnexpectedChange(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"file", func(t *testing.T, repository string) {
			if err := os.WriteFile(filepath.Join(repository, "unexpected.txt"), []byte("manual edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"index", func(t *testing.T, repository string) {
			if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("staged edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runSnapshotGit(t, repository, "add", "tracked.txt")
		}},
		{"head", func(t *testing.T, repository string) {
			if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("committed edit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runSnapshotGit(t, repository, "add", "tracked.txt")
			runSnapshotGit(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "unexpected")
		}},
		{"same OID branch switch", func(t *testing.T, repository string) {
			runSnapshotGit(t, repository, "checkout", "--quiet", "-b", "same-oid")
		}},
		{"same OID detach", func(t *testing.T, repository string) {
			runSnapshotGit(t, repository, "checkout", "--quiet", "--detach")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newSnapshotRepository(t)
			expected, err := gitsnapshot.Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			run := &implementationstate.Run{Status: implementationstate.RunActive}
			if err := CheckWorkspaceBeforeOperation(context.Background(), repository, expected, run); err != nil {
				t.Fatalf("unchanged workspace rejected: %v", err)
			}
			test.change(t, repository)
			if err := CheckWorkspaceBeforeOperation(context.Background(), repository, expected, run); !errors.Is(err, gitsnapshot.ErrRepositoryDiverged) {
				t.Fatalf("error = %v", err)
			}
			if run.Status != implementationstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
				t.Fatalf("run was not paused for divergence: %#v", run)
			}
		})
	}
}

func TestCheckWorkspaceBeforeOperationPausesWhenIndexCannotBeVerified(t *testing.T) {
	repository := newSnapshotRepository(t)
	expected, err := gitsnapshot.Capture(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := strings.TrimSpace(runSnapshotGitOutput(t, repository, "rev-parse", "--git-path", "index"))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repository, indexPath)
	}
	if err := os.WriteFile(indexPath, []byte("not a Git index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &implementationstate.Run{Status: implementationstate.RunActive}
	err = CheckWorkspaceBeforeOperation(context.Background(), repository, expected, run)
	if err == nil || errors.Is(err, gitsnapshot.ErrRepositoryDiverged) {
		t.Fatalf("error = %v, want meaningful verification failure", err)
	}
	if run.Status != implementationstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
		t.Fatalf("run was not paused for unverifiable index: %#v", run)
	}
}

func TestCheckWorkspaceBeforeOperationPausesOnSubmoduleCycle(t *testing.T) {
	original := ensureWorkspaceUnchanged
	ensureWorkspaceUnchanged = func(context.Context, string, gitsnapshot.Snapshot) error {
		return &gitsnapshot.SubmoduleCycleError{Root: "cycle"}
	}
	t.Cleanup(func() { ensureWorkspaceUnchanged = original })

	run := &implementationstate.Run{Status: implementationstate.RunActive}
	err := CheckWorkspaceBeforeOperation(context.Background(), t.TempDir(), gitsnapshot.Snapshot{}, run)
	if !errors.Is(err, gitsnapshot.ErrSubmoduleCycle) {
		t.Fatalf("error = %v", err)
	}
	if run.Status != implementationstate.RunPaused || run.PauseReason != unexpectedWorkspaceChangePauseReason {
		t.Fatalf("run was not paused for submodule cycle: %#v", run)
	}
}

func newSnapshotRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runSnapshotGit(t, repository, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runSnapshotGit(t, repository, "add", "tracked.txt")
	runSnapshotGit(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	return repository
}

func runSnapshotGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	_ = runSnapshotGitOutput(t, repository, args...)
}

func runSnapshotGitOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
