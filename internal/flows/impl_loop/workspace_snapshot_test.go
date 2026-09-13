package impl_loop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
