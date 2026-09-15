package impl_loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestFinalAcceptanceRequiresCurrentChecksAndPositiveIndependentReview(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()

	checks, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !checks.Set.Succeeded() || len(fixture.runner.commands) != len(fixture.selection.Required) {
		t.Fatalf("final checks = %#v, calls=%#v", checks, fixture.runner.commands)
	}

	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	review, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-result", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: runtime, thread: "final-reviewer"}, "base")
	if err != nil {
		t.Fatal(err)
	}
	if review.Response.Kind != ResponseReviewPassed || review.ResultID != "final-review-result" || len(runtime.messages) != 1 {
		t.Fatalf("final review = %#v, calls=%#v", review, runtime.messages)
	}
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, "final-checks-result", review); err != nil {
		t.Fatal(err)
	}
	if fixture.run.Status != implementationstate.RunSucceeded {
		t.Fatalf("run did not succeed: %#v", fixture.run)
	}
}

func TestFinalBlockingFindingCannotCloseTheRun(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseChangesRequested)}}}
	review, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-result", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: runtime, thread: "final-reviewer"}, "base")
	if err != nil {
		t.Fatal(err)
	}
	if review.Response.Kind != ResponseChangesRequested {
		t.Fatalf("final review = %#v", review)
	}
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, "final-checks-result", review); !errors.Is(err, ErrFinalAcceptanceRoute) {
		t.Fatalf("blocking review completed run: %v", err)
	}
	if fixture.run.Status == implementationstate.RunSucceeded || fixture.run.FinalAcceptance != nil {
		t.Fatalf("blocking review left successful evidence: %#v", fixture.run)
	}
	data, err := fixture.journal.Read(review.Evidence)
	if err != nil || !strings.Contains(string(data), "F-1") {
		t.Fatalf("blocking finding was not durable: %q, %v", data, err)
	}
}

func newCompletedFinalFixture(t *testing.T) implementerTransitionFixture {
	t.Helper()
	fixture := newImplementerTransitionFixture(t)
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, item := range []struct {
		operation implementationstate.OperationID
		result    implementationstate.ResultID
		kind      implementationstate.OperationKind
	}{{"accepted-check", "accepted-check-result", implementationstate.OperationCheck}, {"accepted-review", "accepted-review-result", implementationstate.OperationReview}} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: item.operation, Kind: item.kind, BriefID: "brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.run.StartAssignmentAttempt("assignment", item.operation); err != nil {
			t.Fatal(err)
		}
		if err := fixture.run.AddResult("assignment", implementationstate.OperationResult{ID: item.result, OperationID: item.operation, Status: implementationstate.ResultSucceeded, State: fixture.run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	intent := implementationstate.CommitIntent{OperationID: "assignment-commit", ParentCommit: "base", Tree: "tree", Message: "complete task"}
	if err := fixture.run.AcceptAssignment("assignment", implementationstate.AcceptanceEvidence{BriefID: "brief", State: fixture.run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"accepted-check-result"}, ReviewResultID: "accepted-review-result", PendingCommit: intent}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.CommitAssignment("assignment", implementationstate.CommitEvidence{OperationID: intent.OperationID, CommitID: "commit", ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: fixture.run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	fixture.runner.commands = nil
	return fixture
}
