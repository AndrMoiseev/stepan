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
		state = ResponseStateBriefing
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
	if role != ResponseRoleBootstrapper {
		expectation.Binding.RunID = "run-1"
		expectation.Binding.Specification = implementationstate.EvidenceRef{ID: "specification", Digest: "spec-v1"}
		expectation.Binding.Configuration = implementationstate.EvidenceRef{ID: "configuration", Digest: "config-v1"}
		expectation.Binding.TaskList = implementationstate.EvidenceRef{ID: "tasks", Digest: "tasks-v1"}
	}
	if role != ResponseRoleBootstrapper && role != ResponseRoleOrchestrator {
		expectation.Binding.AssignmentID = "assignment-1"
		expectation.Binding.BriefID = "brief-1"
	}
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
	return map[string]any{
		"kind": string(kind), "message": "", "task_ids": []string{}, "task_payloads": []string{}, "brief": "", "check_names": []string{}, "finding_ids": []string{}, "findings": []string{}, "question": "", "context": "", "boundaries": "", "known_facts": []string{}, "references": []string{}, "options": []string{}, "recommendation": "", "blocked_action": "", "diagnostic": "", "attempts": []string{}, "required_user_action": "", "user_implementation": "", "project_implementation": "", "explanation": "",
	}
}
