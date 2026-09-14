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
			result, err := ExecuteBriefSelection(context.Background(), "assignment-1", briefSelectionCall(run, stateStore, journal, repository, expectation, runtime))
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
			result, err := ExecuteBriefSelection(context.Background(), "assignment-1", briefSelectionCall(run, stateStore, journal, repository, expectation, runtime))
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
	if _, err := ExecuteBriefSelection(context.Background(), "other", briefSelectionCall(run, stateStore, journal, repository, expectation, runtime)); !errors.Is(err, ErrBriefSelection) {
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
		Repository: repository,
		Policy:     AgentCallPolicy{Role: AgentRoleBriefer, CallID: expectation.Binding.CallID},
		Run:        run, Journal: journal, StateStore: stateStore, OperationID: "select",
		Limits: controlledCallLimits(), Expectation: expectation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.call.Session == nil || selection.call.Session.Role != ResponseRoleBriefer || selection.call.Message != briefSelectionMessage {
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

func briefSelectionCall(run *implementationstate.Run, stateStore *runstore.StateStore, journal *runstore.Run, repository string, expectation ResponseExpectation, runtime *controlledCallRuntime) BriefSelectionCall {
	return BriefSelectionCall{call: ControlledAgentCall{
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
