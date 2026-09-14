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
	// Initial briefing is run-scoped; a refinement belongs to an already
	// selected assignment and its current brief version.
	ResponseStateInitialBriefing ResponseState = "initial_briefing"
	ResponseStateBriefRefinement ResponseState = "brief_refinement"
	ResponseStateImplementing    ResponseState = "implementing"
	ResponseStateTaskReview      ResponseState = "task_review"
	ResponseStateFinalReview     ResponseState = "final_review"
	ResponseStateExploring       ResponseState = "exploring"
	ResponseStateBootstrapping   ResponseState = "bootstrapping"
)

// ResponseScope identifies the durable controller object that owns a call.
// It prevents a run-level call from inventing an assignment merely to satisfy
// a generic response contract.
type ResponseScope string

const (
	ResponseScopeRun        ResponseScope = "run"
	ResponseScopeAssignment ResponseScope = "assignment"
	ResponseScopeBootstrap  ResponseScope = "bootstrap"
)

// ExplorerSource identifies the paused role whose request an Explorer call
// serves. The source is controller-owned, just like every other binding.
type ExplorerSource string

const (
	ExplorerSourceInitialBriefing ExplorerSource = "initial_briefing"
	ExplorerSourceBriefRefinement ExplorerSource = "brief_refinement"
	ExplorerSourceImplementer     ExplorerSource = "implementer"
	ExplorerSourceTaskReviewer    ExplorerSource = "task_reviewer"
	ExplorerSourceFinalReviewer   ExplorerSource = "final_reviewer"
	ExplorerSourceBootstrapper    ExplorerSource = "bootstrapper"
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
	ErrResponseSemantics      = errors.New("implementation agent response has invalid semantic fields")
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
	// Bootstrap captures each editable configuration input independently so a
	// proposal cannot be applied to a configuration the agent did not inspect.
	UserConfiguration    implementationstate.EvidenceRef
	ProjectConfiguration implementationstate.EvidenceRef
}

// ResponseExpectation is the controller's immutable context for one call.
// Bootstrap has no implementation run or assignment; its CallID still binds a
// proposal to the controller action that requested it.
type ResponseExpectation struct {
	Role           ResponseRole
	State          ResponseState
	Scope          ResponseScope
	ExplorerSource ExplorerSource
	Binding        ResponseBinding
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
	FindingDecisions      []string
	FindingReasons        []string
	Question              *string
	Context               *string
	Boundaries            *string
	KnownFacts            []string
	Unknowns              []string
	References            []string
	Locations             []string
	Bases                 []string
	ExpectedResults       []string
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
			"finding_decisions":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"finding_reasons":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"question":               map[string]any{"type": "string"},
			"context":                map[string]any{"type": "string"},
			"boundaries":             map[string]any{"type": "string"},
			"known_facts":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"unknowns":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"references":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"locations":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"bases":                  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"expected_results":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
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
	response := normalizedResponse(kind, transport, expectation.Binding)
	if err := validateResponseSemantics(response); err != nil {
		return AgentResponse{}, err
	}
	return response, nil
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
	FindingDecisions      []string `json:"finding_decisions"`
	FindingReasons        []string `json:"finding_reasons"`
	Question              string   `json:"question"`
	Context               string   `json:"context"`
	Boundaries            string   `json:"boundaries"`
	KnownFacts            []string `json:"known_facts"`
	Unknowns              []string `json:"unknowns"`
	References            []string `json:"references"`
	Locations             []string `json:"locations"`
	Bases                 []string `json:"bases"`
	ExpectedResults       []string `json:"expected_results"`
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
	"kind", "message", "task_ids", "task_payloads", "brief", "check_names", "finding_ids", "findings", "finding_decisions", "finding_reasons", "question", "context", "boundaries", "known_facts", "unknowns", "references", "locations", "bases", "expected_results", "options", "recommendation", "blocked_action", "diagnostic", "attempts", "required_user_action", "user_implementation", "project_implementation", "explanation",
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
	if expected := scopeForExpectation(expectation); expectation.Scope != expected {
		return fmt.Errorf("%w: role %q state %q requires %q scope", ErrResponseBinding, expectation.Role, expectation.State, expected)
	}
	if expectation.Role == ResponseRoleExplorer {
		if !validExplorerSource(expectation.ExplorerSource) || explorerSourceScope(expectation.ExplorerSource) != expectation.Scope {
			return fmt.Errorf("%w: explorer source %q does not match %q scope", ErrResponseBinding, expectation.ExplorerSource, expectation.Scope)
		}
	} else if expectation.ExplorerSource != "" {
		return fmt.Errorf("%w: only Explorer calls have an explorer source", ErrResponseBinding)
	}
	binding := expectation.Binding
	if strings.TrimSpace(binding.CallID) == "" {
		return fmt.Errorf("%w: call ID is required", ErrResponseBinding)
	}
	if expectation.Scope == ResponseScopeBootstrap {
		if !validEvidence(binding.UserConfiguration) || !validEvidence(binding.ProjectConfiguration) {
			return fmt.Errorf("%w: bootstrap configuration inputs are required", ErrResponseBinding)
		}
		if binding.RunID != "" || binding.AssignmentID != "" || binding.BriefID != "" {
			return fmt.Errorf("%w: bootstrap calls must not carry implementation identifiers", ErrResponseBinding)
		}
		return nil
	}
	if binding.RunID == "" || !validEvidence(binding.Specification) || !validEvidence(binding.Configuration) || !validEvidence(binding.TaskList) {
		return fmt.Errorf("%w: run and input versions are required", ErrResponseBinding)
	}
	if expectation.Scope == ResponseScopeAssignment && (binding.AssignmentID == "" || binding.BriefID == "") {
		return fmt.Errorf("%w: assignment and brief IDs are required", ErrResponseBinding)
	}
	if expectation.Scope == ResponseScopeRun && (binding.AssignmentID != "" || binding.BriefID != "") {
		return fmt.Errorf("%w: run-scoped calls must not carry assignment or brief IDs", ErrResponseBinding)
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
	case ResponseStateInitialBriefing, ResponseStateBriefRefinement:
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

func scopeForExpectation(expectation ResponseExpectation) ResponseScope {
	switch expectation.Role {
	case ResponseRoleBootstrapper:
		return ResponseScopeBootstrap
	case ResponseRoleOrchestrator, ResponseRoleFinalReviewer:
		return ResponseScopeRun
	case ResponseRoleBriefer:
		if expectation.State == ResponseStateInitialBriefing {
			return ResponseScopeRun
		}
		return ResponseScopeAssignment
	case ResponseRoleExplorer:
		return explorerSourceScope(expectation.ExplorerSource)
	default:
		return ResponseScopeAssignment
	}
}

func validExplorerSource(source ExplorerSource) bool {
	return source == ExplorerSourceInitialBriefing || source == ExplorerSourceBriefRefinement || source == ExplorerSourceImplementer || source == ExplorerSourceTaskReviewer || source == ExplorerSourceFinalReviewer || source == ExplorerSourceBootstrapper
}

func explorerSourceScope(source ExplorerSource) ResponseScope {
	switch source {
	case ExplorerSourceInitialBriefing, ExplorerSourceFinalReviewer:
		return ResponseScopeRun
	case ExplorerSourceBootstrapper:
		return ResponseScopeBootstrap
	default:
		return ResponseScopeAssignment
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
	case ResponseStateInitialBriefing, ResponseStateBriefRefinement:
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
	if result.TaskIDs == nil || result.TaskPayloads == nil || result.CheckNames == nil || result.FindingIDs == nil || result.Findings == nil || result.FindingDecisions == nil || result.FindingReasons == nil || result.KnownFacts == nil || result.Unknowns == nil || result.References == nil || result.Locations == nil || result.Bases == nil || result.ExpectedResults == nil || result.Options == nil || result.Attempts == nil {
		return responseTransport{}, fmt.Errorf("%w: array placeholders must be arrays", ErrMalformedAgentResponse)
	}
	return result, nil
}

func normalizedResponse(kind ResponseKind, input responseTransport, binding ResponseBinding) AgentResponse {
	result := AgentResponse{
		Kind: kind, Message: optionalString(input.Message), Brief: optionalString(input.Brief),
		TaskPayloads: optionalStrings(input.TaskPayloads), CheckNames: optionalStrings(input.CheckNames),
		FindingIDs: optionalStrings(input.FindingIDs), Findings: optionalStrings(input.Findings), FindingDecisions: optionalStrings(input.FindingDecisions), FindingReasons: optionalStrings(input.FindingReasons),
		Question: optionalString(input.Question), Context: optionalString(input.Context), Boundaries: optionalString(input.Boundaries),
		KnownFacts: optionalStrings(input.KnownFacts), Unknowns: optionalStrings(input.Unknowns), References: optionalStrings(input.References),
		Locations: optionalStrings(input.Locations), Bases: optionalStrings(input.Bases), ExpectedResults: optionalStrings(input.ExpectedResults), Options: optionalStrings(input.Options),
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

// validateResponseSemantics runs after required transport placeholders become
// nil. This keeps the provider schema compatible with flat-object transports
// while preventing an empty success result from advancing the controller.
func validateResponseSemantics(response AgentResponse) error {
	if err := rejectUnexpectedSemanticFields(response); err != nil {
		return err
	}
	requireText := func(name string, value *string) error {
		if value == nil || strings.TrimSpace(*value) == "" {
			return fmt.Errorf("%w: %s is required", ErrResponseSemantics, name)
		}
		return nil
	}
	requireValues := func(name string, values []string) error {
		if len(values) == 0 {
			return fmt.Errorf("%w: %s is required", ErrResponseSemantics, name)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%w: %s must not contain blank values", ErrResponseSemantics, name)
			}
		}
		return nil
	}
	requireTaskIDs := func() error {
		if len(response.TaskIDs) == 0 {
			return fmt.Errorf("%w: task_ids is required", ErrResponseSemantics)
		}
		for _, id := range response.TaskIDs {
			if strings.TrimSpace(string(id)) == "" {
				return fmt.Errorf("%w: task_ids must not contain blank values", ErrResponseSemantics)
			}
		}
		return nil
	}
	sameLength := func(fields ...[]string) error {
		for _, field := range fields[1:] {
			if len(field) != len(fields[0]) {
				return fmt.Errorf("%w: associated finding fields have different lengths", ErrResponseSemantics)
			}
		}
		return nil
	}

	switch response.Kind {
	case ResponseBriefReady:
		if err := requireTaskIDs(); err != nil {
			return err
		}
		return requireText("brief", response.Brief)
	case ResponseImplementationReady:
		return requireText("message", response.Message)
	case ResponseChecksRequested:
		return requireValues("check_names", response.CheckNames)
	case ResponseReviewPassed:
		if err := requireText("message", response.Message); err != nil {
			return err
		}
		return requireValues("references", response.References)
	case ResponseChangesRequested:
		for _, value := range []struct {
			name   string
			values []string
		}{{"finding_ids", response.FindingIDs}, {"findings", response.Findings}, {"finding_decisions", response.FindingDecisions}, {"finding_reasons", response.FindingReasons}, {"locations", response.Locations}, {"bases", response.Bases}, {"expected_results", response.ExpectedResults}} {
			if err := requireValues(value.name, value.values); err != nil {
				return err
			}
		}
		return sameLength(response.FindingIDs, response.Findings, response.FindingDecisions, response.FindingReasons, response.Locations, response.Bases, response.ExpectedResults)
	case ResponseReviewDisputed:
		if len(response.FindingIDs) != 1 {
			return fmt.Errorf("%w: review_disputed requires exactly one finding ID", ErrResponseSemantics)
		}
		if err := requireValues("finding_ids", response.FindingIDs); err != nil {
			return err
		}
		if err := requireText("message", response.Message); err != nil {
			return err
		}
		return requireValues("references", response.References)
	case ResponseExplorationRequested:
		for _, value := range []struct {
			name  string
			value *string
		}{{"question", response.Question}, {"context", response.Context}, {"boundaries", response.Boundaries}} {
			if err := requireText(value.name, value.value); err != nil {
				return err
			}
		}
		return requireValues("known_facts", response.KnownFacts)
	case ResponseExplorationResult:
		if err := requireText("message", response.Message); err != nil {
			return err
		}
		for _, value := range []struct {
			name   string
			values []string
		}{{"known_facts", response.KnownFacts}, {"references", response.References}, {"unknowns", response.Unknowns}} {
			if err := requireValues(value.name, value.values); err != nil {
				return err
			}
		}
		return nil
	case ResponseClarificationNeeded:
		for _, value := range []struct {
			name  string
			value *string
		}{{"question", response.Question}, {"context", response.Context}, {"boundaries", response.Boundaries}} {
			if err := requireText(value.name, value.value); err != nil {
				return err
			}
		}
		return requireValues("references", response.References)
	case ResponseExecutionBlocked:
		for _, value := range []struct {
			name  string
			value *string
		}{{"blocked_action", response.BlockedAction}, {"diagnostic", response.Diagnostic}, {"required_user_action", response.RequiredUserAction}} {
			if err := requireText(value.name, value.value); err != nil {
				return err
			}
		}
		return requireValues("attempts", response.Attempts)
	case ResponseTasksExtracted, ResponseTasksAdded:
		if err := requireTaskIDs(); err != nil {
			return err
		}
		if err := requireValues("task_payloads", response.TaskPayloads); err != nil {
			return err
		}
		if len(response.TaskIDs) != len(response.TaskPayloads) {
			return fmt.Errorf("%w: task_ids and task_payloads have different lengths", ErrResponseSemantics)
		}
		return nil
	case ResponseProgressReflected:
		return requireTaskIDs()
	case ResponseConfigurationProposed:
		if err := requireText("user_implementation", response.UserImplementation); err != nil {
			return err
		}
		if err := requireText("project_implementation", response.ProjectImplementation); err != nil {
			return err
		}
		return requireText("explanation", response.Explanation)
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrResponseSemantics, response.Kind)
	}
}

// rejectUnexpectedSemanticFields gives every action a closed domain shape.
// The transport schema must carry placeholders for all fields, but a
// non-placeholder field from another action is a malformed domain result,
// rather than optional metadata the controller might accidentally ignore.
func rejectUnexpectedSemanticFields(response AgentResponse) error {
	present := map[string]bool{
		"message":                response.Message != nil,
		"task_ids":               len(response.TaskIDs) != 0,
		"task_payloads":          len(response.TaskPayloads) != 0,
		"brief":                  response.Brief != nil,
		"check_names":            len(response.CheckNames) != 0,
		"finding_ids":            len(response.FindingIDs) != 0,
		"findings":               len(response.Findings) != 0,
		"finding_decisions":      len(response.FindingDecisions) != 0,
		"finding_reasons":        len(response.FindingReasons) != 0,
		"question":               response.Question != nil,
		"context":                response.Context != nil,
		"boundaries":             response.Boundaries != nil,
		"known_facts":            len(response.KnownFacts) != 0,
		"unknowns":               len(response.Unknowns) != 0,
		"references":             len(response.References) != 0,
		"locations":              len(response.Locations) != 0,
		"bases":                  len(response.Bases) != 0,
		"expected_results":       len(response.ExpectedResults) != 0,
		"options":                len(response.Options) != 0,
		"recommendation":         response.Recommendation != nil,
		"blocked_action":         response.BlockedAction != nil,
		"diagnostic":             response.Diagnostic != nil,
		"attempts":               len(response.Attempts) != 0,
		"required_user_action":   response.RequiredUserAction != nil,
		"user_implementation":    response.UserImplementation != nil,
		"project_implementation": response.ProjectImplementation != nil,
		"explanation":            response.Explanation != nil,
	}
	for name, isPresent := range present {
		if isPresent && !slices.Contains(allowedSemanticFields[response.Kind], name) {
			return fmt.Errorf("%w: field %s is not allowed for kind %q", ErrResponseSemantics, name, response.Kind)
		}
	}
	return nil
}

var allowedSemanticFields = map[ResponseKind][]string{
	ResponseBriefReady:            {"task_ids", "brief"},
	ResponseImplementationReady:   {"message"},
	ResponseChecksRequested:       {"check_names"},
	ResponseReviewPassed:          {"message", "references"},
	ResponseChangesRequested:      {"finding_ids", "findings", "finding_decisions", "finding_reasons", "locations", "bases", "expected_results"},
	ResponseReviewDisputed:        {"finding_ids", "message", "references"},
	ResponseExplorationRequested:  {"question", "context", "boundaries", "known_facts"},
	ResponseExplorationResult:     {"message", "known_facts", "unknowns", "references"},
	ResponseClarificationNeeded:   {"question", "context", "boundaries", "references", "options", "recommendation"},
	ResponseExecutionBlocked:      {"blocked_action", "diagnostic", "attempts", "required_user_action"},
	ResponseTasksExtracted:        {"task_ids", "task_payloads"},
	ResponseTasksAdded:            {"task_ids", "task_payloads"},
	ResponseProgressReflected:     {"task_ids"},
	ResponseConfigurationProposed: {"user_implementation", "project_implementation", "explanation"},
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
