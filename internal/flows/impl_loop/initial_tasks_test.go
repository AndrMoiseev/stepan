package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestNewRunFromTaskExtractionPreservesFormalOrderAndHierarchy(t *testing.T) {
	identity := extractedRunIdentity()
	response := AgentResponse{
		Kind:    ResponseTasksExtracted,
		TaskIDs: []implstate.TaskID{"one", "one-a", "one-b", "two"},
		TaskPayloads: []string{
			`{"id":"one","parent_id":"","title":"First section"}`,
			`{"id":"one-a","parent_id":"one","title":"First leaf"}`,
			`{"id":"one-b","parent_id":"one","title":"Second leaf"}`,
			`{"id":"two","parent_id":"","title":"Second section"}`,
		},
		Binding: boundExtraction(identity),
	}

	run, err := NewRunFromTaskExtraction(identity, response)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := run.Tasks, []implstate.Task{
		{ID: "one", Order: 0, Title: "First section"},
		{ID: "one-a", ParentID: "one", Order: 1, Title: "First leaf"},
		{ID: "one-b", ParentID: "one", Order: 2, Title: "Second leaf"},
		{ID: "two", Order: 3, Title: "Second section"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks = %#v, want %#v", got, want)
	}
	if got, want := run.PendingLeafTasks(), []implstate.TaskID{"one-a", "one-b", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending leaves = %#v, want %#v", got, want)
	}
}

func TestDecodeExtractedTasksRejectsNonFormalOrAmbiguousHierarchy(t *testing.T) {
	for _, test := range []struct {
		name     string
		ids      []implstate.TaskID
		payloads []string
	}{
		{name: "duplicate identifier", ids: []implstate.TaskID{"one", "one"}, payloads: []string{`{"id":"one","parent_id":"","title":"one"}`, `{"id":"one","parent_id":"","title":"again"}`}},
		{name: "payload identifier disagrees", ids: []implstate.TaskID{"one"}, payloads: []string{`{"id":"other","parent_id":"","title":"one"}`}},
		{name: "later parent", ids: []implstate.TaskID{"child", "parent"}, payloads: []string{`{"id":"child","parent_id":"parent","title":"child"}`, `{"id":"parent","parent_id":"","title":"parent"}`}},
		{name: "reopened closed subtree", ids: []implstate.TaskID{"A", "B", "A1"}, payloads: []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"B","parent_id":"","title":"B"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}},
		{name: "unknown payload field", ids: []implstate.TaskID{"one"}, payloads: []string{`{"id":"one","parent_id":"","title":"one","order":9}`}},
		{name: "blank title", ids: []implstate.TaskID{"one"}, payloads: []string{`{"id":"one","parent_id":"","title":" "}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeExtractedTasks(test.ids, test.payloads); !errors.Is(err, ErrTaskExtraction) {
				t.Fatalf("error = %v, want ErrTaskExtraction", err)
			}
		})
	}
}

func TestNewRunFromTaskExtractionRejectsUnboundOrWrongResponse(t *testing.T) {
	identity := extractedRunIdentity()
	response := AgentResponse{
		Kind:         ResponseTasksAdded,
		TaskIDs:      []implstate.TaskID{"one"},
		TaskPayloads: []string{`{"id":"one","parent_id":"","title":"one"}`},
		Binding:      boundExtraction(identity),
	}
	if _, err := NewRunFromTaskExtraction(identity, response); !errors.Is(err, ErrTaskExtraction) {
		t.Fatalf("error = %v, want ErrTaskExtraction", err)
	}
}

func TestOrchestratorInstructionsSpecifyFormalTaskPayload(t *testing.T) {
	instructions, err := RoleInstructions(ResponseRoleOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"JSON object with id, parent_id, and title", "Response order is the source order"} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("orchestrator instructions omit %q\n%s", required, instructions)
		}
	}
}

func TestInitialExtractionPersistsPreDispatchAndCompletionBoundaries(t *testing.T) {
	store := mustTransientControllerStore(t, t.TempDir())
	journal, err := store.Create("extract-run")
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
	identity := implstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: "repo", WorkCopy: "repo", Branch: "branch", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("spec"), TaskList: ref("tasks"), Configuration: ref("config")}
	run, stateStore, err := PrepareInitialTaskExtraction(context.Background(), journal, identity, "extract")
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	if !run.TaskExtractionPending || len(run.RunOperations) != 1 {
		t.Fatalf("pre-extraction state = %#v", run)
	}
	if _, _, err := stateStore.RecordRunAttemptStart(context.Background(), run, "extract"); err != nil {
		t.Fatal(err)
	}
	// This is the crash-before-response boundary: reopening sees a reserved
	// attempt and no imported hierarchy.
	if _, err := stateStore.RecordRunAttemptOutcome(context.Background(), run, "extract", implstate.AttemptSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	response := AgentResponse{Kind: ResponseTasksExtracted, TaskIDs: []implstate.TaskID{"A", "A1"}, TaskPayloads: []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}, Binding: boundExtraction(identity)}
	if err := PersistInitialTaskExtraction(context.Background(), stateStore, run, "extract", response); err != nil {
		t.Fatal(err)
	}
	current, _, err := runstore.ReadJournalCurrent(journal)
	if err != nil {
		t.Fatal(err)
	}
	if current.TaskExtractionPending || !reflect.DeepEqual(current.PendingLeafTasks(), []implstate.TaskID{"A1"}) {
		t.Fatalf("persisted extraction = %#v", current)
	}
}

func TestExecuteInitialTaskExtractionUsesControlledFakeTurn(t *testing.T) {
	store := mustTransientControllerStore(t, t.TempDir())
	journal, err := store.Create("controlled-extract")
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
	repository := newFilesystemWorkspace(t)
	identity := implstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "branch", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("spec"), TaskList: ref("tasks"), Configuration: ref("config")}
	run, stateStore, err := PrepareInitialTaskExtraction(context.Background(), journal, identity, "extract")
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	invalid := responsePayloadMap(ResponseTasksExtracted)
	invalid["task_ids"] = []string{"A", "B", "A1"}
	invalid["task_payloads"] = []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"B","parent_id":"","title":"B"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}
	invalidRaw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	payload := responsePayloadMap(ResponseTasksExtracted)
	payload["task_ids"] = []string{"A", "A1"}
	payload["task_payloads"] = []string{`{"id":"A","parent_id":"","title":"A"}`, `{"id":"A1","parent_id":"A","title":"A1"}`}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: invalidRaw}, {raw: raw}}}
	expectation := ResponseExpectation{Role: ResponseRoleOrchestrator, State: ResponseStateExtractingTasks, Scope: ResponseScopeRun, Binding: boundExtraction(identity)}
	call := ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "thread", restart: func(context.Context) (*AgentSession, error) {
		return &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "thread"}, nil
	}}, Repository: repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: "extract"}, Run: run, Journal: journal, StateStore: stateStore, OperationID: "extract", Limits: controlledCallLimits(), Expectation: expectation, Message: "extract"}
	if _, err := ExecuteInitialTaskExtraction(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if run.TaskExtractionPending || len(run.RunResults) != 1 || len(run.Tasks) != 2 || len(run.RunOperations[0].Attempts) != 2 || run.RunOperations[0].Attempts[0].Outcome != implstate.AttemptRejected || run.RunOperations[0].Attempts[1].Outcome != implstate.AttemptSucceeded {
		t.Fatalf("completed extraction = %#v", run)
	}
}

func TestExecuteInitialTaskExtractionPausesForExecutionBlockedWithoutImportingTasks(t *testing.T) {
	store := mustTransientControllerStore(t, t.TempDir())
	journal, err := store.Create("blocked-extract")
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
	repository := newFilesystemWorkspace(t)
	identity := implstate.RunIdentity{ID: journal.ID(), Change: "change", Repository: repository, WorkCopy: repository, Branch: "branch", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("spec"), TaskList: ref("tasks"), Configuration: ref("config")}
	run, stateStore, err := PrepareInitialTaskExtraction(context.Background(), journal, identity, "extract")
	if err != nil {
		t.Fatal(err)
	}
	defer stateStore.Close()
	expectation := ResponseExpectation{Role: ResponseRoleOrchestrator, State: ResponseStateExtractingTasks, Scope: ResponseScopeRun, Binding: boundExtraction(identity)}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	result, err := ExecuteInitialTaskExtraction(context.Background(), ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleOrchestrator, runtime: runtime, thread: "orchestrator"}, Repository: repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: expectation.Binding.CallID}, Run: run, Journal: journal, StateStore: stateStore, OperationID: "extract", Limits: controlledCallLimits(), Expectation: expectation})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseExecutionBlocked || run.Status != implstate.RunPaused || run.ExecutionBlock == nil || !run.TaskExtractionPending || len(run.Tasks) != 0 || len(runtime.messages) != 1 {
		t.Fatalf("orchestrator execution block imported or retried work: result=%#v run=%#v turns=%#v", result, run, runtime.messages)
	}
}

func extractedRunIdentity() implstate.RunIdentity {
	ref := func(id implstate.EvidenceID) implstate.EvidenceRef {
		return implstate.EvidenceRef{ID: id, Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}
	return implstate.RunIdentity{ID: "run", Change: "change", Repository: "repository", WorkCopy: "work-copy", Branch: "feature", BaselineCommit: "base", BaselineState: ref("baseline"), Specification: ref("specification"), TaskList: ref("tasks"), Configuration: ref("configuration")}
}

func boundExtraction(identity implstate.RunIdentity) ResponseBinding {
	return ResponseBinding{CallID: "extract", RunID: identity.ID, Specification: identity.Specification, Configuration: identity.Configuration, TaskList: identity.TaskList}
}
