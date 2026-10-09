package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
)

func TestTaskReviewKeepsReviewerOwnedFindingsAcrossDisputeRounds(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	if _, err := ApplyImplementerTransition(context.Background(), fixture.input("required", "required-result"), fixture.response(ResponseImplementationReady, nil)); err != nil {
		t.Fatal(err)
	}
	if err := CanStartTaskReview(fixture.run, "assignment"); err != nil {
		t.Fatalf("review gate = %v", err)
	}

	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseChangesRequested)}, {raw: responsePayload(t, ResponseReviewPassed)}}}
	reviewer := &AgentSession{Role: ResponseRoleTaskReviewer, runtime: runtime, thread: "reviewer"}
	input := TaskReviewInput{Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", OperationID: "review-1", ResultID: "review-1-result", CallID: "review-1-call", Limits: controlledCallLimits()}
	first, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "review", "base")
	if err != nil {
		t.Fatal(err)
	}
	if first.Response.Kind != ResponseChangesRequested || len(first.Record.Findings) != 1 || first.Record.Findings[0].Status != implstate.FindingOpen {
		t.Fatalf("first review = %#v", first)
	}
	dispute := AgentResponse{Kind: ResponseReviewDisputed, FindingIDs: []string{"F-1"}, Message: stringPointer("the code already validates this"), References: []string{"internal/example.go:12"}, Binding: fixture.binding()}
	if err := validateExecutorDispute(fixture.run, "assignment", dispute); err != nil {
		t.Fatalf("valid dispute = %v", err)
	}
	if err := validateExecutorDispute(fixture.run, "assignment", AgentResponse{Kind: ResponseReviewDisputed, FindingIDs: []string{"missing"}, Message: stringPointer("no"), References: []string{"x"}, Binding: fixture.binding()}); !errors.Is(err, ErrInvalidTaskReviewRoute) {
		t.Fatalf("closed/unknown finding dispute = %v", err)
	}

	input.OperationID, input.ResultID, input.CallID = "review-2", "review-2-result", "review-2-call"
	second, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "dispute", "base")
	if err != nil {
		t.Fatal(err)
	}
	if second.Session != reviewer || second.Response.Kind != ResponseReviewPassed || len(second.Record.Findings) != 1 || second.Record.Findings[0].Status != implstate.FindingResolved {
		t.Fatalf("reviewer did not retain ownership through dispute: %#v", second)
	}
	assignment := fixture.run.Assignments[0]
	if len(assignment.TaskReviews) != 2 || assignment.Counters.AssignmentReview != 2 || assignment.Status != implstate.AssignmentActive {
		t.Fatalf("durable review discussion/counter = %#v", assignment)
	}
	if assignment.Results[len(assignment.Results)-1].Status != implstate.ResultSucceeded {
		t.Fatalf("passed review did not retain successful review evidence: %#v", assignment.Results)
	}
}

func TestTaskReviewerExecutionBlockedPausesWithoutReviewRecord(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	reviewer := &AgentSession{Role: ResponseRoleTaskReviewer, runtime: runtime, thread: "reviewer"}
	input := TaskReviewInput{Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", OperationID: "blocked-review", ResultID: "blocked-review-result", CallID: "blocked-review-call", Limits: controlledCallLimits()}
	result, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "review", "base")
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseExecutionBlocked || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || len(fixture.run.Assignments[0].TaskReviews) != 0 || len(runtime.messages) != 1 {
		t.Fatalf("reviewer execution block advanced review: result=%#v run=%#v turns=%#v", result, fixture.run, runtime.messages)
	}
}

func TestRouteTaskReviewChangesPausesForExecutorExecutionBlocked(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddOperation("assignment", implstate.Operation{ID: "executor-review-changes", Kind: implstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	expectation := fixture.executorExpectation("executor-review-changes-call")
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	review := TaskReviewResult{Response: AgentResponse{Kind: ResponseChangesRequested, Binding: expectation.Binding}}
	result, err := RouteTaskReviewChanges(context.Background(), review, ControlledAgentCall{
		Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "executor"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: expectation.Binding.CallID, AllowUnprotected: true},
		Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-review-changes", Limits: controlledCallLimits(), Expectation: expectation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseExecutionBlocked || fixture.run.Status != implstate.RunPaused || fixture.run.ExecutionBlock == nil || len(runtime.messages) != 1 {
		t.Fatalf("executor block after review changes advanced work: result=%#v run=%#v turns=%#v", result, fixture.run, runtime.messages)
	}
	restarted, _, err := fixture.state.Current(context.Background())
	if err != nil || restarted.Status != implstate.RunPaused || restarted.ExecutionBlock == nil {
		t.Fatalf("executor review block was not durable: run=%#v error=%v", restarted, err)
	}
}

func TestTaskReviewerRejectsPreferenceAsBlockingFinding(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	response := AgentResponse{Kind: ResponseChangesRequested, FindingIDs: []string{"F-preference"}, Findings: []string{"prefer a different name"}, Locations: []string{"internal/example.go:1"}, Bases: []string{"reviewer preference"}, ExpectedResults: []string{"rename the symbol"}, Binding: fixture.binding()}
	if err := validateTaskReviewerResponse(fixture.run, "assignment", response); !errors.Is(err, ErrInvalidTaskReviewRoute) {
		t.Fatalf("preference was accepted as a blocking finding: %v", err)
	}
}

func TestTaskReviewPersistsMixedReviewerDecisionsAndDisputeAcrossRestart(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	if _, err := ApplyImplementerTransition(context.Background(), fixture.input("required", "required-result"), fixture.response(ResponseImplementationReady, nil)); err != nil {
		t.Fatal(err)
	}
	firstPayload := responsePayloadMap(ResponseChangesRequested)
	firstPayload["finding_ids"] = []string{"F-1", "F-2"}
	firstPayload["findings"] = []string{"first defect", "second defect"}
	firstPayload["finding_decisions"] = []string{"open", "open"}
	firstPayload["finding_reasons"] = []string{"new defect needs repair", "new defect needs repair"}
	firstPayload["locations"] = []string{"internal/one.go:1", "internal/two.go:2"}
	firstPayload["bases"] = []string{"defect in explicit requirement", "project rule violation"}
	firstPayload["expected_results"] = []string{"repair first", "repair second"}
	secondPayload := responsePayloadMap(ResponseChangesRequested)
	secondPayload["finding_ids"] = []string{"F-1", "F-2"}
	secondPayload["findings"] = []string{"ignored replacement", "ignored replacement"}
	secondPayload["finding_decisions"] = []string{"resolved", "retained"}
	secondPayload["finding_reasons"] = []string{"reviewer verified correction", "dispute evidence does not repair this defect"}
	secondPayload["locations"] = []string{"internal/one.go:1", "internal/two.go:2"}
	secondPayload["bases"] = []string{"defect in explicit requirement", "project rule violation"}
	secondPayload["expected_results"] = []string{"repair first", "repair second"}
	firstRaw, err := json.Marshal(firstPayload)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := json.Marshal(secondPayload)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: firstRaw}, {raw: secondRaw}}}
	reviewer := &AgentSession{Role: ResponseRoleTaskReviewer, runtime: runtime, thread: "reviewer"}
	input := TaskReviewInput{Workspace: &unchangedWorkspaceControl{}, Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", OperationID: "review-1", ResultID: "review-1-result", CallID: "review-1-call", Limits: controlledCallLimits()}
	if _, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "initial", "base"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordTaskReviewDispute("assignment", implstate.TaskReviewDispute{FindingID: "F-1", Arguments: "the implementation already meets the brief", References: []string{"internal/one.go:1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	input.OperationID, input.ResultID, input.CallID = "review-2", "review-2-result", "review-2-call"
	second, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "dispute", "base")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Record.Disputes) != 1 || len(second.Record.Findings) != 2 || second.Record.Findings[0].Status != implstate.FindingResolved || second.Record.Findings[1].Status != implstate.FindingRetained || second.Record.Findings[1].Resolution != "dispute evidence does not repair this defect" {
		t.Fatalf("mixed review record = %#v", second.Record)
	}
	restarted, _, err := fixture.state.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := renderTaskReviewHistory(restarted, "assignment")
	for _, fragment := range []string{"first defect", "repair second", "the implementation already meets the brief", "dispute evidence does not repair this defect"} {
		if !strings.Contains(history, fragment) {
			t.Fatalf("restarted review history omitted %q:\n%s", fragment, history)
		}
	}
}

func stringPointer(value string) *string { return &value }
