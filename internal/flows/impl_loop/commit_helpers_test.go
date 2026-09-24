package impl_loop

import (
	"path/filepath"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
)

func writeResumeSpecification(t *testing.T, repository string) {
	t.Helper()
	writeWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "proposal.md"), "proposal\n")
	writeWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "design.md"), "design\n")
	writeWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "tasks.md"), "- [ ] task\n")
	writeWorkspaceFile(t, filepath.Join(repository, "openspec", "changes", "change", "specs", "feature", "spec.md"), "requirement\n")
	writeWorkspaceFile(t, filepath.Join(repository, "openspec", "specs", "base", "spec.md"), "base\n")
}

func commitResponse(run *implstate.Run, operationID implstate.OperationID, message string) AgentResponse {
	return AgentResponse{Kind: ResponseImplementationReady, Message: &message, Binding: ResponseBinding{CallID: string(operationID) + "-call", RunID: run.Identity.ID, AssignmentID: "assignment", BriefID: "brief", Specification: run.Identity.Specification, Configuration: run.Identity.Configuration, TaskList: run.Identity.TaskList}}
}
