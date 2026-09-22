package impl_loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestAcceptAssignmentAndReflectProgressPersistsAcceptanceBeforeInformationalMarkdownEdit(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	tasksPath := filepath.Join("openspec", "changes", "change", "tasks.md")
	absoluteTasksPath := filepath.Join(repository, tasksPath)
	if err := os.MkdirAll(filepath.Dir(absoluteTasksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absoluteTasksPath, []byte("- [ ] source task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()

	payload := responsePayloadMap(ResponseProgressReflected)
	// The receipt intentionally disagrees with the selected task. It proves
	// that this route does not use an agent response or checkbox as task state.
	payload["task_ids"] = []string{"unrelated-receipt"}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var beforeCall *implstate.Run
	var beforeCallErr error
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: raw, before: func() {
		beforeCall, _, beforeCallErr = runstore.ReadJournalCurrent(journal)
		if beforeCallErr == nil {
			beforeCallErr = os.WriteFile(absoluteTasksPath, []byte("- [ ] deliberately stale informational checkbox\n"), 0o600)
		}
	}}}}
	input := AcceptanceReflectionInput{
		Run: run, StateStore: stateStore, Journal: journal, Repository: repository,
		Workspace:    newFilesystemWorkspaceControl(),
		Session:      &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"},
		AssignmentID: "assignment", Acceptance: acceptanceReflectionEvidence(run),
		TasksPath:             filepath.ToSlash(tasksPath),
		ReflectionOperationID: "reflect-progress", ReflectionResultID: "reflect-progress-result", ReflectionCallID: "reflect-progress",
		Limits: controlledCallLimits(),
	}
	result, err := AcceptAssignmentAndReflectProgress(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if beforeCallErr != nil {
		t.Fatalf("state was not readable during the orchestrator call: %v", beforeCallErr)
	}
	if beforeCall == nil || beforeCall.Assignments[0].Status != implstate.AssignmentAcceptedAwaitingCommit || beforeCall.LeafStatus["A"] != implstate.TaskAcceptedAwaitingCommit || len(beforeCall.RunOperations) != 2 {
		t.Fatalf("acceptance was not durably recorded before the orchestrator turn: %#v", beforeCall)
	}
	if result.Call.Response.Kind != ResponseProgressReflected || len(runtime.messages) != 1 || !strings.Contains(runtime.messages[0], "openspec/changes/change/tasks.md") {
		t.Fatalf("orchestrator was not directly instructed to reflect task progress: result=%#v messages=%q", result, runtime.messages)
	}
	markdown, err := os.ReadFile(absoluteTasksPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), "[ ]") {
		t.Fatalf("fixture did not leave the intended stale checkbox: %q", markdown)
	}
	if run.Assignments[0].Status != implstate.AssignmentAcceptedAwaitingCommit || run.LeafStatus["A"] != implstate.TaskAcceptedAwaitingCommit || run.Assignments[0].Acceptance == nil {
		t.Fatalf("informational Markdown edit staled acceptance: %#v", run.Assignments[0])
	}
	if len(run.Assignments[0].TaskReviews) != 0 || len(run.Assignments[0].Operations) != 2 || len(run.RunResults) != 2 {
		t.Fatalf("informational reflection added task review or assignment work: %#v", run)
	}
}

func TestValidateAcceptanceReflectionInputAllowsOnlySelectedChangeTasksPath(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	run, stateStore, journal := acceptanceReflectionFixture(t, repository)
	defer stateStore.Close()
	input := AcceptanceReflectionInput{
		Run: run, StateStore: stateStore, Journal: journal, Repository: repository,
		Session:               &AgentSession{Role: ResponseRoleOrchestrator},
		AssignmentID:          "assignment",
		ReflectionOperationID: "reflect-progress", ReflectionResultID: "reflect-progress-result", ReflectionCallID: "reflect-progress",
		Limits: controlledCallLimits(),
	}
	input.TasksPath = "openspec/changes/change/tasks.md"
	if err := validateAcceptanceReflectionInput(input); err != nil {
		t.Fatalf("selected change tasks path rejected: %v", err)
	}
	for _, path := range []string{
		"openspec/changes/other-change/tasks.md",
		"openspec/changes/change/specs/tasks.md",
	} {
		input.TasksPath = path
		if err := validateAcceptanceReflectionInput(input); err == nil {
			t.Fatalf("foreign or nested tasks path %q was accepted", path)
		}
	}
	for _, change := range []string{".", "..", "nested/change", "nested\\change"} {
		input.Run.Identity.Change = change
		input.TasksPath = "openspec/changes/change/tasks.md"
		if err := validateAcceptanceReflectionInput(input); err == nil {
			t.Fatalf("unsafe change component %q was accepted", change)
		}
	}
}

func acceptanceReflectionFixture(t *testing.T, repository string) (*implstate.Run, *runstore.StateStore, *runstore.Run) {
	t.Helper()
	store := mustTransientControllerStore(t, t.TempDir())
	journal, err := store.Create("acceptance-reflection")
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id implstate.EvidenceID) implstate.EvidenceRef {
		value, err := journal.Publish(id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	identity := implstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "feature", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("specification"), TaskList: ref("tasks"), Configuration: ref("configuration")}
	run, err := implstate.NewRun(identity, []implstate.Task{
		{ID: "parent", Order: 0, Title: "parent task"},
		{ID: "A", ParentID: "parent", Order: 1, Title: "source task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: identity.Specification, Configuration: identity.Configuration}
	if err := run.AddRunOperation(implstate.Operation{ID: "baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment", []implstate.TaskID{"A"}); err != nil {
		t.Fatal(err)
	}
	brief := implstate.BriefVersion{ID: "brief", Number: 1, Document: ref("brief")}
	if err := run.AddBriefVersion("assignment", brief); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []implstate.Operation{
		{ID: "check", Kind: implstate.OperationCheck, BriefID: brief.ID, Basis: basis, Counter: implstate.CycleCounterMandatoryChecks},
		{ID: "review", Kind: implstate.OperationReview, BriefID: brief.ID, Basis: basis, Counter: implstate.CycleCounterAssignmentReview},
	} {
		if err := run.AddOperation("assignment", operation); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt("assignment", operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult("assignment", implstate.OperationResult{ID: implstate.ResultID(string(operation.ID) + "-result"), OperationID: operation.ID, Status: implstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		_ = stateStore.Close()
		t.Fatal(err)
	}
	return run, stateStore, journal
}

func acceptanceReflectionEvidence(run *implstate.Run) implstate.AcceptanceEvidence {
	basis := implstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	return implstate.AcceptanceEvidence{
		BriefID: "brief", State: run.CurrentState, Basis: basis,
		CheckResultIDs: []implstate.ResultID{"check-result"}, ReviewResultID: "review-result",
		PendingCommit: implstate.CommitIntent{OperationID: "commit", ParentCommit: "base", Tree: "tree", Message: "commit accepted work"},
	}
}
