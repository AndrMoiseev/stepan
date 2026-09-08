package gitsnapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
			changed, err := os.Stat(path)
			if err != nil || changed.Size() != info.Size() || !changed.ModTime().Equal(info.ModTime()) {
				t.Fatalf("same-size/timestamp precondition failed: %v, %v", changed, err)
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
	write(t, path, "unstaged\n")
	write(t, filepath.Join(repo, "untracked-данные.txt"), "untracked\n")
	write(t, filepath.Join(repo, "ignored.tmp"), "ignored\n")
	runGit(t, repo, "pack-refs", "--all")
	stateBefore := readRepositoryBytes(t, repo)
	snapshot := mustCapture(t, repo)
	content := runGit(t, repo, "show", snapshot.TreeOID+":tracked.txt")
	if content != "unstaged\n" {
		t.Fatalf("candidate content = %q", content)
	}
	if stateAfter := readRepositoryBytes(t, repo); !reflect.DeepEqual(stateBefore, stateAfter) {
		t.Fatal("capture changed real index, HEAD, refs, or working tree bytes")
	}
}

func TestCaptureDetectsMutationAndRecalculation(t *testing.T) {
	repo := newRepository(t)
	before := mustCapture(t, repo)
	path := filepath.Join(repo, "tracked.txt")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = capture(context.Background(), repo, func() error {
		if err := os.WriteFile(path, []byte("mutation\n"), 0o600); err != nil {
			return err
		}
		return os.Chtimes(path, info.ModTime(), info.ModTime())
	})
	if !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("error = %v", err)
	}
	after := mustCapture(t, repo)
	if after.TreeOID == before.TreeOID {
		t.Fatalf("recalculation did not observe mutation: %+v, %+v", before, after)
	}
}

func TestCompareAndCheckBoundary(t *testing.T) {
	repo := newRepository(t)
	write(t, filepath.Join(repo, "outside-before.txt"), "dirty before baseline\n")
	before := mustCapture(t, repo)

	write(t, filepath.Join(repo, "docs", "changes", "features", "feature", "specification.md"), "inside\n")
	after := mustCapture(t, repo)
	paths, err := Compare(context.Background(), repo, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/changes/features/feature/specification.md"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("changed paths = %q, want %q", paths, want)
	}
	if err := CheckBoundary(paths, "docs/changes/features/feature"); err != nil {
		t.Fatal(err)
	}
}

func TestCompareReportsAllOutsideChanges(t *testing.T) {
	repo := newRepository(t)
	before := mustCapture(t, repo)
	write(t, filepath.Join(repo, "docs", "changes", "features", "feature", "specification.md"), "inside\n")
	write(t, filepath.Join(repo, "outside.txt"), "outside\n")
	remove(t, filepath.Join(repo, "tracked.txt"))
	if err := os.Rename(filepath.Join(repo, "rename.txt"), filepath.Join(repo, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	after := mustCapture(t, repo)

	paths, err := Compare(context.Background(), repo, before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/changes/features/feature/specification.md", "outside.txt", "rename.txt", "renamed.txt", "tracked.txt"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("changed paths = %q, want %q", paths, want)
	}
	err = CheckBoundary(paths, "docs/changes/features/feature")
	var boundaryErr *BoundaryError
	if !errors.As(err, &boundaryErr) || !errors.Is(err, ErrOutsideBoundary) {
		t.Fatalf("error = %v", err)
	}
	if want := []string{"outside.txt", "rename.txt", "renamed.txt", "tracked.txt"}; !reflect.DeepEqual(boundaryErr.Paths, want) {
		t.Fatalf("outside paths = %q, want %q", boundaryErr.Paths, want)
	}
}

func TestCompareRejectsChangedHead(t *testing.T) {
	repo := newRepository(t)
	before := mustCapture(t, repo)
	write(t, filepath.Join(repo, "tracked.txt"), "new commit\n")
	runGit(t, repo, "add", "tracked.txt")
	runGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "next")
	after := mustCapture(t, repo)
	if _, err := Compare(context.Background(), repo, before, after); !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckBoundaryRejectsUnsafePaths(t *testing.T) {
	for _, path := range []string{"", ".", "../outside", filepath.Join(string(filepath.Separator), "outside")} {
		if err := CheckBoundary([]string{path}, "docs/changes/features/feature"); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
	if err := CheckBoundary([]string{"docs/changes/features/feature-file"}, "docs/changes/features/feature"); !errors.Is(err, ErrOutsideBoundary) {
		t.Fatalf("sibling-prefix error = %v", err)
	}
}

type repositoryBytes struct {
	index, head []byte
	refs, tree  map[string][]byte
}

func readRepositoryBytes(t *testing.T, repo string) repositoryBytes {
	t.Helper()
	gitDir := strings.TrimSpace(runGit(t, repo, "rev-parse", "--absolute-git-dir"))
	refs := readFiles(t, filepath.Join(gitDir, "refs"), "")
	if packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
		refs["packed-refs"] = packed
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	return repositoryBytes{readIndex(t, repo), head, refs, readFiles(t, repo, ".git")}
}

func readFiles(t *testing.T, root, skipDir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && entry.Name() == skipDir {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err == nil {
			files[relative] = data
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
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
