package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
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
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, fixture.journal, &unchangedWorkspaceControl{}, fixture.repository, "final-checks-result", review); err != nil {
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
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, fixture.journal, &unchangedWorkspaceControl{}, fixture.repository, "final-checks-result", review); !errors.Is(err, ErrFinalAcceptanceRoute) {
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

func TestFinalReviewRoutesExplorerAndContinuesTheSameReviewerSession(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		t.Fatal(err)
	}
	explorer := &finalExplorerRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExplorationResult)}}}
	owner := newSessionOwnerForTest(t, finalExplorerFactory{runtime: explorer})
	t.Cleanup(func() { _ = owner.Close() })
	reviewer := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExplorationRequested)}, {raw: responsePayload(t, ResponseReviewPassed)}}}
	result, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Owner: owner, Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-request", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
		Explorer: &FinalReviewExplorer{ExplorerOperationID: "final-explorer", ExplorerResultID: "final-explorer-result", ExplorerCallID: "final-explorer-call", ContinuationOperationID: "final-review-continuation", ContinuationResultID: "final-review-result", ContinuationCallID: "final-review-continuation-call"},
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: reviewer, thread: "final-reviewer"}, "base")
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseReviewPassed || result.ResultID != "final-review-result" || len(reviewer.messages) != 2 || len(explorer.messages) != 1 {
		t.Fatalf("routed final review = %#v, reviewer=%#v explorer=%#v", result, reviewer.messages, explorer.messages)
	}
	for _, id := range []implementationstate.ResultID{"final-review-request", "final-explorer-result", "final-review-result"} {
		if finalRunResult(fixture.run, id) == nil {
			t.Fatalf("durable result %q is absent: %#v", id, fixture.run.RunResults)
		}
	}
	operation := finalRunOperation(fixture.run, "final-explorer")
	if operation == nil || operation.Counter != implementationstate.CycleCounterExplorer || operation.Episode != "final-reviewer" {
		t.Fatalf("final Explorer operation = %#v", operation)
	}
}

func TestFinalReviewPausesBeforeAReviewerCanApproveAnInterveningEdit(t *testing.T) {
	fixture := newCompletedFinalFixture(t)
	defer fixture.state.Close()
	if _, err := RunFinalRequiredChecks(context.Background(), FinalRequiredChecks{
		Run: fixture.run, Workspace: &unchangedWorkspaceControl{}, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		Selection: fixture.selection, Runner: fixture.runner, MaxCycles: 3, Operation: "final-checks", Result: "final-checks-result",
	}); err != nil {
		t.Fatal(err)
	}
	reviewer := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseReviewPassed)}}}
	changed := ensureErrorWorkspaceControl{WorkspaceControl: &unchangedWorkspaceControl{}, err: gitsnapshot.ErrRepositoryDiverged}
	_, err := runFinalReviewerTurn(context.Background(), FinalReviewInput{
		Workspace: changed, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository,
		CheckResult: "final-checks-result", OperationID: "final-review", ResultID: "final-review-result", CallID: "final-review-call", RoundID: "round-1", Limits: controlledCallLimits(),
	}, &AgentSession{Role: ResponseRoleFinalReviewer, runtime: reviewer, thread: "final-reviewer"}, "base")
	if !errors.Is(err, ErrFinalAcceptanceRoute) || fixture.run.Status != implementationstate.RunPaused || len(reviewer.messages) != 0 {
		t.Fatalf("intervening edit was not blocked before approval: error=%v run=%#v reviewer=%#v", err, fixture.run, reviewer.messages)
	}
	if err := CompleteFinalAcceptance(context.Background(), fixture.run, fixture.state, fixture.journal, changed, fixture.repository, "final-checks-result", FinalReviewResult{Response: AgentResponse{Kind: ResponseReviewPassed}, ResultID: "final-review-result"}); !errors.Is(err, ErrFinalAcceptanceRoute) || fixture.run.Status == implementationstate.RunSucceeded {
		t.Fatalf("intervening edit could still close the run: %v, %#v", err, fixture.run)
	}
}

type finalExplorerFactory struct{ runtime *finalExplorerRuntime }

func (factory finalExplorerFactory) Preflight(implementationconfig.RuntimeProfile) error { return nil }
func (factory finalExplorerFactory) Create(context.Context, implementationconfig.RuntimeProfile) (agentruntime.Runtime, error) {
	return factory.runtime, nil
}

type finalExplorerRuntime struct {
	turns    []controlledTurn
	messages []string
}

func (*finalExplorerRuntime) StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return "explorer", nil
}
func (runtime *finalExplorerRuntime) RunTurn(_ agentruntime.Thread, message string) (json.RawMessage, error) {
	if len(runtime.turns) == 0 {
		return nil, errors.New("unexpected Explorer turn")
	}
	turn := runtime.turns[0]
	runtime.turns = runtime.turns[1:]
	runtime.messages = append(runtime.messages, message)
	return turn.raw, turn.err
}
func (*finalExplorerRuntime) Interrupt() error                      { return nil }
func (*finalExplorerRuntime) CloseThread(agentruntime.Thread) error { return nil }
func (*finalExplorerRuntime) Close() error                          { return nil }

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
