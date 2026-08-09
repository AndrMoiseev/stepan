package gitsnapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureCandidateChanges(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"tracked", func(t *testing.T, repo string) { write(t, filepath.Join(repo, "tracked.txt"), "changed\n") }},
		{"untracked", func(t *testing.T, repo string) { write(t, filepath.Join(repo, "new.txt"), "new\n") }},
		{"deleted", func(t *testing.T, repo string) { remove(t, filepath.Join(repo, "tracked.txt")) }},
		{"renamed", func(t *testing.T, repo string) {
			if err := os.Rename(filepath.Join(repo, "rename.txt"), filepath.Join(repo, "renamed.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unicode path", func(t *testing.T, repo string) {
			path := filepath.Join(repo, "папка с пробелом", "файл &[].txt")
			if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, path, "данные\n")
		}},
		{"same size and timestamp", func(t *testing.T, repo string) {
			path := filepath.Join(repo, "same.txt")
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			write(t, path, "BBBB\n")
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t)
			before := mustCapture(t, repo)
			indexBefore := readIndex(t, repo)
			test.change(t, repo)
			after := mustCapture(t, repo)
			if after.HeadOID != before.HeadOID || after.TreeOID == before.TreeOID {
				t.Fatalf("before = %+v, after = %+v", before, after)
			}
			if !bytes.Equal(indexBefore, readIndex(t, repo)) {
				t.Fatal("real index changed")
			}
		})
	}
}

func TestCaptureIsStableAndIgnoresIgnoredFiles(t *testing.T) {
	repo := newRepository(t)
	indexBefore := readIndex(t, repo)
	first := mustCapture(t, repo)
	second := mustCapture(t, repo)
	if first != second {
		t.Fatalf("snapshots differ: %+v, %+v", first, second)
	}
	write(t, filepath.Join(repo, "ignored.tmp"), "ignored\n")
	third := mustCapture(t, repo)
	if third != first {
		t.Fatalf("ignored file changed snapshot: %+v, %+v", first, third)
	}
	if !bytes.Equal(indexBefore, readIndex(t, repo)) {
		t.Fatal("real index changed")
	}
}

func TestCaptureUsesWorkingTreeInsteadOfRealIndex(t *testing.T) {
	repo := newRepository(t)
	path := filepath.Join(repo, "tracked.txt")
	write(t, path, "staged\n")
	runGit(t, repo, "add", "tracked.txt")
	indexBefore := readIndex(t, repo)
	write(t, path, "unstaged\n")
	snapshot := mustCapture(t, repo)
	content := runGit(t, repo, "show", snapshot.TreeOID+":tracked.txt")
	if content != "unstaged\n" {
		t.Fatalf("candidate content = %q", content)
	}
	if !bytes.Equal(indexBefore, readIndex(t, repo)) {
		t.Fatal("staged real index changed")
	}
}

func TestCaptureDetectsMutationAndRecalculation(t *testing.T) {
	repo := newRepository(t)
	before := mustCapture(t, repo)
	path := filepath.Join(repo, "tracked.txt")
	_, err := capture(context.Background(), repo, func() error {
		return os.WriteFile(path, []byte("mutated during capture\n"), 0o600)
	})
	if !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("error = %v", err)
	}
	after := mustCapture(t, repo)
	if after.TreeOID == before.TreeOID {
		t.Fatalf("recalculation did not observe mutation: %+v, %+v", before, after)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "--quiet")
	write(t, filepath.Join(repo, ".gitignore"), "*.tmp\n")
	write(t, filepath.Join(repo, "tracked.txt"), "original\n")
	write(t, filepath.Join(repo, "rename.txt"), "rename\n")
	write(t, filepath.Join(repo, "same.txt"), "AAAA\n")
	runGit(t, repo, "add", "-A")
	command := exec.Command("git", "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	command.Dir = repo
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	return repo
}

func mustCapture(t *testing.T, repo string) Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := Capture(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func readIndex(t *testing.T, repo string) []byte {
	t.Helper()
	path := strings.TrimSpace(runGit(t, repo, "rev-parse", "--git-path", "index"))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repo
	command.Env = gitEnvironment("")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
