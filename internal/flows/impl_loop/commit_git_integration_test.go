//go:build git_integration

package impl_loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitCommitControlCommitsCodeAndInformationalMarkTogether(t *testing.T) {
	repository := newGitWorkspace(t)
	switchToBranch(t, repository, "implementation")
	run, stateStore, _ := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	acceptCommitFixture(t, stateStore, run)

	tasks := filepath.Join(repository, "openspec", "changes", "change", "tasks.md")
	writeGitWorkspaceFile(t, tasks, "- [x] source task\n")
	writeGitWorkspaceFile(t, filepath.Join(repository, "implementation.txt"), "accepted code\n")
	message := "Implement source task"
	response := AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: "implement", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}

	result, err := CommitAcceptedAssignment(context.Background(), CommitAcceptedAssignmentInput{Run: run, StateStore: stateStore, Repository: repository, AssignmentID: "assignment", OperationID: "commit-1", Response: response, Control: GitCommitControl{}})
	if err != nil {
		t.Fatal(err)
	}
	if commits := strings.Fields(git(t, repository, "rev-list", "--count", "HEAD")); len(commits) != 1 || commits[0] != "2" {
		t.Fatalf("commit count = %q, want one new commit", commits)
	}
	for _, path := range []string{"implementation.txt", "openspec/changes/change/tasks.md"} {
		if got := git(t, repository, "show", "--format=", "--name-only", "HEAD", "--", path); strings.TrimSpace(got) != path {
			t.Fatalf("commit does not include %q: %q", path, got)
		}
	}
	contents, err := os.ReadFile(tasks)
	if err != nil || !strings.Contains(string(contents), "[x]") {
		t.Fatalf("informational mark = %q, error = %v", contents, err)
	}
	if result.Commit.CommitID != strings.TrimSpace(git(t, repository, "rev-parse", "HEAD")) {
		t.Fatalf("committed state did not retain actual HEAD: %#v", result.Commit)
	}
}
