//go:build git_integration || process_integration

package impl_loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	gitFixture(t, repository, "init", "--quiet", "--initial-branch=main")
	gitFixture(t, repository, "config", "user.name", "Stepan Tests")
	gitFixture(t, repository, "config", "user.email", "stepan-tests@example.invalid")
	writeGitWorkspaceFile(t, filepath.Join(repository, "tracked.txt"), "initial\n")
	gitFixture(t, repository, "add", "--", "tracked.txt")
	gitFixture(t, repository, "commit", "--quiet", "-m", "initial")
	return repository
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

func gitFixture(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
