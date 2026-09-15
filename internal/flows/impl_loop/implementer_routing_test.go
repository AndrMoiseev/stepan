package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
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
			Session: session, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true},
			Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment",
		},
		Transition: transition,
		Continuation: ControlledAgentCall{
			Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: continuationExpectation.Binding.CallID, AllowUnprotected: true},
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
		OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment"},
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

func TestRouteImplementerChecksPausesForExecutorExecutionBlockedWithoutChecks(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: "executor-origin", Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}
	expectation := fixture.executorExpectation("executor-origin-call")
	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseExecutionBlocked)}}}
	result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{
		OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "executor"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: expectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: expectation},
		Transition:      fixture.input("blocked-checks", "blocked-checks-result"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Kind != ResponseExecutionBlocked || fixture.run.Status != implementationstate.RunPaused || fixture.run.ExecutionBlock == nil || len(runtime.messages) != 1 || len(fixture.runner.commands) != 0 {
		t.Fatalf("executor execution block advanced work: result=%#v run=%#v turns=%#v checks=%#v", result, fixture.run, runtime.messages, fixture.runner.commands)
	}
}

func TestRouteImplementerChecksPausesForUnavailableRequiredCheckInfrastructure(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*implementerTransitionFixture, *ImplementerTransitionInput)
	}{
		{name: "platform command unavailable", prepare: func(fixture *implementerTransitionFixture, _ *ImplementerTransitionInput) {
			check := fixture.selection.Checks["lint"]
			check.Available = false
			fixture.selection.Checks["lint"] = check
		}},
		{name: "check launch failure", prepare: func(_ *implementerTransitionFixture, input *ImplementerTransitionInput) {
			input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureLaunch}, errors.New("configured tool is missing")
			})
		}},
		{name: "check infrastructure failure", prepare: func(_ *implementerTransitionFixture, input *ImplementerTransitionInput) {
			input.Runner = CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
				return checkexec.Result{ExitCode: -1, Failure: checkexec.FailureInfrastructure}, errors.New("process supervisor is unavailable")
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newImplementerTransitionFixture(t)
			defer fixture.state.Close()
			basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
			if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: "executor-origin", Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
				t.Fatal(err)
			}
			transition := fixture.input("required-checks", "required-checks-result")
			test.prepare(&fixture, &transition)
			expectation := fixture.executorExpectation("executor-origin-call")
			runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseImplementationReady)}}}
			result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "executor"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: expectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: expectation}, Transition: transition})
			if err != nil {
				t.Fatal(err)
			}
			if result.RequiredChecks.ExecutionBlock == nil || fixture.run.Status != implementationstate.RunPaused || fixture.run.ExecutionBlock == nil || len(runtime.messages) != 1 || len(fixture.run.Assignments[0].Results) != 1 || fixture.run.Assignments[0].Results[0].Status != implementationstate.ResultFailed || CanStartTaskReview(fixture.run, "assignment") == nil {
				t.Fatalf("required infrastructure failure was treated as executor feedback: result=%#v run=%#v turns=%#v", result, fixture.run, runtime.messages)
			}
		})
	}
}

func TestRouteImplementerChecksRestartsAfterLateRequiredCheckMutatesWorkspace(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"executor-origin", "executor-stability"} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseImplementationReady)}, {raw: responsePayload(t, ResponseImplementationReady)}}}
	originExpectation := fixture.executorExpectation("executor-origin-call")
	stabilityExpectation := fixture.executorExpectation("executor-stability-call")
	runner := CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		fixture.runner.commands = append(fixture.runner.commands, command)
		return checkexec.Result{ExitCode: 0}, nil
	})
	firstTransition := fixture.input("required-mutating", "required-mutating-result")
	firstTransition.Runner = runner
	firstTransition.Workspace = &scriptedWorkspaceControl{differences: []gitsnapshot.Difference{{Paths: []string{"generated.go"}}, {Paths: []string{"generated.go"}}}}
	secondTransition := fixture.input("required-stable", "required-stable-result")
	secondTransition.Runner = runner

	result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{
		OriginatingCall:        ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment"},
		Transition:             firstTransition,
		Continuation:           ControlledAgentCall{Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: stabilityExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-stability", Limits: controlledCallLimits(), Expectation: stabilityExpectation},
		ContinuationTransition: secondTransition,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, fixture.runner, "lint", "test_all", "lint", "test_all")
	if !result.ReviewReady || result.RequiredChecks.WorkspaceChanged || !result.RequiredChecks.Set.Succeeded() {
		t.Fatalf("stable required set did not solely enable review: %#v", result)
	}
	if len(fixture.run.Assignments[0].Results) != 2 || fixture.run.Assignments[0].Results[0].Status != implementationstate.ResultFailed || fixture.run.Assignments[0].Results[1].Status != implementationstate.ResultSucceeded {
		t.Fatalf("mutating required set remained acceptance evidence: %#v", fixture.run.Assignments[0].Results)
	}
	if len(runtime.messages) != 2 || !strings.Contains(runtime.messages[1], "not acceptance evidence") {
		t.Fatalf("workspace-mutation feedback = %#v", runtime.messages)
	}
}

func TestRouteImplementerChecksRestartsFullRequiredSetAfterCorrection(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"executor-origin", "executor-correction"} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseImplementationReady)}, {raw: responsePayload(t, ResponseImplementationReady)}}}
	originExpectation := fixture.executorExpectation("executor-origin-call")
	correctionExpectation := fixture.executorExpectation("executor-correction-call")
	runs := 0
	runner := CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		fixture.runner.commands = append(fixture.runner.commands, command)
		runs++
		if runs == 1 {
			return checkexec.Result{ExitCode: 1, Stderr: []byte("first required set failed")}, nil
		}
		return checkexec.Result{ExitCode: 0}, nil
	})
	firstTransition := fixture.input("required-first", "required-first-result")
	firstTransition.Runner = runner
	secondTransition := fixture.input("required-second", "required-second-result")
	secondTransition.Runner = runner

	result, err := RouteImplementerChecks(context.Background(), ImplementerCheckRoute{
		OriginatingCall:        ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation, Message: "implement the assignment"},
		Transition:             firstTransition,
		Continuation:           ControlledAgentCall{Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: correctionExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-correction", Limits: controlledCallLimits(), Expectation: correctionExpectation},
		ContinuationTransition: secondTransition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RequiredChecks.Set.Succeeded() || result.RequiredChecks.Set.Kind != CheckSetRequired {
		t.Fatalf("last required set = %#v", result.RequiredChecks)
	}
	assertRunOrder(t, fixture.runner, "lint", "lint", "test_all")
	if len(runtime.messages) != 2 || !strings.Contains(runtime.messages[1], "first required set failed") {
		t.Fatalf("correction feedback = %#v", runtime.messages)
	}
	if counters := fixture.run.Assignments[0].Counters; counters.MandatoryChecks != 0 || counters.MandatoryChecksCycle != 2 {
		t.Fatalf("successful second required set did not open only its next cycle: %#v", counters)
	}
}

func TestRouteImplementerChecksPausesBeforeFourthFailedRequiredSet(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	basis := implementationstate.AcceptanceBasis{Specification: fixture.run.Identity.Specification, Configuration: fixture.run.Identity.Configuration}
	for _, id := range []implementationstate.OperationID{"executor-origin", "executor-correction-1", "executor-correction-2", "executor-correction-3"} {
		if err := fixture.run.AddOperation("assignment", implementationstate.Operation{ID: id, Kind: implementationstate.OperationAgent, BriefID: "brief", Basis: basis}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	runtime := &controlledCallRuntime{turns: []controlledTurn{{raw: responsePayload(t, ResponseImplementationReady)}, {raw: responsePayload(t, ResponseImplementationReady)}, {raw: responsePayload(t, ResponseImplementationReady)}, {raw: responsePayload(t, ResponseImplementationReady)}}}
	expectation := func(id string) ResponseExpectation { return fixture.executorExpectation(id) }
	runner := CheckRunnerFunc(func(_ context.Context, command checkexec.Command) (checkexec.Result, error) {
		fixture.runner.commands = append(fixture.runner.commands, command)
		return checkexec.Result{ExitCode: 1, Stderr: []byte("still failing")}, nil
	})
	makeCall := func(operationID implementationstate.OperationID, callID string) ControlledAgentCall {
		return ControlledAgentCall{Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: callID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: operationID, Limits: controlledCallLimits(), Expectation: expectation(callID)}
	}
	input := func(operation implementationstate.OperationID, result implementationstate.ResultID) ImplementerTransitionInput {
		transition := fixture.input(operation, result)
		transition.Runner = runner
		return transition
	}
	route := ImplementerCheckRoute{
		OriginatingCall:        makeCall("executor-origin", "executor-origin-call"),
		Transition:             input("required-1", "required-result-1"),
		Continuation:           makeCall("executor-correction-1", "executor-correction-call-1"),
		ContinuationTransition: input("required-2", "required-result-2"),
		FurtherContinuations: []ImplementerContinuation{
			{Call: makeCall("executor-correction-2", "executor-correction-call-2"), Transition: input("required-3", "required-result-3")},
			{Call: makeCall("executor-correction-3", "executor-correction-call-3"), Transition: input("required-4", "required-result-4")},
		},
	}
	route.OriginatingCall.Session = &AgentSession{Role: ResponseRoleImplementer, runtime: runtime, thread: "same-executor-thread"}
	_, err := RouteImplementerChecks(context.Background(), route)
	if !errors.Is(err, implementationstate.ErrLimitExceeded) {
		t.Fatalf("fourth required set error = %v, want limit pause", err)
	}
	if fixture.run.Status != implementationstate.RunPaused || fixture.run.LimitPause == nil || fixture.run.LimitPause.Counter != implementationstate.CycleCounterMandatoryChecks {
		t.Fatalf("fourth required set did not pause mandatory cycle: %#v", fixture.run)
	}
	assertRunOrder(t, fixture.runner, "lint", "lint", "lint")
	if got := fixture.run.Assignments[0].Counters.MandatoryChecks; got != 3 {
		t.Fatalf("mandatory attempts = %d, want three", got)
	}
	if operation := fixture.run.Assignments[0].Operations[len(fixture.run.Assignments[0].Operations)-1]; operation.ID != "required-4" || len(operation.Attempts) != 0 {
		t.Fatalf("fourth required operation was dispatched: %#v", operation)
	}
}

func TestTaskReviewReadinessRequiresFreshMandatorySetAfterCorrection(t *testing.T) {
	fixture := newImplementerTransitionFixture(t)
	defer fixture.state.Close()
	first := fixture.input("required-first", "required-first-result")
	if _, err := ApplyImplementerTransition(context.Background(), first, fixture.response(ResponseImplementationReady, nil)); err != nil {
		t.Fatal(err)
	}
	if err := CanStartTaskReview(fixture.run, "assignment"); err != nil {
		t.Fatalf("successful current required set did not enable review: %v", err)
	}

	// This is the state observed after the implementer applies a reviewer
	// correction. It must make the prior full set unusable before the next
	// reviewer session can start.
	corrected, err := fixture.journal.Publish("post-review-correction", []byte("corrected code state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.run.ObserveCodeState(corrected); err != nil {
		t.Fatal(err)
	}
	if err := CanStartTaskReview(fixture.run, "assignment"); !errors.Is(err, ErrTaskReviewNotReady) {
		t.Fatalf("stale mandatory result enabled review: %v", err)
	}
	if _, err := fixture.state.Record(context.Background(), fixture.run); err != nil {
		t.Fatal(err)
	}

	second := fixture.input("required-after-correction", "required-after-correction-result")
	if _, err := ApplyImplementerTransition(context.Background(), second, fixture.response(ResponseImplementationReady, nil)); err != nil {
		t.Fatal(err)
	}
	assertRunOrder(t, fixture.runner, "lint", "test_all", "lint", "test_all")
	if err := CanStartTaskReview(fixture.run, "assignment"); err != nil {
		t.Fatalf("fresh mandatory result did not enable review: %v", err)
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
		OriginatingCall: ControlledAgentCall{Session: &AgentSession{Role: ResponseRoleImplementer}, Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: originExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-origin", Limits: controlledCallLimits(), Expectation: originExpectation},
		Transition:      transition,
		Continuation:    ControlledAgentCall{Repository: fixture.repository, Workspace: &unchangedWorkspaceControl{}, Policy: AgentCallPolicy{Role: AgentRoleExecutor, CallID: continuationExpectation.Binding.CallID, AllowUnprotected: true}, Run: fixture.run, Journal: fixture.journal, StateStore: fixture.state, AssignmentID: "assignment", OperationID: "executor-continuation", Limits: controlledCallLimits(), Expectation: continuationExpectation},
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
