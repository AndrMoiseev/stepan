//go:build git_integration

package gitsnapshot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCaptureCandidateChanges(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	repo := newRepository(t)
	indexBefore := readIndex(t, repo)
	first := mustCapture(t, repo)
	second := mustCapture(t, repo)
	if !sameSnapshot(first, second) {
		t.Fatalf("snapshots differ: %+v, %+v", first, second)
	}
	write(t, filepath.Join(repo, "ignored.tmp"), "ignored\n")
	third := mustCapture(t, repo)
	if !sameSnapshot(third, first) {
		t.Fatalf("ignored file changed snapshot: %+v, %+v", first, third)
	}
	if !bytes.Equal(indexBefore, readIndex(t, repo)) {
		t.Fatal("real index changed")
	}
}

func TestCaptureUsesWorkingTreeInsteadOfRealIndex(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestCaptureDetectsRealStateMutationDuringSecondHierarchyPass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"index only", func(t *testing.T, repository string) {
			runGit(t, repository, "update-index", "--assume-unchanged", "tracked.txt")
		}},
		{"symbolic HEAD only", func(t *testing.T, repository string) {
			runGit(t, repository, "branch", "same-oid")
			gitDirectory := strings.TrimSpace(runGit(t, repository, "rev-parse", "--absolute-git-dir"))
			if err := os.WriteFile(filepath.Join(gitDirectory, "HEAD"), []byte("ref: refs/heads/same-oid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newRepository(t)
			statusReads := 0
			mutated := false
			replaceBeforeGitCommandHook(t, func(_ string, index string, args []string) {
				if index == "" && len(args) != 0 && args[0] == "status" {
					statusReads++
				}
				// The third status command is the second hierarchy pass's initial
				// readState. Mutating before its temporary-index read-tree models
				// the exact race that the closing verification must reject.
				if !mutated && statusReads == 3 && index != "" && len(args) != 0 && args[0] == "read-tree" {
					mutated = true
					test.mutate(t, repository)
				}
			})
			_, err := Capture(context.Background(), repository)
			if !mutated || !errors.Is(err, ErrRepositoryDiverged) {
				t.Fatalf("mutated = %t, error = %v", mutated, err)
			}
		})
	}
}

func TestCompareAndCheckBoundary(t *testing.T) {
	t.Parallel()
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

func TestDiffAttributesOnlyCurrentInvocationChanges(t *testing.T) {
	t.Parallel()
	repo := newRepository(t)
	write(t, filepath.Join(repo, "prior-uncommitted.txt"), "belongs to user\n")
	before := mustCapture(t, repo)

	write(t, filepath.Join(repo, "current-invocation.txt"), "belongs to operation\n")
	after := mustCapture(t, repo)
	difference, err := Diff(context.Background(), repo, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"current-invocation.txt"}; !reflect.DeepEqual(difference.Paths, want) {
		t.Fatalf("paths = %q, want %q", difference.Paths, want)
	}
	if difference.HeadChanged || difference.IndexChanged || !difference.StatusChanged {
		t.Fatalf("unexpected non-file difference: %+v", difference)
	}
}

func TestDiffReportsHeadAndIndexFingerprints(t *testing.T) {
	t.Parallel()
	t.Run("index", func(t *testing.T) {
		repo := newRepository(t)
		write(t, filepath.Join(repo, "tracked.txt"), "unstaged\n")
		before := mustCapture(t, repo)
		runGit(t, repo, "add", "tracked.txt")
		after := mustCapture(t, repo)
		difference, err := Diff(context.Background(), repo, before, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(difference.Paths) != 0 || difference.HeadChanged || !difference.IndexChanged || !difference.StatusChanged {
			t.Fatalf("difference = %+v", difference)
		}
	})

	t.Run("head", func(t *testing.T) {
		repo := newRepository(t)
		before := mustCapture(t, repo)
		write(t, filepath.Join(repo, "tracked.txt"), "committed\n")
		runGit(t, repo, "add", "tracked.txt")
		runGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "operation")
		after := mustCapture(t, repo)
		difference, err := Diff(context.Background(), repo, before, after)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"tracked.txt"}; !reflect.DeepEqual(difference.Paths, want) || !difference.HeadChanged || !difference.IndexChanged || difference.StatusChanged {
			t.Fatalf("difference = %+v, want paths %q and changed fingerprints", difference, want)
		}
	})
}

func TestRestorePathsPreservesExactBytesAcrossGitFilters(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	runGit(t, repository, "config", "core.autocrlf", "true")
	runGit(t, repository, "config", "filter.stepan.clean", "tr -d '\\r'")
	runGit(t, repository, "config", "filter.stepan.smudge", "cat")
	write(t, filepath.Join(repository, ".gitattributes"), "crlf.txt text\nfiltered.txt filter=stepan\n")
	write(t, filepath.Join(repository, "crlf.txt"), "crlf before\r\n")
	write(t, filepath.Join(repository, "filtered.txt"), "filter before\r\n")
	runGit(t, repository, "add", ".gitattributes", "crlf.txt", "filtered.txt")
	runGit(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "filtered baseline")

	before := mustCapture(t, repository)
	for _, path := range []string{"crlf.txt", "filtered.txt"} {
		if blob := runGit(t, repository, "show", before.TreeOID+":"+path); strings.Contains(blob, "\r") {
			t.Fatalf("filtered synthetic blob for %s retained CRLF: %q", path, blob)
		}
	}
	write(t, filepath.Join(repository, "crlf.txt"), "agent changed\n")
	write(t, filepath.Join(repository, "filtered.txt"), "agent changed\n")
	after := mustCapture(t, repository)
	if _, err := RestorePaths(context.Background(), repository, before, after, []string{"crlf.txt", "filtered.txt"}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		path, contents string
	}{{"crlf.txt", "crlf before\r\n"}, {"filtered.txt", "filter before\r\n"}} {
		contents, err := os.ReadFile(filepath.Join(repository, expected.path))
		if err != nil || string(contents) != expected.contents {
			t.Fatalf("restored %s = %q, %v; want exact %q", expected.path, contents, err, expected.contents)
		}
	}
}

func TestRestorePathsRestoresExecutableMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix executable mode")
	}
	repository := newRepository(t)
	path := filepath.Join(repository, "executable.sh")
	write(t, path, "#!/bin/sh\necho before\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "executable.sh")
	runGit(t, repository, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "executable baseline")
	before := mustCapture(t, repository)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(t, path, "#!/bin/sh\necho changed\n")
	after := mustCapture(t, repository)
	if _, err := RestorePaths(context.Background(), repository, before, after, []string{"executable.sh"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("restored mode = %o, %v; want 755", info.Mode().Perm(), err)
	}
}

func TestEnsureUnchangedDetectsDirtySubmodule(t *testing.T) {
	t.Parallel()
	repo := newRepository(t)
	child := newRepository(t)
	childHead := strings.TrimSpace(runGit(t, child, "rev-parse", "HEAD"))
	runGit(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+childHead+",nested")
	runGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "add nested repository")
	if err := os.Rename(child, filepath.Join(repo, "nested")); err != nil {
		t.Fatal(err)
	}

	clean := mustCapture(t, repo)
	if _, recordedAsFile := clean.worktreeFiles["nested"]; recordedAsFile {
		t.Fatal("gitlink was incorrectly recorded as a restorable regular file")
	}
	write(t, filepath.Join(repo, "nested", "tracked.txt"), "first manual edit\n")
	if err := EnsureUnchanged(context.Background(), repo, clean); !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("error = %v", err)
	}
	expected := mustCapture(t, repo)
	write(t, filepath.Join(repo, "nested", "tracked.txt"), "second manual edit\n")
	if err := EnsureUnchanged(context.Background(), repo, expected); !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("dirty submodule mutation error = %v", err)
	}

	after := mustCapture(t, repo)
	difference, err := Diff(context.Background(), repo, expected, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(difference.Paths) != 0 || difference.StatusChanged || !difference.SubmodulesChanged || difference.HeadChanged || difference.HeadRefChanged || difference.IndexChanged {
		t.Fatalf("difference = %+v", difference)
	}
}

func TestCaptureNestedSubmodulesUsesLinearGitCalls(t *testing.T) {
	const depth = 4
	repository := nestedRepositoryChain(t, depth)
	var calls int
	replaceBeforeGitCommandHook(t, func(string, string, []string) { calls++ })
	mustCapture(t, repository)

	// Each hierarchy pass performs a fixed number of Git calls per populated
	// repository. The old recurrence grew exponentially at this depth.
	if limit := 30*depth + 2; calls > limit {
		t.Fatalf("git calls = %d, want at most %d for depth %d", calls, limit, depth)
	}
}

func TestCaptureRejectsCanonicalSubmoduleCycle(t *testing.T) {
	repository := newRepository(t)
	root, err := repositoryRoot(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	replaceFindPopulatedSubmodules(t, func(context.Context, string) ([]populatedSubmodule, error) {
		return []populatedSubmodule{{path: "cycle", root: root}}, nil
	})
	_, err = Capture(context.Background(), repository)
	var cycle *SubmoduleCycleError
	if !errors.As(err, &cycle) || !errors.Is(err, ErrSubmoduleCycle) || cycle.Root != root {
		t.Fatalf("error = %v", err)
	}
}

func TestCaptureFingerprintsSymbolicHEAD(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"same OID branch switch", func(t *testing.T, repo string) {
			runGit(t, repo, "checkout", "--quiet", "-b", "same-oid")
		}},
		{"same OID detach", func(t *testing.T, repo string) {
			runGit(t, repo, "checkout", "--quiet", "--detach")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t)
			before := mustCapture(t, repo)
			test.change(t, repo)
			after := mustCapture(t, repo)
			if before.HeadOID != after.HeadOID || before.HeadRef == after.HeadRef {
				t.Fatalf("before = %+v, after = %+v", before, after)
			}
			if err := EnsureUnchanged(context.Background(), repo, before); !errors.Is(err, ErrRepositoryDiverged) {
				t.Fatalf("EnsureUnchanged() error = %v", err)
			}
			difference, err := Diff(context.Background(), repo, before, after)
			if err != nil {
				t.Fatal(err)
			}
			if difference.HeadChanged || !difference.HeadRefChanged || difference.IndexChanged || difference.StatusChanged || difference.SubmodulesChanged || len(difference.Paths) != 0 {
				t.Fatalf("difference = %+v", difference)
			}
			if _, err := Compare(context.Background(), repo, before, after); !errors.Is(err, ErrRepositoryDiverged) {
				t.Fatalf("Compare() error = %v", err)
			}
		})
	}
}

func TestCaptureUsesGitRootForSubdirectory(t *testing.T) {
	t.Parallel()
	repo := newRepository(t)
	subdirectory := filepath.Join(repo, "nested", "directory")
	if err := os.MkdirAll(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "outside.txt"), "before operation\n")
	fromRoot := mustCapture(t, repo)
	fromSubdirectory := mustCapture(t, subdirectory)
	if !sameSnapshot(fromRoot, fromSubdirectory) {
		t.Fatalf("root snapshot = %+v, subdirectory snapshot = %+v", fromRoot, fromSubdirectory)
	}

	write(t, filepath.Join(repo, "outside.txt"), "changed during operation\n")
	after := mustCapture(t, subdirectory)
	difference, err := Diff(context.Background(), subdirectory, fromSubdirectory, after)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"outside.txt"}; !reflect.DeepEqual(difference.Paths, want) {
		t.Fatalf("paths = %q, want %q", difference.Paths, want)
	}
	if err := EnsureUnchanged(context.Background(), subdirectory, fromSubdirectory); !errors.Is(err, ErrRepositoryDiverged) {
		t.Fatalf("EnsureUnchanged() error = %v", err)
	}
}

func TestCaptureIgnoresRepositorySelectionEnvironment(t *testing.T) {
	target := newRepository(t)
	alternate := newRepository(t)
	write(t, filepath.Join(target, "tracked.txt"), "target\n")
	write(t, filepath.Join(alternate, "tracked.txt"), "alternate\n")
	t.Setenv("GIT_DIR", filepath.Join(alternate, ".git"))
	t.Setenv("GIT_WORK_TREE", alternate)

	snapshot := mustCapture(t, target)
	content := runGit(t, target, "show", snapshot.TreeOID+":tracked.txt")
	if content != "target\n" {
		t.Fatalf("captured content = %q, want target repository", content)
	}
}

func TestEnsureUnchangedDetectsUnexpectedChangesBetweenOperations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"file", func(t *testing.T, repo string) { write(t, filepath.Join(repo, "unexpected.txt"), "manual edit\n") }},
		{"index", func(t *testing.T, repo string) {
			write(t, filepath.Join(repo, "tracked.txt"), "staged\n")
			runGit(t, repo, "add", "tracked.txt")
		}},
		{"head", func(t *testing.T, repo string) {
			write(t, filepath.Join(repo, "tracked.txt"), "committed\n")
			runGit(t, repo, "add", "tracked.txt")
			runGit(t, repo, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "unexpected")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t)
			expected := mustCapture(t, repo)
			if err := EnsureUnchanged(context.Background(), repo, expected); err != nil {
				t.Fatalf("unchanged repository rejected: %v", err)
			}
			test.change(t, repo)
			if err := EnsureUnchanged(context.Background(), repo, expected); !errors.Is(err, ErrRepositoryDiverged) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCompareReportsAllOutsideChanges(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func nestedRepositoryChain(t *testing.T, depth int) string {
	t.Helper()
	if depth < 1 {
		t.Fatal("nested repository depth must be positive")
	}
	repository := newRepository(t)
	for level := 1; level < depth; level++ {
		parent := newRepository(t)
		childHead := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
		runGit(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+childHead+",nested")
		runGit(t, parent, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "add nested repository")
		if err := os.Rename(repository, filepath.Join(parent, "nested")); err != nil {
			t.Fatal(err)
		}
		repository = parent
	}
	return repository
}

func replaceBeforeGitCommandHook(t *testing.T, replacement func(repository, index string, args []string)) {
	t.Helper()
	original := beforeGitCommandHook
	beforeGitCommandHook = replacement
	t.Cleanup(func() { beforeGitCommandHook = original })
}

func replaceFindPopulatedSubmodules(t *testing.T, replacement func(context.Context, string) ([]populatedSubmodule, error)) {
	t.Helper()
	original := findPopulatedSubmodules
	findPopulatedSubmodules = replacement
	t.Cleanup(func() { findPopulatedSubmodules = original })
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
