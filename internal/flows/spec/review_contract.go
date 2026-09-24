package specflow

import (
	"fmt"
	"strings"
)

type rawFinding struct {
	heading        rawHeading
	severity       FindingSeverity
	status         FindingStatus
	problem        string
	location       string
	traces         []StableID
	tracesPresent  bool
	recommendation string
	resolution     string
	supersededBy   StableID
	decision       FindingDecision
	decidedBy      DecisionMaker
	rationale      string
	invalid        bool
}

func (v *documentValidation) validateReview() {
	expectedFamily := IDSpecFinding
	if v.request.Kind == DocumentPlanReview {
		expectedFamily = IDPlanFinding
	}

	findings := make([]rawFinding, 0)
	byID := make(map[StableID]int)
	for _, heading := range v.parsed.headings {
		if !heading.id.Valid() {
			continue
		}
		v.validateFieldNamesAndDuplicates(heading)
		if heading.id.Family() != expectedFamily {
			v.add(DiagnosticDisallowedID, heading.line, heading.idSpelling, fmt.Sprintf("%s review may declare only %s finding headings", v.request.Kind, expectedFamily))
			continue
		}
		raw := v.parseFinding(heading)
		if _, duplicate := byID[heading.id]; !duplicate {
			byID[heading.id] = len(findings)
		}
		findings = append(findings, raw)
	}

	for i := range findings {
		v.validateFindingSupersession(&findings[i], byID, expectedFamily)
		v.validateFindingImmutability(&findings[i])
		if findings[i].invalid {
			continue
		}
		snapshot := findings[i].snapshot()
		finding, err := RestoreFinding(snapshot)
		if err != nil {
			v.add(DiagnosticInvalidFindingState, findings[i].heading.line, findings[i].heading.idSpelling, err.Error())
			continue
		}
		v.result.Document.Findings = append(v.result.Document.Findings, ParsedFinding{
			Finding: finding, Spelling: findings[i].heading.idSpelling,
			Line: findings[i].heading.line, TracesPresent: findings[i].tracesPresent,
		})
	}

	for _, previous := range v.request.PreviousFindings {
		if _, present := byID[previous.ID]; !present {
			v.add(DiagnosticMissingKnownFinding, v.parsed.eofLine(), previous.ID.String(), "new review report must list every previously known finding")
		}
	}
}

func (v *documentValidation) parseFinding(heading rawHeading) rawFinding {
	result := rawFinding{heading: heading}
	result.severity = FindingSeverity(v.requiredFindingField(heading, "Severity", &result.invalid))
	if result.severity != "" && !result.severity.Valid() {
		v.add(DiagnosticInvalidFindingField, fieldLine(heading, "Severity"), heading.idSpelling, "Severity must be blocker, major, or minor")
		result.invalid = true
	}
	result.status = FindingStatus(v.requiredFindingField(heading, "Status", &result.invalid))
	if result.status != "" && !result.status.Valid() {
		v.add(DiagnosticInvalidFindingField, fieldLine(heading, "Status"), heading.idSpelling, "Status must be open, resolved, dismissed, or superseded")
		result.invalid = true
	}
	result.problem = v.requiredFindingField(heading, "Problem", &result.invalid)
	result.location = v.requiredFindingField(heading, "Location", &result.invalid)
	result.recommendation = v.requiredFindingField(heading, "Recommendation", &result.invalid)

	traces, present := parseReferenceField(v, heading, "Traces")
	result.traces, result.tracesPresent = traces, present
	if present && len(traces) == 0 {
		v.add(DiagnosticMissingTraces, fieldLine(heading, "Traces"), heading.idSpelling, "present Traces field must identify at least one document element; omit it for a whole-document finding")
		result.invalid = true
	}
	for _, trace := range traces {
		validFamily := trace.Family() == IDTask
		if v.request.Kind == DocumentSpecReview {
			validFamily = trace.Family() == IDRequirement || trace.Family() == IDDecision || trace.Family() == IDAcceptance
		}
		if !validFamily {
			v.add(DiagnosticInvalidTraceFamily, fieldLine(heading, "Traces"), heading.idSpelling, "finding Traces must identify an element of the reviewed document")
			result.invalid = true
			continue
		}
		if !v.referenceExists(trace, v.active, fieldLine(heading, "Traces"), heading.idSpelling) {
			result.invalid = true
		}
	}

	result.decision = FindingDecision(v.requiredFindingFieldAllowEmpty(heading, "Decision", &result.invalid))
	if result.decision != "" && !result.decision.Valid() {
		v.add(DiagnosticInvalidFindingField, fieldLine(heading, "Decision"), heading.idSpelling, "Decision must be pending, fix, or dismiss")
		result.invalid = true
	}
	result.decidedBy = DecisionMaker(v.requiredFindingFieldAllowEmpty(heading, "Decided-by", &result.invalid))
	if result.decidedBy != "" && !result.decidedBy.Valid() {
		v.add(DiagnosticInvalidFindingField, fieldLine(heading, "Decided-by"), heading.idSpelling, "Decided-by must be none, user, or reviewer")
		result.invalid = true
	}
	result.rationale = v.requiredFindingFieldAllowEmpty(heading, "Rationale", &result.invalid)
	result.resolution = optionalFindingField(heading, "Resolution")

	if field, exists := firstField(heading, "Superseded-by"); exists {
		id, err := ParseStableID(strings.Trim(field.value, "`"))
		if err != nil {
			v.add(DiagnosticInvalidFindingField, field.line, heading.idSpelling, "Superseded-by must contain one valid finding ID")
			result.invalid = true
		} else {
			result.supersededBy = id
		}
	}

	v.validateFindingLifecycle(&result)
	return result
}

func (v *documentValidation) requiredFindingField(heading rawHeading, name string, invalid *bool) string {
	field, exists := firstField(heading, name)
	if !exists {
		v.add(DiagnosticMissingFindingField, heading.line, heading.idSpelling, "finding requires "+name+" field")
		*invalid = true
		return ""
	}
	if strings.TrimSpace(field.value) == "" {
		v.add(DiagnosticInvalidFindingField, field.line, heading.idSpelling, name+" must not be empty")
		*invalid = true
	}
	return strings.TrimSpace(field.value)
}

func (v *documentValidation) requiredFindingFieldAllowEmpty(heading rawHeading, name string, invalid *bool) string {
	field, exists := firstField(heading, name)
	if !exists {
		v.add(DiagnosticMissingFindingField, heading.line, heading.idSpelling, "finding requires "+name+" field")
		*invalid = true
		return ""
	}
	return strings.TrimSpace(field.value)
}

func optionalFindingField(heading rawHeading, name string) string {
	field, _ := firstField(heading, name)
	return strings.TrimSpace(field.value)
}

func fieldLine(heading rawHeading, name string) int {
	if field, exists := firstField(heading, name); exists {
		return field.line
	}
	return heading.line
}

func (v *documentValidation) validateFindingLifecycle(finding *rawFinding) {
	line, subject := finding.heading.line, finding.heading.idSpelling
	isContract := finding.decision == DecisionFix && finding.decidedBy == DecidedByReviewer

	if isContract {
		if finding.rationale == "" {
			v.add(DiagnosticInvalidFindingState, line, subject, "contract violation requires reviewer rationale")
			finding.invalid = true
		}
		if finding.status == FindingDismissed {
			v.add(DiagnosticInvalidFindingState, line, subject, "contract violation cannot be dismissed")
			finding.invalid = true
		}
	} else {
		if finding.decidedBy == DecidedByReviewer {
			v.add(DiagnosticInvalidFindingState, line, subject, "reviewer cannot decide a material finding")
			finding.invalid = true
		}
		if finding.decision == DecisionPending {
			if finding.decidedBy != DecidedByNone || finding.rationale != "" {
				v.add(DiagnosticInvalidFindingState, line, subject, "pending material finding requires Decided-by: none and an empty Rationale")
				finding.invalid = true
			}
		} else if finding.decision.Valid() {
			if finding.decidedBy != DecidedByUser || finding.rationale == "" {
				v.add(DiagnosticInvalidFindingState, line, subject, "material decision requires Decided-by: user and a non-empty Rationale")
				finding.invalid = true
			}
		}
	}

	if finding.status == FindingResolved {
		if finding.resolution == "" {
			v.add(DiagnosticInvalidFindingState, line, subject, "resolved finding requires Resolution")
			finding.invalid = true
		}
		if finding.decision != DecisionFix {
			v.add(DiagnosticInvalidFindingState, line, subject, "resolved finding requires Decision: fix")
			finding.invalid = true
		}
	}
	if finding.status == FindingDismissed {
		if isContract || finding.decision != DecisionDismiss || finding.decidedBy != DecidedByUser || finding.rationale == "" {
			v.add(DiagnosticInvalidFindingState, line, subject, "dismissed finding requires a material user dismiss decision and rationale")
			finding.invalid = true
		}
	} else if finding.decision == DecisionDismiss {
		v.add(DiagnosticInvalidFindingState, line, subject, "Decision: dismiss requires Status: dismissed")
		finding.invalid = true
	}
	if finding.status == FindingSuperseded {
		if !finding.supersededBy.Valid() {
			v.add(DiagnosticInvalidSupersession, line, subject, "superseded finding requires Superseded-by")
			finding.invalid = true
		}
	} else if finding.supersededBy.Valid() {
		v.add(DiagnosticInvalidSupersession, line, subject, "Superseded-by is allowed only for Status: superseded")
		finding.invalid = true
	}
}

func (v *documentValidation) validateFindingSupersession(finding *rawFinding, byID map[StableID]int, family IDFamily) {
	if !finding.supersededBy.Valid() {
		return
	}
	line, subject := finding.heading.line, finding.heading.idSpelling
	if finding.supersededBy == finding.heading.id || finding.supersededBy.Family() != family {
		v.add(DiagnosticInvalidSupersession, line, subject, "Superseded-by must identify a different finding in the same review")
		finding.invalid = true
		return
	}
	if _, present := byID[finding.supersededBy]; !present {
		v.add(DiagnosticInvalidSupersession, line, subject, "Superseded-by target is not present in the complete review report")
		finding.invalid = true
	}
}

func (v *documentValidation) validateFindingImmutability(current *rawFinding) {
	var previous *FindingSnapshot
	for i := range v.request.PreviousFindings {
		if v.request.PreviousFindings[i].ID == current.heading.id {
			previous = &v.request.PreviousFindings[i]
			break
		}
	}
	if previous == nil {
		return
	}
	compare := func(name, oldValue, newValue string) {
		if oldValue != newValue {
			v.add(DiagnosticImmutableFindingField, current.heading.line, current.heading.idSpelling, name+" is immutable across review revisions")
			current.invalid = true
		}
	}
	compare("Severity", string(previous.Severity), string(current.severity))
	compare("Problem", normalizeText(previous.Problem), normalizeText(current.problem))
	compare("Location", normalizeText(previous.Location), normalizeText(current.location))
	currentKind := FindingMaterial
	if current.decision == DecisionFix && current.decidedBy == DecidedByReviewer {
		currentKind = FindingContractViolation
	}
	if previous.Kind != currentKind {
		v.add(DiagnosticImmutableFindingField, current.heading.line, current.heading.idSpelling, "finding kind implied by decision provenance is immutable across review revisions")
		current.invalid = true
	}
	if !sameStableIDSet(previous.Traces, current.traces) {
		v.add(DiagnosticImmutableFindingField, current.heading.line, current.heading.idSpelling, "Traces is immutable across review revisions")
		current.invalid = true
	}
}

func sameStableIDSet(left, right []StableID) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := stableIDSet(left)
	rightSet := stableIDSet(right)
	if len(leftSet) != len(rightSet) {
		return false
	}
	for id := range leftSet {
		if _, exists := rightSet[id]; !exists {
			return false
		}
	}
	return true
}

func (f rawFinding) snapshot() FindingSnapshot {
	kind := FindingMaterial
	if f.decision == DecisionFix && f.decidedBy == DecidedByReviewer {
		kind = FindingContractViolation
	}
	return FindingSnapshot{
		ID: f.heading.id, Kind: kind, Severity: f.severity, Problem: f.problem,
		Location: f.location, Traces: cloneStableIDs(f.traces), Status: f.status,
		Recommendation: f.recommendation, Resolution: f.resolution,
		SupersededBy: f.supersededBy,
		Decision:     FindingDecisionRecord{Decision: f.decision, DecidedBy: f.decidedBy, Rationale: f.rationale},
	}
}
