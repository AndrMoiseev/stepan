package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestRouteImplementerChecksContinuesExactExecutorSessionWithSafeFeedback(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"executor-origin", "executor-continuation"} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterNone}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	requested := responsePayloadMap(ResponseChecksRequested)
	requested["check_names"] = []string{"test_auth"}
	requestedRaw, err := json.Marshal(requested)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: requestedRaw}, {raw: responsePayload(t, ResponseImplementationReady)}}}
	session := &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}
	originExpectation := fixture.executorExpectation("executor-origin-call")
	continuationExpectation := fixture.executorExpectation("executor-continuation-call")
	transition := fixture.input("requested-checks", "requested-checks-result")
	requiredTransition := fixture.input("required-checks", "required-checks-result")
	transition.Runner = CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		fixture.runner.commands = append(fixture.runner.commands, command)
		return checkexec.Result{ExitCode: 0, Stdout: []byte("bounded executor diagnostic")}, nil
	})

	result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{
		OriginatingCall: ControlledAgentCall{
			Session: session, Repository: fixture.repository, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true},
			Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment",
		},
		Transition: transition,
		Continuation: ControlledAgentCall{
			Repository: fixture.repository, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: continuationExpectation.Binding.CallID, AllowUnprotected: true},
			Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-continuation", Limits: controlledCallLimits(), Expectation: continuationExpectation,
		},
		ContinuationTransition: requiredTransition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseChecksRequested || result.ContinuationResponse.Kind != ResponseImplementationReady || result.ResponseAttempts != 1 || result.ContinuationAttempts != 1 || !result.RequiredChecks.RequiredAcceptance {
		t.Fatalf("route result = %#v", result)
	}
	if got, want := runtime.threads, []any{"same-executor-thread", "same-executor-thread"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("executor continuation did not use same thread: %#v", got)
	}
	if len(runtime.messages) != 2 || runtime.messages[0] != "implement the assignment" || runtime.messages[1] != result.Feedback {
		t.Fatalf("executor turns = %#v", runtime.messages)
	}
	for _, fragment := range []string{"# Configured check results", "## test_auth", "- Status: succeeded", "- Command: \"test_auth\" \"entire configured check\"", "Checked state: requested-checks-result-check-1-test_auth-state", "Stdout log: requested-checks-result-check-1-test_auth-stdout", "bounded executor diagnostic"} {
		if !strings.Contains(result.Feedback, fragment) {
			t.Fatalf("feedback missing %q:\n%s", fragment, result.Feedback)
		}
	}
	if strings.Contains(result.Feedback, "CHECK") || strings.Contains(result.Feedback, "Env") {
		t.Fatalf("feedback exposed command environment:\n%s", result.Feedback)
	}
	assertRunOrder(t, fixture.runner, "test_auth", "lint", "test_all")
	if fixture.run.Assignments[0].Status != implementationstate.AssignmentActive || fixture.run.LeafStatus["task"] != implementationstate.TaskPending {
		t.Fatalf("automatic required checks accepted or committed assignment: %#v", fixture.run.Assignments[0])
	}
}

func TestRouteImplementerChecksInitialReadyRunsRequiredSetWithoutRetry(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: "executor-origin", Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterNone}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseImplementationReady)}}}
	originExpectation := fixture.executorExpectation("executor-origin-call")
	result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{
		OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}, Repository: fixture.repository, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment"},
		Transition:      fixture.input("required-checks", "required-checks-result"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseImplementationReady || result.ResponseAttempts != 1 || !result.RequiredChecks.RequiredAcceptance || len(runtime.messages) != 1 {
		t.Fatalf("initial ready route = %#v, turns=%#v", result, runtime.messages)
	}
	assertRunOrder(t, fixture.runner, "lint", "test_all")
	if fixture.run.Assignments[0].Status != implementationstate.AssignmentActive || fixture.run.LeafStatus["task"] != implementationstate.TaskPending {
		t.Fatalf("initial ready accepted or committed assignment: %#v", fixture.run.Assignments[0])
	}
}

func TestRouteImplementerChecksRejectsMismatchedOriginatingCallBinding(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"executor-origin", "executor-continuation"} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis, Counter: implementationstate.CycleCounterNone}); err != nil {
			t.Fatal(err)
		}
	}
	originExpectation := fixture.executorExpectation("executor-origin-call")
	continuationExpectation := fixture.executorExpectation("executor-continuation-call")
	transition := fixture.input("requested-checks", "requested-checks-result")
	transition.BriefID = "different-brief"
	route := ImplementerCheckRoute{
		OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer}, Repository: fixture.repository, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation},
		Transition:      transition,
		Continuation:    ControlledAgentCall{Repository: fixture.repository, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: continuationExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-continuation", Limits: controlledCallLimits(), Expectation: continuationExpectation},
	}
	if _, err := RouteImplementerChecks(context.Background(), route); !errors.Is(err, ErrInvalidImplementerRoute) {
		t.Fatalf("mismatched originating binding error = %v", err)
	}
}

func (fixture implementerTransitionFixture) executorExpectation(callID string) ResponseExpectation {
	binding := fixture.binding()
	binding.CallID = callID
	return ResponseExpectation{Role: ResponseRoleImplementer, State: ResponseStateImplementing, Scope: ResponseScopeAssignment, Binding: binding}
}
