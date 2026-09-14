package impl_loop

import (
	"context"
	"errors"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
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
	input := TaskReviewInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", OperationID: "review-1", ResultID: "review-1-result", CallID: "review-1-call", Limits: controlledCallLimits()}
	first, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "review", "base")
	if err != nil {
		t.Fatal(err)
	}
	if first.Response.Kind != ResponseChangesRequested || len(first.Record.Findings) != 1 || first.Record.Findings[0].Status != implementationstate.FindingOpen {
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
	if second.Session != reviewer || second.Response.Kind != ResponseReviewPassed || len(second.Record.Findings) != 1 || second.Record.Findings[0].Status != implementationstate.FindingResolved {
		t.Fatalf("reviewer did not retain ownership through dispute: %#v", second)
	}
	assignment := fixture.run.Assignments[0]
	if len(assignment.TaskReviews) != 2 || assignment.Counters.AssignmentReview != 2 || assignment.Status != implementationstate.AssignmentActive {
		t.Fatalf("durable review discussion/counter = %#v", assignment)
	}
	if assignment.Results[len(assignment.Results)-1].Status != implementationstate.ResultSucceeded {
		t.Fatalf("passed review did not retain successful review evidence: %#v", assignment.Results)
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

func stringPointer(value string) *string { return &value }
