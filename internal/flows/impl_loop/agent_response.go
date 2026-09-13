package impl_loop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
)

// ResponseRole is the role that owns an immutable response schema. It is
// deliberately distinct from AgentRole, which only describes post-call file
// write policy.
type ResponseRole string

const (
	ResponseRoleOrchestrator  ResponseRole = implementationconfig.RoleOrchestrator
	ResponseRoleBriefer       ResponseRole = implementationconfig.RoleBriefer
	ResponseRoleImplementer   ResponseRole = implementationconfig.RoleImplementer
	ResponseRoleTaskReviewer  ResponseRole = implementationconfig.RoleTaskReviewer
	ResponseRoleExplorer      ResponseRole = implementationconfig.RoleExplorer
	ResponseRoleFinalReviewer ResponseRole = implementationconfig.RoleFinalReviewer
	ResponseRoleBootstrapper  ResponseRole = implementationconfig.RoleBootstrapper
)

// ResponseState is the controller-owned phase in which a response is read.
// It is not supplied by an agent and intentionally does not duplicate the
// durable implementation-state model.
type ResponseState string

const (
	ResponseStateExtractingTasks ResponseState = "extracting_tasks"
	ResponseStateAddingTasks     ResponseState = "adding_tasks"
	ResponseStateReflectingTasks ResponseState = "reflecting_tasks"
	ResponseStateBriefing        ResponseState = "briefing"
	ResponseStateImplementing    ResponseState = "implementing"
	ResponseStateTaskReview      ResponseState = "task_review"
	ResponseStateFinalReview     ResponseState = "final_review"
	ResponseStateExploring       ResponseState = "exploring"
	ResponseStateBootstrapping   ResponseState = "bootstrapping"
)

// ResponseKind names one next action. An agent can never choose an arbitrary
// command: checks_requested carries check names only, and the controller
// resolves their commands from configuration.
type ResponseKind string

const (
	ResponseBriefReady           ResponseKind = "brief_ready"
	ResponseImplementationReady  ResponseKind = "implementation_ready"
	ResponseChecksRequested      ResponseKind = "checks_requested"
	ResponseReviewPassed         ResponseKind = "review_passed"
	ResponseChangesRequested     ResponseKind = "changes_requested"
	ResponseReviewDisputed       ResponseKind = "review_disputed"
	ResponseExplorationRequested ResponseKind = "exploration_requested"
	ResponseExplorationResult    ResponseKind = "exploration_result"
	ResponseClarificationNeeded  ResponseKind = "clarification_required"
	ResponseExecutionBlocked     ResponseKind = "execution_blocked"

	// These three are the orchestrator operations. They are local transport
	// names, not a user-visible OpenSpec contract.
	ResponseTasksExtracted    ResponseKind = "tasks_extracted"
	ResponseTasksAdded        ResponseKind = "tasks_added"
	ResponseProgressReflected ResponseKind = "progress_reflected"

	// ResponseConfigurationProposed belongs only to the bootstrap mode.
	ResponseConfigurationProposed ResponseKind = "configuration_proposed"
)

var (
	ErrMalformedAgentResponse = errors.New("malformed implementation agent response")
	ErrUnknownResponseKind    = errors.New("unknown implementation agent response kind")
	ErrResponseRole           = errors.New("implementation agent response is not allowed for role")
	ErrResponseState          = errors.New("implementation agent response is not allowed for state")
	ErrResponseBinding        = errors.New("implementation agent response binding is incomplete")
)

// ResponseBinding is made by the controller after a transport response has
// been decoded. Agents do not repeat run, assignment, brief, or input-version
// identifiers, so a stale or copied response cannot select another target.
type ResponseBinding struct {
	CallID        string
	RunID         implementationstate.RunID
	AssignmentID  implementationstate.AssignmentID
	BriefID       implementationstate.BriefID
	Specification implementationstate.EvidenceRef
	Configuration implementationstate.EvidenceRef
	TaskList      implementationstate.EvidenceRef
}

// ResponseExpectation is the controller's immutable context for one call.
// Bootstrap has no implementation run or assignment; its CallID still binds a
// proposal to the controller action that requested it.
type ResponseExpectation struct {
	Role    ResponseRole
	State   ResponseState
	Binding ResponseBinding
}

// AgentResponse is the normalized domain result. Nil string fields and nil
// slices mean that their required transport placeholders were empty. The
// response contains no agent-provided controller identifiers.
type AgentResponse struct {
	Kind ResponseKind

	Message               *string
	TaskIDs               []implementationstate.TaskID
	TaskPayloads          []string
	Brief                 *string
	CheckNames            []string
	FindingIDs            []string
	Findings              []string
	Question              *string
	Context               *string
	Boundaries            *string
	KnownFacts            []string
	References            []string
	Options               []string
	Recommendation        *string
	BlockedAction         *string
	Diagnostic            *string
	Attempts              []string
	RequiredUserAction    *string
	UserImplementation    *string
	ProjectImplementation *string
	Explanation           *string

	Binding ResponseBinding
}

// ResponseSchema returns the immutable, provider-neutral schema for role.
// It is intentionally a flat object schema: each property is required even
// when an action does not need it. Empty strings and empty arrays are transport
// placeholders and are normalized by BindAgentResponse before domain use.
func ResponseSchema(role ResponseRole) (json.RawMessage, error) {
	kinds, ok := responseKindsByRole[role]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrResponseRole, role)
	}
	enum := make([]string, len(kinds))
	for index, kind := range kinds {
		enum[index] = string(kind)
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":                   map[string]any{"type": "string", "enum": enum},
			"message":                map[string]any{"type": "string"},
			"task_ids":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"task_payloads":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"brief":                  map[string]any{"type": "string"},
			"check_names":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"finding_ids":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"findings":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"question":               map[string]any{"type": "string"},
			"context":                map[string]any{"type": "string"},
			"boundaries":             map[string]any{"type": "string"},
			"known_facts":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"references":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"options":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"recommendation":         map[string]any{"type": "string"},
			"blocked_action":         map[string]any{"type": "string"},
			"diagnostic":             map[string]any{"type": "string"},
			"attempts":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"required_user_action":   map[string]any{"type": "string"},
			"user_implementation":    map[string]any{"type": "string"},
			"project_implementation": map[string]any{"type": "string"},
			"explanation":            map[string]any{"type": "string"},
		},
		"required":             responseTransportFields,
		"additionalProperties": false,
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshal response schema: %w", err)
	}
	return encoded, nil
}

// BindAgentResponse decodes one role schema and attaches controller-owned
// identifiers and input versions. It performs no state transition; later
// controller work consumes the validated result.
func BindAgentResponse(expectation ResponseExpectation, raw json.RawMessage) (AgentResponse, error) {
	if err := validateExpectation(expectation); err != nil {
		return AgentResponse{}, err
	}
	transport, err := decodeResponseTransport(raw)
	if err != nil {
		return AgentResponse{}, err
	}
	kind := ResponseKind(transport.Kind)
	if !isKnownResponseKind(kind) {
		return AgentResponse{}, fmt.Errorf("%w: %q", ErrUnknownResponseKind, transport.Kind)
	}
	if !slices.Contains(responseKindsByRole[expectation.Role], kind) {
		return AgentResponse{}, fmt.Errorf("%w: kind %q for role %q", ErrResponseRole, kind, expectation.Role)
	}
	if !responseAllowedInState(kind, expectation) {
		return AgentResponse{}, fmt.Errorf("%w: kind %q for %q", ErrResponseState, kind, expectation.State)
	}
	return normalizedResponse(kind, transport, expectation.Binding), nil
}

type responseTransport struct {
	Kind                  string   `json:"kind"`
	Message               string   `json:"message"`
	TaskIDs               []string `json:"task_ids"`
	TaskPayloads          []string `json:"task_payloads"`
	Brief                 string   `json:"brief"`
	CheckNames            []string `json:"check_names"`
	FindingIDs            []string `json:"finding_ids"`
	Findings              []string `json:"findings"`
	Question              string   `json:"question"`
	Context               string   `json:"context"`
	Boundaries            string   `json:"boundaries"`
	KnownFacts            []string `json:"known_facts"`
	References            []string `json:"references"`
	Options               []string `json:"options"`
	Recommendation        string   `json:"recommendation"`
	BlockedAction         string   `json:"blocked_action"`
	Diagnostic            string   `json:"diagnostic"`
	Attempts              []string `json:"attempts"`
	RequiredUserAction    string   `json:"required_user_action"`
	UserImplementation    string   `json:"user_implementation"`
	ProjectImplementation string   `json:"project_implementation"`
	Explanation           string   `json:"explanation"`
}

var responseTransportFields = []string{
	"kind", "message", "task_ids", "task_payloads", "brief", "check_names", "finding_ids", "findings", "question", "context", "boundaries", "known_facts", "references", "options", "recommendation", "blocked_action", "diagnostic", "attempts", "required_user_action", "user_implementation", "project_implementation", "explanation",
}

var responseKindsByRole = map[ResponseRole][]ResponseKind{
	ResponseRoleOrchestrator:  {ResponseTasksExtracted, ResponseTasksAdded, ResponseProgressReflected, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleBriefer:       {ResponseBriefReady, ResponseExplorationRequested, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleImplementer:   {ResponseImplementationReady, ResponseChecksRequested, ResponseReviewDisputed, ResponseExplorationRequested, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleTaskReviewer:  {ResponseReviewPassed, ResponseChangesRequested, ResponseExplorationRequested, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleExplorer:      {ResponseExplorationResult, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleFinalReviewer: {ResponseReviewPassed, ResponseChangesRequested, ResponseExplorationRequested, ResponseClarificationNeeded, ResponseExecutionBlocked},
	ResponseRoleBootstrapper:  {ResponseConfigurationProposed, ResponseExplorationRequested, ResponseClarificationNeeded, ResponseExecutionBlocked},
}

func validateExpectation(expectation ResponseExpectation) error {
	if _, ok := responseKindsByRole[expectation.Role]; !ok || !stateBelongsToRole(expectation.State, expectation.Role) {
		return fmt.Errorf("%w: role %q and state %q", ErrResponseRole, expectation.Role, expectation.State)
	}
	binding := expectation.Binding
	if strings.TrimSpace(binding.CallID) == "" {
		return fmt.Errorf("%w: call ID is required", ErrResponseBinding)
	}
	if expectation.Role == ResponseRoleBootstrapper {
		return nil
	}
	if binding.RunID == "" || !validEvidence(binding.Specification) || !validEvidence(binding.Configuration) || !validEvidence(binding.TaskList) {
		return fmt.Errorf("%w: run and input versions are required", ErrResponseBinding)
	}
	if expectation.Role != ResponseRoleOrchestrator && (binding.AssignmentID == "" || binding.BriefID == "") {
		return fmt.Errorf("%w: assignment and brief IDs are required", ErrResponseBinding)
	}
	return nil
}

func validEvidence(value implementationstate.EvidenceRef) bool {
	return value.ID != "" && value.Digest != ""
}

func stateBelongsToRole(state ResponseState, role ResponseRole) bool {
	switch state {
	case ResponseStateExtractingTasks, ResponseStateAddingTasks, ResponseStateReflectingTasks:
		return role == ResponseRoleOrchestrator
	case ResponseStateBriefing:
		return role == ResponseRoleBriefer
	case ResponseStateImplementing:
		return role == ResponseRoleImplementer
	case ResponseStateTaskReview:
		return role == ResponseRoleTaskReviewer
	case ResponseStateFinalReview:
		return role == ResponseRoleFinalReviewer
	case ResponseStateExploring:
		return role == ResponseRoleExplorer
	case ResponseStateBootstrapping:
		return role == ResponseRoleBootstrapper
	default:
		return false
	}
}

func responseAllowedInState(kind ResponseKind, expectation ResponseExpectation) bool {
	if kind == ResponseClarificationNeeded || kind == ResponseExecutionBlocked {
		return true
	}
	if kind == ResponseExplorationRequested {
		return expectation.Role != ResponseRoleOrchestrator && expectation.Role != ResponseRoleExplorer
	}
	switch expectation.State {
	case ResponseStateExtractingTasks:
		return kind == ResponseTasksExtracted
	case ResponseStateAddingTasks:
		return kind == ResponseTasksAdded
	case ResponseStateReflectingTasks:
		return kind == ResponseProgressReflected
	case ResponseStateBriefing:
		return kind == ResponseBriefReady
	case ResponseStateImplementing:
		return kind == ResponseImplementationReady || kind == ResponseChecksRequested || kind == ResponseReviewDisputed
	case ResponseStateTaskReview, ResponseStateFinalReview:
		return kind == ResponseReviewPassed || kind == ResponseChangesRequested
	case ResponseStateExploring:
		return kind == ResponseExplorationResult
	case ResponseStateBootstrapping:
		return kind == ResponseConfigurationProposed
	default:
		return false
	}
}

func decodeResponseTransport(raw json.RawMessage) (responseTransport, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return responseTransport{}, fmt.Errorf("%w: response must be an object", ErrMalformedAgentResponse)
	}
	if len(fields) != len(responseTransportFields) {
		return responseTransport{}, fmt.Errorf("%w: response must have exactly the role schema fields", ErrMalformedAgentResponse)
	}
	for _, field := range responseTransportFields {
		if _, ok := fields[field]; !ok {
			return responseTransport{}, fmt.Errorf("%w: missing field %q", ErrMalformedAgentResponse, field)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result responseTransport
	if err := decoder.Decode(&result); err != nil {
		return responseTransport{}, fmt.Errorf("%w: response fields have invalid types", ErrMalformedAgentResponse)
	}
	if decoder.More() {
		return responseTransport{}, fmt.Errorf("%w: response has trailing values", ErrMalformedAgentResponse)
	}
	if result.TaskIDs == nil || result.TaskPayloads == nil || result.CheckNames == nil || result.FindingIDs == nil || result.Findings == nil || result.KnownFacts == nil || result.References == nil || result.Options == nil || result.Attempts == nil {
		return responseTransport{}, fmt.Errorf("%w: array placeholders must be arrays", ErrMalformedAgentResponse)
	}
	return result, nil
}

func normalizedResponse(kind ResponseKind, input responseTransport, binding ResponseBinding) AgentResponse {
	result := AgentResponse{
		Kind: kind, Message: optionalString(input.Message), Brief: optionalString(input.Brief),
		TaskPayloads: optionalStrings(input.TaskPayloads), CheckNames: optionalStrings(input.CheckNames),
		FindingIDs: optionalStrings(input.FindingIDs), Findings: optionalStrings(input.Findings),
		Question: optionalString(input.Question), Context: optionalString(input.Context), Boundaries: optionalString(input.Boundaries),
		KnownFacts: optionalStrings(input.KnownFacts), References: optionalStrings(input.References), Options: optionalStrings(input.Options),
		Recommendation: optionalString(input.Recommendation), BlockedAction: optionalString(input.BlockedAction), Diagnostic: optionalString(input.Diagnostic),
		Attempts: optionalStrings(input.Attempts), RequiredUserAction: optionalString(input.RequiredUserAction),
		UserImplementation: optionalString(input.UserImplementation), ProjectImplementation: optionalString(input.ProjectImplementation), Explanation: optionalString(input.Explanation),
		Binding: binding,
	}
	if values := optionalStrings(input.TaskIDs); values != nil {
		result.TaskIDs = make([]implementationstate.TaskID, len(values))
		for index, value := range values {
			result.TaskIDs[index] = implementationstate.TaskID(value)
		}
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return slices.Clone(values)
}

func isKnownResponseKind(kind ResponseKind) bool {
	for _, kinds := range responseKindsByRole {
		if slices.Contains(kinds, kind) {
			return true
		}
	}
	return false
}

// ThreadConfigForResponse constructs the immutable adapter input for one role.
// Keeping schema selection beside binding prevents a caller from starting a
// thread with a schema that the controller would later reject.
func ThreadConfigForResponse(role ResponseRole, config agentruntime.ThreadConfig) (agentruntime.ThreadConfig, error) {
	schema, err := ResponseSchema(role)
	if err != nil {
		return agentruntime.ThreadConfig{}, err
	}
	config = config.Clone()
	config.OutputSchema = schema
	return config, nil
}
