package impl_loop

import (
	"context"
	"encoding/json"
	"testing"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
)

func TestRecoverAgentOperationUsesDurableResultAndPreservesInterruptedAttempt(t *testing.T) {
	call := controlledCallFixture(t, &controlledCallRuntime{})
	basis := call.Run.RunOperations[0].Basis
	call.Run.RunOperations[0].Counter = implstate.CycleCounterExplorer
	call.Run.RunOperations[0].Episode = "recovery"
	if _, _, err := call.StateStore.RecordRunAttemptStartWithLimits(context.Background(), call.Run, call.OperationID, call.Limits); err != nil {
		t.Fatal(err)
	}
	response, err := BindAgentResponse(explorerExpectationFrom(t, expectationFor(ResponseRoleFinalReviewer, ResponseReviewPassed), "completed-explorer-call"), explorationResponse(t, "durably completed research"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := call.Journal.Publish("completed-explorer-result-response", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Run.AddRunResult(implstate.OperationResult{ID: "completed-explorer-result", OperationID: call.OperationID, Status: implstate.ResultSucceeded, State: call.Run.CurrentState, Basis: basis, Evidence: []implstate.EvidenceRef{evidence}}); err != nil {
		t.Fatal(err)
	}
	if err := call.Run.AddRunOperation(implstate.Operation{ID: "interrupted-parent", Kind: implstate.OperationAgent, Basis: basis, Counter: implstate.CycleCounterExplorer, Episode: "parent"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := call.StateStore.RecordRunAttemptStartWithLimits(context.Background(), call.Run, "interrupted-parent", call.Limits); err != nil {
		t.Fatal(err)
	}
	if _, err := call.StateStore.Record(context.Background(), call.Run); err != nil {
		t.Fatal(err)
	}
	if err := call.StateStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.OpenState(call.Journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, _, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	completed, err := RecoverAgentOperation(call.Journal, recovered, "", call.OperationID, "completed-explorer-result")
	if err != nil || completed.State != AgentOperationCompleted || completed.Response.Message == nil || *completed.Response.Message != "durably completed research" || completed.Attempts != 1 {
		t.Fatalf("completed auxiliary recovery = %#v, %v", completed, err)
	}
	interrupted, err := RecoverAgentOperation(call.Journal, recovered, "", "interrupted-parent", "interrupted-parent-result")
	if err != nil || interrupted.State != AgentOperationInterrupted || interrupted.Attempts != 1 {
		t.Fatalf("interrupted parent recovery = %#v, %v", interrupted, err)
	}
	// Restart the same durable operation. Its first interrupted attempt remains
	// accounted instead of creating a new operation or losing a counter.
	if _, _, err := reopened.RecordRunAttemptStartWithLimits(context.Background(), recovered, "interrupted-parent", call.Limits); err != nil {
		t.Fatal(err)
	}
	operation := finalRunOperation(recovered, "interrupted-parent")
	if operation == nil || len(operation.Attempts) != 2 || operation.Attempts[0].Outcome != "" || operation.Attempts[0].SemanticRound != operation.Attempts[1].SemanticRound || recovered.RunExplorerCounters["parent"] != 1 {
		t.Fatalf("restarted parent lost its conservative attempt accounting: %#v", operation)
	}
}
