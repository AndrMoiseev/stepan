//go:build git_integration

package impl_loop

import (
	"path/filepath"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git/testfixture"
)

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	gitFixture(t, repository, "init", "--quiet", "--initial-branch=main")
	gitFixture(t, repository, "config", "user.name", "Stepan Tests")
	gitFixture(t, repository, "config", "user.email", "stepan-tests@example.invalid")
	writeWorkspaceFile(t, filepath.Join(repository, "tracked.txt"), "initial\n")
	gitFixture(t, repository, "add", "--", "tracked.txt")
	gitFixture(t, repository, "commit", "--quiet", "-m", "initial")
	return repository
}

func gitFixture(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	return testfixture.Run(t, repository, arguments...)
}

func switchToBranch(t *testing.T, repository, branch string) {
	t.Helper()
	gitFixture(t, repository, "switch", "--quiet", "-c", branch)
}
