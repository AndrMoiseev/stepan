package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeRulesClassificationResolvesRepositoryAlias(t *testing.T) {
	repository := canonicalTestDirectory(t)
	writeResumeFile(t, filepath.Join(repository, "rules", "index.md"), "# Rules\n")
	alias := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(repository, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	rules, err := resumeTestConfiguration(t, "test-model", "rules/index.md").ValidateRulesFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	before := resumeSnapshot("before", "before")
	after := resumeSnapshot("after", "after")
	workspace := &resumeWorkspace{paths: []string{"rules/index.md"}}
	if !rulesOnlyWorkspaceChange(context.Background(), workspace, alias, before, after, rules) {
		t.Fatal("rules-only change through a repository alias invalidated acceptance")
	}
	workspace.paths = []string{"code.go"}
	if rulesOnlyWorkspaceChange(context.Background(), workspace, alias, before, after, rules) {
		t.Fatal("code change through a repository alias classified as rules-only")
	}
}
