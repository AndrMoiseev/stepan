//go:build git_integration

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssignmentDiffIncludesUntrackedFiles(t *testing.T) {
	repository := newGitWorkspace(t)
	baseOutput, err := exec.Command("git", "-C", repository, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "generated_assignment.go"), []byte("package generated\n\nconst Included = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := (Control{}).AssignmentDiff(context.Background(), repository, strings.TrimSpace(string(baseOutput)))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"generated_assignment.go", "const Included = true"} {
		if !strings.Contains(diff, fragment) {
			t.Fatalf("assignment diff omitted untracked file fragment %q:\n%s", fragment, diff)
		}
	}
}
