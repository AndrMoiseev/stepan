package specflow

import (
	"fmt"
	"sort"
	"strings"
)

// DocumentKind identifies one machine-readable planning artifact. Reviews are
// distinct kinds because their allowed finding identity family depends on the
// stage being reviewed.
type DocumentKind string

const (
	DocumentIntent     DocumentKind = "intent"
	DocumentSpec       DocumentKind = "spec"
	DocumentPlan       DocumentKind = "plan"
	DocumentSpecReview DocumentKind = "spec-review"
	DocumentPlanReview DocumentKind = "plan-review"
)

func (k DocumentKind) Valid() bool {
	switch k {
	case DocumentIntent, DocumentSpec, DocumentPlan, DocumentSpecReview, DocumentPlanReview:
		return true
	default:
		return false
	}
}

type DocumentValidationMode string

const (
	ValidateDraft    DocumentValidationMode = "draft"
	ValidateApproval DocumentValidationMode = "approval"
)

func (m DocumentValidationMode) Valid() bool {
	return m == ValidateDraft || m == ValidateApproval
}

// DocumentRequest contains only validation facts. ActiveIDs are the active
// upstream elements available for plan/review references. IssuedIDs are all
// identities ever observed by the flow; RetainedIDs are identities owned by
// the immediately preceding draft/revision and therefore allowed to remain.
type DocumentRequest struct {
	Kind             DocumentKind
	Mode             DocumentValidationMode
	Markdown         string
	ActiveIDs        []StableID
	IssuedIDs        []StableID
	RetainedIDs      []StableID
	PreviousFindings []FindingSnapshot
}

type ObservedID struct {
	ID       StableID
	Spelling string
	Line     int
	Level    int
}

type DocumentElement struct {
	ID        StableID
	Spelling  string
	Title     string
	Line      int
	Level     int
	Traces    []StableID
	DependsOn []StableID
}

type ParsedTestScenario struct {
	Title  string
	Line   int
	Level  int
	TaskID StableID
	Traces []StableID
}

type ParsedFinding struct {
	Finding       Finding
	Spelling      string
	Line          int
	TracesPresent bool
}

type OpenQuestionsSection struct {
	Line       int
	Level      int
	HasContent bool
}

type ParsedDocument struct {
	Kind          DocumentKind
	Source        string
	Elements      []DocumentElement
	TestScenarios []ParsedTestScenario
	Findings      []ParsedFinding
	OpenQuestions []OpenQuestionsSection
}

type DiagnosticCode string

const (
	DiagnosticInvalidRequest          DiagnosticCode = "invalid-request"
	DiagnosticInvalidID               DiagnosticCode = "invalid-id"
	DiagnosticDuplicateID             DiagnosticCode = "duplicate-id"
	DiagnosticReusedID                DiagnosticCode = "reused-id"
	DiagnosticDisallowedID            DiagnosticCode = "disallowed-id"
	DiagnosticInvalidFieldName        DiagnosticCode = "invalid-field-name"
	DiagnosticDuplicateField          DiagnosticCode = "duplicate-field"
	DiagnosticInvalidReference        DiagnosticCode = "invalid-reference"
	DiagnosticUnknownReference        DiagnosticCode = "unknown-reference"
	DiagnosticDeletedReference        DiagnosticCode = "deleted-reference"
	DiagnosticMissingTraces           DiagnosticCode = "missing-traces"
	DiagnosticInvalidTraceFamily      DiagnosticCode = "invalid-trace-family"
	DiagnosticInvalidDependency       DiagnosticCode = "invalid-dependency"
	DiagnosticSelfDependency          DiagnosticCode = "self-dependency"
	DiagnosticDependencyCycle         DiagnosticCode = "dependency-cycle"
	DiagnosticMissingTestScenario     DiagnosticCode = "missing-test-scenario"
	DiagnosticOrphanTestScenario      DiagnosticCode = "orphan-test-scenario"
	DiagnosticUncoveredElement        DiagnosticCode = "uncovered-element"
	DiagnosticMissingOpenQuestions    DiagnosticCode = "missing-open-questions"
	DiagnosticDuplicateOpenQuestions  DiagnosticCode = "duplicate-open-questions"
	DiagnosticOpenQuestionsUnresolved DiagnosticCode = "open-questions-unresolved"
	DiagnosticMissingFindingField     DiagnosticCode = "missing-finding-field"
	DiagnosticInvalidFindingField     DiagnosticCode = "invalid-finding-field"
	DiagnosticInvalidFindingState     DiagnosticCode = "invalid-finding-state"
	DiagnosticImmutableFindingField   DiagnosticCode = "immutable-finding-field"
	DiagnosticMissingKnownFinding     DiagnosticCode = "missing-known-finding"
	DiagnosticInvalidSupersession     DiagnosticCode = "invalid-supersession"
)

type DocumentDiagnostic struct {
	Code    DiagnosticCode
	Line    int
	Subject string
	Message string
	order   int
}

type DocumentResult struct {
	Document    ParsedDocument
	ObservedIDs []ObservedID
	Diagnostics []DocumentDiagnostic
}

func (r DocumentResult) Valid() bool { return len(r.Diagnostics) == 0 }

type documentValidation struct {
	request       DocumentRequest
	parsed        parsedMarkdown
	result        DocumentResult
	active        map[StableID]struct{}
	issued        map[StableID]struct{}
	retained      map[StableID]struct{}
	nextDiagOrder int
}

// ParseDocument is the complete interface of the document-contract module.
// It never short-circuits on document errors: callers can reserve every
// observed identity and present the entire deterministic diagnostic set to an
// author in one repair turn.
func ParseDocument(request DocumentRequest) DocumentResult {
	v := documentValidation{
		request:  request,
		parsed:   parseMarkdown(request.Markdown),
		active:   stableIDSet(request.ActiveIDs),
		issued:   stableIDSet(request.IssuedIDs),
		retained: stableIDSet(request.RetainedIDs),
	}
	for _, previous := range request.PreviousFindings {
		if previous.ID.Valid() {
			v.retained[previous.ID] = struct{}{}
		}
	}
	v.result.Document = ParsedDocument{Kind: request.Kind, Source: request.Markdown}

	if !request.Kind.Valid() {
		v.add(DiagnosticInvalidRequest, 0, "document", fmt.Sprintf("unknown document kind %q", request.Kind))
	}
	if !request.Mode.Valid() {
		v.add(DiagnosticInvalidRequest, 0, "document", fmt.Sprintf("unknown validation mode %q", request.Mode))
	}

	v.collectHeadings()
	v.validateOpenQuestions()
	switch request.Kind {
	case DocumentIntent:
		v.validateIntent()
	case DocumentSpec:
		v.validateSpec()
	case DocumentPlan:
		v.validatePlan()
	case DocumentSpecReview, DocumentPlanReview:
		v.validateReview()
	}

	sort.SliceStable(v.result.Diagnostics, func(i, j int) bool {
		left, right := v.result.Diagnostics[i], v.result.Diagnostics[j]
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.order < right.order
	})
	for i := range v.result.Diagnostics {
		v.result.Diagnostics[i].order = 0
	}
	return v.result
}

func (v *documentValidation) add(code DiagnosticCode, line int, subject, message string) {
	v.result.Diagnostics = append(v.result.Diagnostics, DocumentDiagnostic{
		Code: code, Line: line, Subject: subject, Message: message, order: v.nextDiagOrder,
	})
	v.nextDiagOrder++
}

func stableIDSet(values []StableID) map[StableID]struct{} {
	result := make(map[StableID]struct{}, len(values))
	for _, value := range values {
		if value.Valid() {
			result[value] = struct{}{}
		}
	}
	return result
}

func (v *documentValidation) collectHeadings() {
	seen := make(map[StableID]ObservedID)
	for i := range v.parsed.headings {
		heading := &v.parsed.headings[i]
		if heading.idCandidate && !heading.id.Valid() {
			v.add(DiagnosticInvalidID, heading.line, heading.idSpelling, "heading ID must use a known uppercase family and a positive numeric suffix")
			continue
		}
		if !heading.id.Valid() {
			continue
		}
		observed := ObservedID{ID: heading.id, Spelling: heading.idSpelling, Line: heading.line, Level: heading.level}
		v.result.ObservedIDs = append(v.result.ObservedIDs, observed)
		if previous, duplicate := seen[heading.id]; duplicate {
			v.add(DiagnosticDuplicateID, heading.line, heading.idSpelling,
				fmt.Sprintf("duplicates canonical identity %s first declared as %s on line %d", heading.id, previous.Spelling, previous.Line))
		} else {
			seen[heading.id] = observed
		}
		if _, issued := v.issued[heading.id]; issued {
			if _, retained := v.retained[heading.id]; !retained {
				v.add(DiagnosticReusedID, heading.line, heading.idSpelling, fmt.Sprintf("identity %s was issued by an earlier draft and is not retained by this revision", heading.id))
			}
		}
	}

	for _, heading := range v.parsed.headings {
		if heading.openQuestions {
			v.result.Document.OpenQuestions = append(v.result.Document.OpenQuestions, OpenQuestionsSection{
				Line: heading.line, Level: heading.level, HasContent: v.parsed.sectionHasContent(heading.index),
			})
		}
	}
}

func (v *documentValidation) validateOpenQuestions() {
	if v.request.Kind == DocumentSpecReview || v.request.Kind == DocumentPlanReview || !v.request.Kind.Valid() {
		return
	}
	sections := v.result.Document.OpenQuestions
	if len(sections) == 0 {
		v.add(DiagnosticMissingOpenQuestions, v.parsed.eofLine(), "Open questions", "document must contain an exact Open questions heading")
		return
	}
	for _, section := range sections[1:] {
		v.add(DiagnosticDuplicateOpenQuestions, section.Line, "Open questions", "document contains more than one Open questions section")
	}
	if v.request.Mode == ValidateApproval {
		for _, section := range sections {
			if section.HasContent {
				v.add(DiagnosticOpenQuestionsUnresolved, section.Line, "Open questions", "approval requires an empty Open questions section")
			}
		}
	}
}

func (v *documentValidation) validateIntent() {
	for _, heading := range v.parsed.headings {
		if heading.id.Valid() {
			v.add(DiagnosticDisallowedID, heading.line, heading.idSpelling, "intent must not declare machine element IDs")
		}
	}
}

func cloneStableIDs(values []StableID) []StableID {
	return append([]StableID(nil), values...)
}

func normalizeText(value string) string { return strings.TrimSpace(value) }
