//go:build git_integration

package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/git"
)

func TestEnsureUnchangedDetectsFileAndMetadataChanges(t *testing.T) {
	t.Parallel()
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
			repository := newGitWorkspace(t)
			expected, err := (Control{}).Capture(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			if err := (Control{}).EnsureUnchanged(context.Background(), repository, expected); err != nil {
				t.Fatalf("unchanged workspace rejected: %v", err)
			}
			test.change(t, repository)
			if err := (Control{}).EnsureUnchanged(context.Background(), repository, expected); !errors.Is(err, git.ErrRepositoryDiverged) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEnsureUnchangedRejectsUnverifiableIndex(t *testing.T) {
	t.Parallel()
	repository := newGitWorkspace(t)
	expected, err := (Control{}).Capture(context.Background(), repository)
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
	err = (Control{}).EnsureUnchanged(context.Background(), repository, expected)
	if err == nil || errors.Is(err, git.ErrRepositoryDiverged) {
		t.Fatalf("error = %v, want meaningful verification failure", err)
	}
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
