package specflow

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestValidReviewFindingStates(t *testing.T) {
	active := []StableID{mustStableID(t, "REQ-1"), mustStableID(t, "REQ-2")}
	markdown := `# Review
## SPEC-F-001 — contract
Severity: blocker
Status: resolved
Problem: AC misses Traces
Location: AC-001
Traces: REQ-001
Recommendation: add Traces
Resolution: added
Decision: fix
Decided-by: reviewer
Rationale: deterministic contract violation

## SPEC-F-002 — dismissed material
Severity: minor
Status: dismissed
Problem: optional detail
Location: REQ-002
Traces: REQ-002
Recommendation: expand
Decision: dismiss
Decided-by: user
Rationale: deliberately excluded

## SPEC-F-003 — whole document pending
Severity: major
Status: open
Problem: unclear structure
Location: whole document
Recommendation: reorganize
Decision: pending
Decided-by: none
Rationale:
`
	result := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: markdown, ActiveIDs: active})
	if !result.Valid() {
		t.Fatalf("valid review diagnostics: %#v", result.Diagnostics)
	}
	if len(result.Document.Findings) != 3 {
		t.Fatalf("findings = %#v", result.Document.Findings)
	}
	if result.Document.Findings[0].Finding.Snapshot().Kind != FindingContractViolation {
		t.Fatal("reviewer fix decision was not represented as contract violation")
	}
	if result.Document.Findings[2].TracesPresent {
		t.Fatal("whole-document finding unexpectedly has Traces")
	}
}

func TestReviewFindingStructuralDiagnosticsAreComplete(t *testing.T) {
	markdown := `# Review
## PLAN-F-001 — broken
severity: critical
Status: dismissed
Problem:
Location: TASK-001
Traces:
Recommendation:
Decision: dismiss
Decided-by: reviewer
`
	result := ParseDocument(DocumentRequest{Kind: DocumentPlanReview, Mode: ValidateDraft, Markdown: markdown})
	want := []DiagnosticCode{
		DiagnosticMissingFindingField,
		DiagnosticInvalidFindingState,
		DiagnosticInvalidFindingState,
		DiagnosticInvalidFindingState,
		DiagnosticInvalidFieldName,
		DiagnosticInvalidFindingField,
		DiagnosticInvalidFindingField,
		DiagnosticMissingTraces,
		DiagnosticInvalidFindingField,
	}
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %v, want %v\n%#v", got, want, result.Diagnostics)
	}
}

func TestReviewSupersessionRequiresCompleteSameFamilyReport(t *testing.T) {
	markdown := `# Review
## SPEC-F-001 — old
Severity: major
Status: superseded
Problem: old problem
Location: REQ-001
Traces: REQ-001
Recommendation: old recommendation
Superseded-by: SPEC-F-002
Decision: pending
Decided-by: none
Rationale:

## SPEC-F-002 — new
Severity: major
Status: open
Problem: materially different problem
Location: REQ-001
Traces: REQ-001
Recommendation: new recommendation
Decision: pending
Decided-by: none
Rationale:
`
	active := []StableID{mustStableID(t, "REQ-1")}
	result := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: markdown, ActiveIDs: active})
	if !result.Valid() {
		t.Fatalf("valid supersession diagnostics: %#v", result.Diagnostics)
	}

	bad := ParseDocument(DocumentRequest{
		Kind: DocumentSpecReview, Mode: ValidateDraft, ActiveIDs: active,
		Markdown: replaceLine(markdown, "Superseded-by: SPEC-F-002", "Superseded-by: PLAN-F-002"),
	})
	if !hasDiagnostic(bad, DiagnosticInvalidSupersession) {
		t.Fatalf("cross-family supersession accepted: %#v", bad.Diagnostics)
	}
}

func TestReviewFindingIdentityIsImmutableAcrossRevisions(t *testing.T) {
	active := []StableID{mustStableID(t, "REQ-1"), mustStableID(t, "REQ-2")}
	initial := `# Review
## SPEC-F-001 — material
Severity: major
Status: open
Problem: original problem
Location: REQ-001
Traces: REQ-001
Recommendation: fix it
Decision: pending
Decided-by: none
Rationale:
`
	parsed := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: initial, ActiveIDs: active})
	if !parsed.Valid() || len(parsed.Document.Findings) != 1 {
		t.Fatalf("initial review invalid: %#v", parsed.Diagnostics)
	}
	previous := []FindingSnapshot{parsed.Document.Findings[0].Finding.Snapshot()}

	resolved := `# Review
## SPEC-F-001 — material
Severity: major
Status: resolved
Problem: original problem
Location: REQ-001
Traces: REQ-001
Recommendation: improved recommendation
Resolution: implemented
Decision: fix
Decided-by: user
Rationale: required behavior
`
	allowed := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: resolved, ActiveIDs: active, PreviousFindings: previous})
	if !allowed.Valid() {
		t.Fatalf("allowed lifecycle update rejected: %#v", allowed.Diagnostics)
	}

	changed := `# Review
## SPEC-F-001 — material
Severity: blocker
Status: open
Problem: changed problem
Location: REQ-002
Traces: REQ-002
Recommendation: fix it
Decision: pending
Decided-by: none
Rationale:
`
	invalid := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: changed, ActiveIDs: active, PreviousFindings: previous})
	if got := diagnosticCodes(invalid.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{
		DiagnosticImmutableFindingField,
		DiagnosticImmutableFindingField,
		DiagnosticImmutableFindingField,
		DiagnosticImmutableFindingField,
	}) {
		t.Fatalf("immutability diagnostics = %v (%#v)", got, invalid.Diagnostics)
	}
}

func TestReviewFindingCannotChangeMaterialKindThroughProvenance(t *testing.T) {
	active := []StableID{mustStableID(t, "REQ-1")}
	initial := `# Review
## SPEC-F-001 — material
Severity: major
Status: open
Problem: original
Location: REQ-001
Traces: REQ-001
Recommendation: fix
Decision: pending
Decided-by: none
Rationale:
`
	parsed := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: initial, ActiveIDs: active})
	previous := []FindingSnapshot{parsed.Document.Findings[0].Finding.Snapshot()}
	changed := replaceLine(replaceLine(initial, "Decision: pending", "Decision: fix"), "Decided-by: none", "Decided-by: reviewer")
	changed = replaceLine(changed, "Rationale:", "Rationale: now called a contract issue")
	result := ParseDocument(DocumentRequest{
		Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: changed, ActiveIDs: active,
		IssuedIDs: []StableID{mustStableID(t, "SPEC-F-1")}, PreviousFindings: previous,
	})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{DiagnosticImmutableFindingField}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
	if hasDiagnostic(result, DiagnosticReusedID) {
		t.Fatal("previous finding identity was not retained automatically")
	}
}

func TestReviewStatusSpecificMetadata(t *testing.T) {
	base := `# Review
## SPEC-F-001 — finding
Severity: major
Status: %s
Problem: problem
Location: whole document
Recommendation: recommendation
%sDecision: %s
Decided-by: %s
Rationale: %s
`
	tests := []struct {
		name      string
		status    string
		extra     string
		decision  string
		decidedBy string
		rationale string
		wantCode  DiagnosticCode
	}{
		{name: "resolved needs resolution", status: "resolved", decision: "fix", decidedBy: "user", rationale: "yes", wantCode: DiagnosticInvalidFindingState},
		{name: "dismissed needs rationale", status: "dismissed", decision: "dismiss", decidedBy: "user", wantCode: DiagnosticInvalidFindingState},
		{name: "superseded needs successor", status: "superseded", decision: "pending", decidedBy: "none", wantCode: DiagnosticInvalidSupersession},
		{name: "successor only when superseded", status: "open", extra: "Superseded-by: SPEC-F-002\n", decision: "pending", decidedBy: "none", wantCode: DiagnosticInvalidSupersession},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			markdown := fmt.Sprintf(base, test.status, test.extra, test.decision, test.decidedBy, test.rationale)
			result := ParseDocument(DocumentRequest{Kind: DocumentSpecReview, Mode: ValidateDraft, Markdown: markdown})
			if !hasDiagnostic(result, test.wantCode) {
				t.Fatalf("missing %s: %#v", test.wantCode, result.Diagnostics)
			}
		})
	}
}

func TestReviewMustRetainEveryKnownFinding(t *testing.T) {
	previousID := mustStableID(t, "PLAN-F-9")
	previous, err := NewFinding(FindingInput{
		ID: previousID, Kind: FindingMaterial, Severity: SeverityMinor,
		Problem: "known", Location: "whole document", Recommendation: "consider",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := ParseDocument(DocumentRequest{
		Kind: DocumentPlanReview, Mode: ValidateDraft, Markdown: "# Review\n",
		PreviousFindings: []FindingSnapshot{previous.Snapshot()},
	})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{DiagnosticMissingKnownFinding}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
}

func TestReviewRejectsWrongFindingFamilyAndUnknownTrace(t *testing.T) {
	markdown := `# Review
## SPEC-F-001 — wrong family
Severity: major
Status: open
Problem: issue
Location: TASK-001
Traces: TASK-001
Recommendation: fix
Decision: pending
Decided-by: none
Rationale:
## PLAN-F-001 — unknown trace
Severity: major
Status: open
Problem: issue
Location: TASK-001
Traces: TASK-001
Recommendation: fix
Decision: pending
Decided-by: none
Rationale:
`
	result := ParseDocument(DocumentRequest{Kind: DocumentPlanReview, Mode: ValidateDraft, Markdown: markdown})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{DiagnosticDisallowedID, DiagnosticUnknownReference}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
}

func replaceLine(value, old, replacement string) string {
	return strings.Replace(value, old, replacement, 1)
}
