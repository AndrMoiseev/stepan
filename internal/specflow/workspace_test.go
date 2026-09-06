package specflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFeatureID(t *testing.T) {
	valid := []string{"a", "spec-1", strings.Repeat("a", 64), "com0", "lpt0"}
	invalid := []string{"", "-", "-prefix", "suffix-", "UPPER", "has_underscore", "has space", "тест", strings.Repeat("a", 65), "CON", "prn", "Aux", "nul"}
	for index := 1; index <= 9; index++ {
		invalid = append(invalid, "com"+string(rune('0'+index)), "lpt"+string(rune('0'+index)))
	}
	for _, specID := range valid {
		t.Run("valid_"+specID, func(t *testing.T) {
			if err := ValidateFeatureID(specID); err != nil {
				t.Fatalf("ValidateFeatureID(%q): %v", specID, err)
			}
		})
	}
	for _, specID := range invalid {
		t.Run("invalid_"+specID, func(t *testing.T) {
			if err := ValidateFeatureID(specID); err == nil {
				t.Fatalf("ValidateFeatureID(%q) succeeded", specID)
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

func TestInitRepositoryConfiguresLocalCommitIdentity(t *testing.T) {
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	root := initRepository(t)
	writeGitTestFile(t, root, "tracked.txt", "initial\n")
	gitRun(t, root, "add", "--", "tracked.txt")
	gitRun(t, root, "commit", "--quiet", "-m", "initial")
}

func initRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	command := exec.Command("git", "init", "--quiet", repo)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	for _, setting := range [][]string{
		{"config", "user.name", "Stepan Tests"},
		{"config", "user.email", "stepan-tests@example.invalid"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitRun(t, repo, setting...)
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
