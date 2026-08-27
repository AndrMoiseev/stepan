package specflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateSpecID(t *testing.T) {
	valid := []string{"a", "spec-1", strings.Repeat("a", 64), "-", "com0", "lpt0"}
	invalid := []string{"", "UPPER", "has_underscore", "has space", "тест", strings.Repeat("a", 65), "CON", "prn", "Aux", "nul"}
	for index := 1; index <= 9; index++ {
		invalid = append(invalid, "com"+string(rune('0'+index)), "lpt"+string(rune('0'+index)))
	}
	for _, specID := range valid {
		t.Run("valid_"+specID, func(t *testing.T) {
			if err := ValidateSpecID(specID); err != nil {
				t.Fatalf("ValidateSpecID(%q): %v", specID, err)
			}
		})
	}
	for _, specID := range invalid {
		t.Run("invalid_"+specID, func(t *testing.T) {
			if err := ValidateSpecID(specID); err == nil {
				t.Fatalf("ValidateSpecID(%q) succeeded", specID)
			}
		})
	}
}

func TestFindGitRootFromRootAndNestedDirectory(t *testing.T) {
	repo := initRepository(t)
	nested := filepath.Join(repo, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{repo, nested} {
		got, err := FindGitRoot(context.Background(), start)
		if err != nil {
			t.Fatalf("FindGitRoot(%q): %v", start, err)
		}
		if got != want {
			t.Fatalf("FindGitRoot(%q) = %q, want %q", start, got, want)
		}
	}
}

func TestFindGitRootOutsideWorkingTree(t *testing.T) {
	directory := filepath.Join(initRepository(t), ".git")
	_, err := FindGitRoot(context.Background(), directory)
	if err == nil || !strings.Contains(err.Error(), "Git root") {
		t.Fatalf("FindGitRoot() error = %v, want clear Git root error", err)
	}
}

func TestPrepareSpecTargetDoesNotCreateOrChangeAnything(t *testing.T) {
	repo := initRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := gitStatus(t, repo)
	target, err := PrepareSpecTarget(repo, "new-spec")
	if err != nil {
		t.Fatal(err)
	}
	if target.Directory != filepath.Join(repo, "docs", "changes", "features", "new-spec") || target.Entrypoint != filepath.Join(target.Directory, "specification.md") {
		t.Fatalf("unexpected target: %#v", target)
	}
	if target.DisplayPath != "docs/changes/features/new-spec/specification.md" {
		t.Fatalf("DisplayPath = %q", target.DisplayPath)
	}
	if _, err := os.Lstat(target.Directory); !os.IsNotExist(err) {
		t.Fatalf("target was created or cannot be checked: %v", err)
	}
	if after := gitStatus(t, repo); after != before {
		t.Fatalf("working tree changed: before %q, after %q", before, after)
	}
	if err := os.MkdirAll(target.Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckContainment(repo, target.Directory); err != nil {
		t.Fatalf("repeated containment check: %v", err)
	}
}

func TestPrepareSpecTargetRejectsExistingTargetWithoutChangingIt(t *testing.T) {
	repo := initRepository(t)
	directory := filepath.Join(repo, "docs", "changes", "features", "existing")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareSpecTarget(repo, "existing"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("PrepareSpecTarget() error = %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing target changed: data=%q err=%v", data, err)
	}
}

func TestPrepareSpecTargetRejectsWindowsReparseEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows reparse point test")
	}
	repo := initRepository(t)
	docs := filepath.Join(repo, "docs")
	outside := t.TempDir()
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(docs, "changes"), 0o755); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(docs, "changes", "features")
	if output, err := exec.Command("cmd", "/c", "mklink", "/J", junction, outside).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v: %s", err, output)
	}
	if _, err := PrepareSpecTarget(repo, "escaped"); err == nil || !strings.Contains(err.Error(), "escapes Git root") {
		t.Fatalf("PrepareSpecTarget() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("escaped target was created or cannot be checked: %v", err)
	}
}

func initRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	command := exec.Command("git", "init", "--quiet", repo)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	canonical, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func gitStatus(t *testing.T, repo string) string {
	t.Helper()
	command := exec.Command("git", "-C", repo, "status", "--porcelain=v1", "--untracked-files=all")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	return string(output)
}
