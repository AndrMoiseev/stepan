package impl_loop

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

func TestResponseSchemasAreFlatAndBindEveryAllowedKind(t *testing.T) {
	for role, kinds := range responseKindsByRole {
		t.Run(string(role), func(t *testing.T) {
			schema, err := ResponseSchema(role)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
				OneOf      json.RawMessage            `json:"oneOf"`
			}
			if err := json.Unmarshal(schema, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.OneOf != nil || len(decoded.Properties) != len(responseTransportFields) || !reflect.DeepEqual(decoded.Required, responseTransportFields) {
				t.Fatalf("schema is not the required flat schema: %s", schema)
			}
			for _, kind := range kinds {
				raw := responsePayload(t, kind)
				response, err := BindAgentResponse(expectationFor(role, kind), raw)
				if err != nil {
					t.Fatalf("bind %q: %v", kind, err)
				}
				if response.Kind != kind || response.Binding.CallID == "" {
					t.Fatalf("bound %q response = %#v", kind, response)
				}
			}
		})
	}
}

func TestBindAgentResponseNormalizesTransportPlaceholdersAndOwnsBinding(t *testing.T) {
	expectation := expectationFor(ResponseRoleImplementer, ResponseChecksRequested)
	payload := responsePayloadMap(ResponseChecksRequested)
	payload["check_names"] = []string{"unit", "lint"}
	payload["message"] = ""
	payload["user_implementation"] = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	response, err := BindAgentResponse(expectation, raw)
	if err != nil {
		t.Fatal(err)
	}
	if response.Message != nil || response.UserImplementation != nil || !reflect.DeepEqual(response.CheckNames, []string{"unit", "lint"}) {
		t.Fatalf("transport placeholders were not normalized: %#v", response)
	}
	if response.Binding != expectation.Binding {
		t.Fatalf("controller binding changed: got %#v want %#v", response.Binding, expectation.Binding)
	}
	if response.Binding.AssignmentID != "assignment-1" || response.Binding.BriefID != "brief-1" || response.Binding.Specification.Digest != "spec-v1" || response.Binding.Configuration.Digest != "config-v1" {
		t.Fatalf("response did not retain controller identities and versions: %#v", response.Binding)
	}
}

func TestBindAgentResponseRejectsWrongRoleStateKindAndIncompleteBinding(t *testing.T) {
	valid := responsePayload(t, ResponseImplementationReady)
	wrongRole := expectationFor(ResponseRoleTaskReviewer, ResponseReviewPassed)
	if _, err := BindAgentResponse(wrongRole, valid); !errors.Is(err, ErrResponseRole) {
		t.Fatalf("wrong role error = %v", err)
	}

	wrongState := expectationFor(ResponseRoleOrchestrator, ResponseTasksAdded)
	wrongState.State = ResponseStateExtractingTasks
	if _, err := BindAgentResponse(wrongState, responsePayload(t, ResponseTasksAdded)); !errors.Is(err, ErrResponseState) {
		t.Fatalf("wrong state error = %v", err)
	}

	unknown := responsePayloadMap(ResponseImplementationReady)
	unknown["kind"] = "command_requested"
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindAgentResponse(expectationFor(ResponseRoleImplementer, ResponseImplementationReady), raw); !errors.Is(err, ErrUnknownResponseKind) {
		t.Fatalf("unknown kind error = %v", err)
	}

	incomplete := expectationFor(ResponseRoleImplementer, ResponseImplementationReady)
	incomplete.Binding.BriefID = ""
	if _, err := BindAgentResponse(incomplete, valid); !errors.Is(err, ErrResponseBinding) {
		t.Fatalf("incomplete binding error = %v", err)
	}
}

func TestBindAgentResponseBindsRunAssignmentAndBootstrapScopes(t *testing.T) {
	tests := []struct {
		name        string
		role        ResponseRole
		kind        ResponseKind
		state       ResponseState
		source      ExplorerSource
		wantScope   ResponseScope
		removeScope bool
	}{
		{"initial briefer is run scoped", ResponseRoleBriefer, ResponseBriefReady, ResponseStateInitialBriefing, "", ResponseScopeRun, false},
		{"final reviewer is run scoped", ResponseRoleFinalReviewer, ResponseReviewPassed, ResponseStateFinalReview, "", ResponseScopeRun, false},
		{"initial briefer explorer is run scoped", ResponseRoleExplorer, ResponseExplorationResult, ResponseStateExploring, ExplorerSourceInitialBriefing, ResponseScopeRun, false},
		{"final reviewer explorer is run scoped", ResponseRoleExplorer, ResponseExplorationResult, ResponseStateExploring, ExplorerSourceFinalReviewer, ResponseScopeRun, false},
		{"brief refinement is assignment scoped", ResponseRoleBriefer, ResponseBriefReady, ResponseStateBriefRefinement, "", ResponseScopeAssignment, false},
		{"brief refinement explorer is assignment scoped", ResponseRoleExplorer, ResponseExplorationResult, ResponseStateExploring, ExplorerSourceBriefRefinement, ResponseScopeAssignment, false},
		{"bootstrap is configuration scoped", ResponseRoleBootstrapper, ResponseConfigurationProposed, ResponseStateBootstrapping, "", ResponseScopeBootstrap, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectation := expectationFor(test.role, test.kind)
			expectation.State, expectation.ExplorerSource, expectation.Scope = test.state, test.source, test.wantScope
			if test.wantScope == ResponseScopeAssignment {
				expectation.Binding.AssignmentID = "assignment-1"
				expectation.Binding.BriefID = "brief-1"
			} else if test.wantScope == ResponseScopeRun {
				expectation.Binding.AssignmentID = ""
				expectation.Binding.BriefID = ""
			}
			if _, err := BindAgentResponse(expectation, responsePayload(t, test.kind)); err != nil {
				t.Fatalf("positive %s scope: %v", test.wantScope, err)
			}
		})
	}

	for _, test := range []struct {
		name        string
		expectation ResponseExpectation
		kind        ResponseKind
	}{
		{"run requires captured inputs", removeRunInputs(), ResponseBriefReady},
		{"run scope rejects fabricated assignment", runWithFabricatedAssignment(), ResponseBriefReady},
		{"assignment requires assignment and brief", removeAssignmentInputs(), ResponseBriefReady},
		{"bootstrap requires both captured configuration inputs", removeBootstrapInputs(), ResponseConfigurationProposed},
		{"explorer source must match scope", explorerWithWrongScope(), ResponseExplorationResult},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BindAgentResponse(test.expectation, responsePayload(t, test.kind)); !errors.Is(err, ErrResponseBinding) {
				t.Fatalf("scope error = %v", err)
			}
		})
	}
}

func TestBindAgentResponseRejectsIncompleteSemanticResults(t *testing.T) {
	tests := []struct {
		name   string
		kind   ResponseKind
		mutate func(map[string]any)
	}{
		{"implementation message", ResponseImplementationReady, func(p map[string]any) { p["message"] = " \t " }},
		{"execution blocked", ResponseExecutionBlocked, func(p map[string]any) { p["diagnostic"] = "" }},
		{"clarification", ResponseClarificationNeeded, func(p map[string]any) { p["references"] = []string{} }},
		{"exploration request", ResponseExplorationRequested, func(p map[string]any) { p["boundaries"] = "" }},
		{"exploration result", ResponseExplorationResult, func(p map[string]any) { p["unknowns"] = []string{} }},
		{"review passed", ResponseReviewPassed, func(p map[string]any) { p["references"] = []string{} }},
		{"review finding consistency", ResponseChangesRequested, func(p map[string]any) { p["locations"] = []string{"one", "two"} }},
		{"review dispute", ResponseReviewDisputed, func(p map[string]any) { p["finding_ids"] = []string{"one", "two"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := responsePayloadMap(test.kind)
			test.mutate(payload)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := BindAgentResponse(expectationFor(roleForKind(test.kind), test.kind), raw); !errors.Is(err, ErrResponseSemantics) {
				t.Fatalf("semantic error = %v", err)
			}
		})
	}
}

func expectationFor(role ResponseRole, kind ResponseKind) ResponseExpectation {
	state := ResponseStateImplementing
	switch role {
	case ResponseRoleOrchestrator:
		switch kind {
		case ResponseTasksAdded:
			state = ResponseStateAddingTasks
		case ResponseProgressReflected:
			state = ResponseStateReflectingTasks
		default:
			state = ResponseStateExtractingTasks
		}
	case ResponseRoleBriefer:
		state = ResponseStateInitialBriefing
	case ResponseRoleTaskReviewer:
		state = ResponseStateTaskReview
	case ResponseRoleExplorer:
		state = ResponseStateExploring
	case ResponseRoleFinalReviewer:
		state = ResponseStateFinalReview
	case ResponseRoleBootstrapper:
		state = ResponseStateBootstrapping
	}
	expectation := ResponseExpectation{Role: role, State: state, Binding: ResponseBinding{CallID: "call-1"}}
	if role == ResponseRoleExplorer {
		expectation.ExplorerSource = ExplorerSourceImplementer
	}
	if role != ResponseRoleBootstrapper {
		expectation.Binding.RunID = "run-1"
		expectation.Binding.Specification = implementationstate.EvidenceRef{ID: "specification", Digest: "spec-v1"}
		expectation.Binding.Configuration = implementationstate.EvidenceRef{ID: "configuration", Digest: "config-v1"}
		expectation.Binding.TaskList = implementationstate.EvidenceRef{ID: "tasks", Digest: "tasks-v1"}
	}
	if role == ResponseRoleBootstrapper {
		expectation.Binding.UserConfiguration = implementationstate.EvidenceRef{ID: "user-settings", Digest: "user-config-v1"}
		expectation.Binding.ProjectConfiguration = implementationstate.EvidenceRef{ID: "project-settings", Digest: "project-config-v1"}
	}
	if scopeForExpectation(expectation) == ResponseScopeAssignment {
		expectation.Binding.AssignmentID = "assignment-1"
		expectation.Binding.BriefID = "brief-1"
	}
	expectation.Scope = scopeForExpectation(expectation)
	return expectation
}

func responsePayload(t *testing.T, kind ResponseKind) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(responsePayloadMap(kind))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func responsePayloadMap(kind ResponseKind) map[string]any {
	payload := map[string]any{
		"kind": string(kind), "message": "", "task_ids": []string{}, "task_payloads": []string{}, "brief": "", "check_names": []string{}, "finding_ids": []string{}, "findings": []string{}, "question": "", "context": "", "boundaries": "", "known_facts": []string{}, "unknowns": []string{}, "references": []string{}, "locations": []string{}, "bases": []string{}, "expected_results": []string{}, "options": []string{}, "recommendation": "", "blocked_action": "", "diagnostic": "", "attempts": []string{}, "required_user_action": "", "user_implementation": "", "project_implementation": "", "explanation": "",
	}
	switch kind {
	case ResponseBriefReady:
		payload["task_ids"], payload["brief"] = []string{"task-1"}, "# assignment brief"
	case ResponseImplementationReady:
		payload["message"] = "implement feature"
	case ResponseChecksRequested:
		payload["check_names"] = []string{"unit"}
	case ResponseReviewPassed:
		payload["message"], payload["references"] = "review passed", []string{"internal/example.go:12"}
	case ResponseChangesRequested:
		payload["finding_ids"], payload["findings"], payload["locations"], payload["bases"], payload["expected_results"] = []string{"F-1"}, []string{"missing validation"}, []string{"internal/example.go:12"}, []string{"rules.md#validation"}, []string{"reject invalid input"}
	case ResponseReviewDisputed:
		payload["finding_ids"], payload["message"], payload["references"] = []string{"F-1"}, "the validation is already present", []string{"internal/example.go:12"}
	case ResponseExplorationRequested:
		payload["question"], payload["context"], payload["boundaries"], payload["known_facts"] = "where is validation?", "assignment needs validation", "inspect internal only", []string{"the assignment touches input parsing"}
	case ResponseExplorationResult:
		payload["message"], payload["known_facts"], payload["references"], payload["unknowns"] = "validation is centralized", []string{"parser validates input"}, []string{"internal/parser.go:12"}, []string{"no direct caller was found"}
	case ResponseClarificationNeeded:
		payload["question"], payload["context"], payload["boundaries"], payload["references"] = "which behavior is required?", "the specification conflicts", "commit is blocked", []string{"spec.md#scenario"}
	case ResponseExecutionBlocked:
		payload["blocked_action"], payload["diagnostic"], payload["attempts"], payload["required_user_action"] = "run required checks", "tool is not installed", []string{"checked PATH", "read project settings"}, "install the configured tool"
	case ResponseTasksExtracted, ResponseTasksAdded:
		payload["task_ids"], payload["task_payloads"] = []string{"task-1"}, []string{"{\"id\":\"task-1\"}"}
	case ResponseProgressReflected:
		payload["task_ids"] = []string{"task-1"}
	case ResponseConfigurationProposed:
		payload["project_implementation"], payload["explanation"] = "{\"checks\":{}}", "detected project checks"
	}
	return payload
}

func roleForKind(kind ResponseKind) ResponseRole {
	for role, kinds := range responseKindsByRole {
		if containsKind(kinds, kind) {
			return role
		}
	}
	panic("missing role for response kind " + string(kind))
}

func containsKind(kinds []ResponseKind, want ResponseKind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func removeRunInputs() ResponseExpectation {
	expectation := expectationFor(ResponseRoleBriefer, ResponseBriefReady)
	expectation.Binding.Specification = implementationstate.EvidenceRef{}
	return expectation
}

func runWithFabricatedAssignment() ResponseExpectation {
	expectation := expectationFor(ResponseRoleBriefer, ResponseBriefReady)
	expectation.Binding.AssignmentID = "fabricated-assignment"
	expectation.Binding.BriefID = "fabricated-brief"
	return expectation
}

func removeAssignmentInputs() ResponseExpectation {
	expectation := expectationFor(ResponseRoleBriefer, ResponseBriefReady)
	expectation.State = ResponseStateBriefRefinement
	expectation.Scope = ResponseScopeAssignment
	expectation.Binding.AssignmentID, expectation.Binding.BriefID = "", ""
	return expectation
}

func removeBootstrapInputs() ResponseExpectation {
	expectation := expectationFor(ResponseRoleBootstrapper, ResponseConfigurationProposed)
	expectation.Binding.ProjectConfiguration = implementationstate.EvidenceRef{}
	return expectation
}

func explorerWithWrongScope() ResponseExpectation {
	expectation := expectationFor(ResponseRoleExplorer, ResponseExplorationResult)
	expectation.ExplorerSource = ExplorerSourceInitialBriefing
	expectation.Scope = ResponseScopeAssignment
	return expectation
}
