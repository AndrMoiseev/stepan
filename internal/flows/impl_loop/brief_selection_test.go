package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

func TestExecuteBriefSelectionAcceptsContiguousInitialPrefixesAndPersistsAssignment(t *testing.T) {
	for _, selection := range [][]implementationstate.TaskID{{"A"}, {"A", "B"}, {"A", "B", "C"}} {
		t.Run(joinTaskIDs(selection), func(t *testing.T) {
			run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
			defer stateStore.Close()
			if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
				t.Fatal(err)
			}
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponse(t, selection)}}}
			result, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("assignment-1", run, stateStore, journal, repository, expectation, runtime))
			if err != nil {
				t.Fatal(err)
			}
			if result.AssignmentID != "assignment-1" || !reflect.DeepEqual(result.TaskIDs, selection) || result.Call.Attempts != 1 {
				t.Fatalf("selection result = %#v", result)
			}
			if len(run.Assignments) != 1 || run.Assignments[0].ID != "assignment-1" || run.Assignments[0].Status != implementationstate.AssignmentActive || !reflect.DeepEqual(run.Assignments[0].TaskIDs, selection) {
				t.Fatalf("assignment was not created from selection: %#v", run.Assignments)
			}
			current, _, err := runstore.ReadJournalCurrent(journal)
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Assignments) != 1 || current.Assignments[0].ID != "assignment-1" || !reflect.DeepEqual(current.Assignments[0].TaskIDs, selection) {
				t.Fatalf("durable assignment = %#v", current.Assignments)
			}
		})
	}
}

func TestExecuteBriefSelectionRejectsGapsParentsAndPartialTasksBeforeAssignment(t *testing.T) {
	for _, selection := range [][]implementationstate.TaskID{{"B"}, {"A", "C"}, {"root"}, {"A-part"}} {
		t.Run(joinTaskIDs(selection), func(t *testing.T) {
			run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
			defer stateStore.Close()
			if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
				t.Fatal(err)
			}
			runtime := &controlledCallRuntime{turns: []controlledTurn{
				{raw: briefReadyResponse(t, selection)},
				{raw: briefReadyResponse(t, []implementationstate.TaskID{"A"})},
			}}
			result, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("assignment-1", run, stateStore, journal, repository, expectation, runtime))
			if err != nil {
				t.Fatal(err)
			}
			if result.Call.Attempts != 2 || len(runtime.messages) != 2 || len(run.Assignments) != 1 || !reflect.DeepEqual(run.Assignments[0].TaskIDs, []implementationstate.TaskID{"A"}) {
				t.Fatalf("invalid selection was not rejected before assignment: result=%#v assignments=%#v messages=%#v", result, run.Assignments, runtime.messages)
			}
			attempts := run.RunOperations[len(run.RunOperations)-1].Attempts
			if len(attempts) != 2 || attempts[0].Outcome != implementationstate.AttemptRejected || attempts[1].Outcome != implementationstate.AttemptSucceeded {
				t.Fatalf("selection retries were not durable: %#v", attempts)
			}
		})
	}
}

func TestExecuteBriefSelectionRejectsReselectingAnActiveAssignment(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "first-select"); err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("existing", []implementationstate.TaskID{"A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "second-select"); !errors.Is(err, ErrBriefSelection) {
		t.Fatalf("prepare while assignment is active = %v, want ErrBriefSelection", err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: briefReadyResponse(t, []implementationstate.TaskID{"A"})}}}
	if _, err := ExecuteBriefSelection(context.Background(), briefSelectionCall("other", run, stateStore, journal, repository, expectation, runtime)); !errors.Is(err, ErrBriefSelection) {
		t.Fatalf("reselect active task error = %v, want ErrBriefSelection", err)
	}
	if len(runtime.messages) != 0 || len(run.Assignments) != 1 || run.Assignments[0].ID != "existing" {
		t.Fatalf("reselection dispatched or changed assignment state: %#v", run.Assignments)
	}
}

func TestValidateBriefSelectionResponseRejectsWrongBinding(t *testing.T) {
	run, stateStore, _, _, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	brief := "brief"
	response := AgentResponse{Kind: ResponseBriefReady, TaskIDs: []implementationstate.TaskID{"A"}, Brief: &brief, Binding: expectation.Binding}
	response.Binding.TaskList = implementationstate.EvidenceRef{ID: "other", Digest: "other"}
	if err := validateBriefSelectionResponse(run, expectation, response); !errors.Is(err, ErrBriefSelection) {
		t.Fatalf("wrong response binding = %v, want ErrBriefSelection", err)
	}
}

func TestNewBriefSelectionCallStartsBrieferWithCompleteRuntimeContext(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select"); err != nil {
		t.Fatal(err)
	}
	factory := &sessionRuntimeFactory{}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })

	selection, err := NewBriefSelectionCall(context.Background(), owner, BriefSelectionCallInput{
		AssignmentID: "assignment-1",
		Repository:   repository,
		Policy:       AgentCallPolicy{Role: AgentRoleBriefer, CallID: expectation.Binding.CallID},
		Run:          run, Journal: journal, StateStore: stateStore, OperationID: "select",
		Limits: controlledCallLimits(), Expectation: expectation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.assignmentID != "assignment-1" || selection.call.Session == nil || selection.call.Session.Role != ResponseRoleBriefer || selection.call.Message != briefSelectionMessage {
		t.Fatalf("selection call was not controller-built: %#v", selection.call)
	}
	configs := factory.configurations()
	if len(configs) != 1 {
		t.Fatalf("briefer sessions = %d, want 1", len(configs))
	}
	context := configs[0].BootstrapInstructions
	for _, required := range []string{
		"COMPLETE-SPECIFICATION-MARKER", "# Full machine task list and statuses",
		"order=0 id=root parent=(root) status=pending title=parent",
		"order=1 id=A parent=root status=pending title=A",
		"order=2 id=B parent=root status=pending title=B",
		"order=3 id=C parent=root status=pending title=C",
		"# Current progress", "Leaf tasks: total=3 pending=3 accepted_awaiting_commit=0 complete=0",
	} {
		if !strings.Contains(context, required) {
			t.Fatalf("briefer runtime context lacks %q:\n%s", required, context)
		}
	}
	if configs[0].WorkspaceWriteAllowed {
		t.Fatal("briefer runtime received workspace write access")
	}
}

func TestBriefSelectionBrieferSessionsAreScopedToStableAssignments(t *testing.T) {
	run, stateStore, journal, repository, expectation := newBriefSelectionFixture(t)
	defer stateStore.Close()
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select-a"); err != nil {
		t.Fatal(err)
	}
	firstExpectation := expectation
	firstExpectation.Binding.CallID = "select-a"
	factory := &sessionRuntimeFactory{}
	owner := newSessionOwnerForTest(t, factory)
	t.Cleanup(func() { _ = owner.Close() })

	first, err := NewBriefSelectionCall(context.Background(), owner, BriefSelectionCallInput{
		AssignmentID: "assignment-a", Repository: repository,
		Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: "select-a"},
		Run:    run, Journal: journal, StateStore: stateStore, OperationID: "select-a",
		Limits: controlledCallLimits(), Expectation: firstExpectation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.StartAssignment("assignment-a", []implementationstate.TaskID{"A"}); err != nil {
		t.Fatal(err)
	}
	commitBriefSelectionAssignment(t, run, journal, "assignment-a")
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBriefSelection(context.Background(), stateStore, run, "select-b"); err != nil {
		t.Fatal(err)
	}
	secondExpectation := expectation
	secondExpectation.Binding.CallID = "select-b"
	secondInput := BriefSelectionCallInput{
		AssignmentID: "assignment-b", Repository: repository,
		Policy: AgentCallPolicy{Role: AgentRoleBriefer, CallID: "select-b"},
		Run:    run, Journal: journal, StateStore: stateStore, OperationID: "select-b",
		Limits: controlledCallLimits(), Expectation: secondExpectation,
	}
	second, err := NewBriefSelectionCall(context.Background(), owner, secondInput)
	if err != nil {
		t.Fatal(err)
	}
	configs := factory.configurations()
	if len(configs) != 2 || first.call.Session == second.call.Session {
		t.Fatalf("briefer sessions did not split by assignment: sessions=%d first=%p second=%p", len(configs), first.call.Session, second.call.Session)
	}
	secondContext := configs[1].BootstrapInstructions
	for _, required := range []string{
		"# Assignment\n\nassignment-b", "order=1 id=A parent=root status=complete title=A",
		"order=2 id=B parent=root status=pending title=B",
		"Leaf tasks: total=3 pending=2 accepted_awaiting_commit=0 complete=1",
	} {
		if !strings.Contains(secondContext, required) {
			t.Fatalf("second briefer context lacks %q:\n%s", required, secondContext)
		}
	}
	if err := run.StartAssignment("assignment-b", []implementationstate.TaskID{"B"}); err != nil {
		t.Fatal(err)
	}
	refinementStart, err := BuildBrieferStartContext(journal, run, "assignment-b")
	if err != nil {
		t.Fatal(err)
	}
	continued, err := owner.Briefer(context.Background(), "assignment-b", refinementStart)
	if err != nil {
		t.Fatal(err)
	}
	if continued != second.call.Session || len(factory.configurations()) != 2 {
		t.Fatalf("same assignment did not preserve briefer lifecycle: selected=%p continued=%p sessions=%d", second.call.Session, continued, len(factory.configurations()))
	}
	if _, err := owner.Briefer(context.Background(), "assignment-a", refinementStart); err == nil {
		t.Fatal("briefer accepted a mismatched assignment context")
	}
}

func commitBriefSelectionAssignment(t *testing.T, run *implementationstate.Run, journal *runstore.Run, assignmentID implementationstate.AssignmentID) {
	t.Helper()
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	briefDocument, err := journal.Publish(implementationstate.EvidenceID("brief-"+assignmentID), []byte("brief"))
	if err != nil {
		t.Fatal(err)
	}
	briefID := implementationstate.BriefID("brief-" + assignmentID)
	if err := run.AddBriefVersion(assignmentID, implementationstate.BriefVersion{ID: briefID, Number: 1, Document: briefDocument}); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []implementationstate.Operation{
		{ID: "check-" + implementationstate.OperationID(assignmentID), Kind: implementationstate.OperationCheck, BriefID: briefID, Basis: basis},
		{ID: "review-" + implementationstate.OperationID(assignmentID), Kind: implementationstate.OperationReview, BriefID: briefID, Basis: basis},
	} {
		if err := run.AddOperation(assignmentID, operation); err != nil {
			t.Fatal(err)
		}
		if _, err := run.StartAssignmentAttempt(assignmentID, operation.ID); err != nil {
			t.Fatal(err)
		}
		if err := run.AddResult(assignmentID, implementationstate.OperationResult{ID: implementationstate.ResultID(operation.ID + "-result"), OperationID: operation.ID, Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	intent := implementationstate.CommitIntent{OperationID: "commit-" + implementationstate.OperationID(assignmentID), ParentCommit: "base", Tree: "tree", Message: "commit assignment"}
	if err := run.AcceptAssignment(assignmentID, implementationstate.AcceptanceEvidence{BriefID: briefID, State: run.CurrentState, Basis: basis, CheckResultIDs: []implementationstate.ResultID{"check-" + implementationstate.ResultID(assignmentID) + "-result"}, ReviewResultID: implementationstate.ResultID("review-" + implementationstate.ResultID(assignmentID) + "-result"), PendingCommit: intent}); err != nil {
		t.Fatal(err)
	}
	if err := run.CommitAssignment(assignmentID, implementationstate.CommitEvidence{OperationID: intent.OperationID, CommitID: "commit-" + string(assignmentID), ParentCommit: intent.ParentCommit, Tree: intent.Tree, Message: intent.Message, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
}

func newBriefSelectionFixture(t *testing.T) (*implementationstate.Run, *runstore.StateStore, *runstore.Run, string, ResponseExpectation) {
	t.Helper()
	repository := newGitWorkspace(t)
	store, err := runstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.Create("brief-selection")
	if err != nil {
		t.Fatal(err)
	}
	ref := func(id implementationstate.EvidenceID) implementationstate.EvidenceRef {
		contents := []byte(id)
		if id == "specification" {
			contents = []byte("# Specification\nCOMPLETE-SPECIFICATION-MARKER\n")
		}
		value, err := journal.Publish(id, contents)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	identity := implementationstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "feature", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("specification"), TaskList: ref("tasks"), Configuration: ref("configuration")}
	run, err := implementationstate.NewRun(identity, []implementationstate.Task{
		{ID: "root", Order: 0, Title: "parent"},
		{ID: "A", ParentID: "root", Order: 1, Title: "A"},
		{ID: "B", ParentID: "root", Order: 2, Title: "B"},
		{ID: "C", ParentID: "root", Order: 3, Title: "C"},
	})
	if err != nil {
		t.Fatal(err)
	}
	basis := implementationstate.AcceptanceBasis{Specification: identity.Specification, Configuration: identity.Configuration}
	if err := run.AddRunOperation(implementationstate.Operation{ID: "baseline", Kind: implementationstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartRunAttempt("baseline"); err != nil {
		t.Fatal(err)
	}
	if err := run.AddRunResult(implementationstate.OperationResult{ID: "baseline-result", OperationID: "baseline", Status: implementationstate.ResultSucceeded, State: run.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordInitialBaselinePass("baseline", "baseline-result"); err != nil {
		t.Fatal(err)
	}
	stateStore, err := runstore.OpenState(journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stateStore.Record(context.Background(), run); err != nil {
		_ = stateStore.Close()
		t.Fatal(err)
	}
	expectation := ResponseExpectation{Role: ResponseRoleBriefer, State: ResponseStateInitialBriefing, Scope: ResponseScopeRun, Binding: ResponseBinding{CallID: "select", RunID: identity.ID, Specification: identity.Specification, Configuration: identity.Configuration, TaskList: identity.TaskList}}
	return run, stateStore, journal, repository, expectation
}

func briefSelectionCall(assignmentID implementationstate.AssignmentID, run *implementationstate.Run, stateStore *runstore.StateStore, journal *runstore.Run, repository string, expectation ResponseExpectation, runtime *controlledCallRuntime) BriefSelectionCall {
	return BriefSelectionCall{assignmentID: assignmentID, call: ControlledAgentCall{
		Session: &AgentSession{Role: ResponseRoleBriefer, runtime: runtime, thread: "thread", restart: func(context.Context) (*AgentSession, error) {
			return &AgentSession{Role: ResponseRoleBriefer, runtime: runtime, thread: "thread"}, nil
		}},
		Repository:  repository,
		Policy:      AgentCallPolicy{Role: AgentRoleBriefer, CallID: expectation.Binding.CallID},
		Run:         run,
		Journal:     journal,
		StateStore:  stateStore,
		OperationID: "select",
		Limits:      controlledCallLimits(),
		Expectation: expectation,
		Message:     "select the next assignment",
	}}
}

func briefReadyResponse(t *testing.T, taskIDs []implementationstate.TaskID) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseBriefReady)
	values := make([]string, len(taskIDs))
	for index, taskID := range taskIDs {
		values[index] = string(taskID)
	}
	payload["task_ids"] = values
	payload["brief"] = "self-contained brief"
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func joinTaskIDs(ids []implementationstate.TaskID) string {
	result := ""
	for index, id := range ids {
		if index != 0 {
			result += "+"
		}
		result += string(id)
	}
	return result
}
