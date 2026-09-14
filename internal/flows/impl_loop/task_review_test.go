package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestAssignmentDiffIncludesUntrackedFiles(t *testing.T) {
	repository := newSnapshotRepository(t)
	baseOutput, err := exec.Command("git", "-C", repository, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "generated_assignment.go"), []byte("package generated\n\nconst Included = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := assignmentDiff(context.Background(), repository, strings.TrimSpace(string(baseOutput)))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"generated_assignment.go", "const Included = true"} {
		if !strings.Contains(diff, fragment) {
			t.Fatalf("assignment diff omitted untracked file fragment %q:\n%s", fragment, diff)
		}
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
	input := TaskReviewInput{Run: fixture.run, StateStore: fixture.state, Journal: fixture.journal, Repository: fixture.repository, AssignmentID: "assignment", OperationID: "review-1", ResultID: "review-1-result", CallID: "review-1-call", Limits: controlledCallLimits()}
	if _, err := runTaskReviewerTurn(context.Background(), input, reviewer, "brief", "initial", "base"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.RecordTaskReviewDispute("assignment", implementationstate.TaskReviewDispute{FindingID: "F-1", Arguments: "the implementation already meets the brief", References: []string{"internal/one.go:1"}}); err != nil {
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
	if len(second.Record.Disputes) != 1 || len(second.Record.Findings) != 2 || second.Record.Findings[0].Status != implementationstate.FindingResolved || second.Record.Findings[1].Status != implementationstate.FindingRetained || second.Record.Findings[1].Resolution != "dispute evidence does not repair this defect" {
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
