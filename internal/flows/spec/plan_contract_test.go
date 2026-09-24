package specflow

import (
	"reflect"
	"testing"
)

func TestValidPlanReturnsDependencyGraphAndCompleteCoverage(t *testing.T) {
	req := mustStableID(t, "REQ-1")
	dec := mustStableID(t, "DEC-2")
	ac := mustStableID(t, "AC-3")
	markdown := `# Plan
## TASK-001 — foundation
Traces: REQ-001

### Test scenario — foundation
Traces: AC-003

## TASK-0000000002 — integration
Traces: DEC-02, AC-3
Depends-on: TASK-01

#### Test scenario — integration
Traces: REQ-0001

## Open questions
`
	result := ParseDocument(DocumentRequest{Kind: DocumentPlan, Mode: ValidateApproval, Markdown: markdown, ActiveIDs: []StableID{req, dec, ac}})
	if !result.Valid() {
		t.Fatalf("valid plan diagnostics: %#v", result.Diagnostics)
	}
	if len(result.Document.Elements) != 2 || len(result.Document.TestScenarios) != 2 {
		t.Fatalf("parsed plan = %#v", result.Document)
	}
	if got := result.Document.Elements[1].DependsOn; len(got) != 1 || got[0].String() != "TASK-1" {
		t.Fatalf("dependency graph not canonicalized: %#v", got)
	}
	if result.Document.TestScenarios[1].TaskID.String() != "TASK-2" {
		t.Fatalf("scenario not nested under task: %#v", result.Document.TestScenarios[1])
	}
}

func TestPlanTaskAndScenarioContracts(t *testing.T) {
	req := mustStableID(t, "REQ-1")
	dec := mustStableID(t, "DEC-1")
	ac := mustStableID(t, "AC-1")
	markdown := `# Plan
### Test scenario — orphan
Traces: DEC-001

## TASK-001 — only AC is insufficient
Traces: AC-001

## TASK-002 — no traces and no scenario

## REQ-009 — disallowed heading

## Open questions
`
	result := ParseDocument(DocumentRequest{Kind: DocumentPlan, Mode: ValidateDraft, Markdown: markdown, ActiveIDs: []StableID{req, dec, ac}})
	wantCodes := []DiagnosticCode{
		DiagnosticOrphanTestScenario,
		DiagnosticInvalidTraceFamily,
		DiagnosticMissingTraces,
		DiagnosticMissingTestScenario,
		DiagnosticMissingTraces,
		DiagnosticMissingTestScenario,
		DiagnosticDisallowedID,
		DiagnosticUncoveredElement,
		DiagnosticUncoveredElement,
		DiagnosticUncoveredElement,
	}
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("diagnostics = %v, want %v\n%#v", got, wantCodes, result.Diagnostics)
	}
}

func TestPlanDependencyFailuresAreDeterministic(t *testing.T) {
	req := mustStableID(t, "REQ-1")
	markdown := `# Plan
## TASK-001 — one
Traces: REQ-001
Depends-on: TASK-002
### Test scenario — one
Traces: REQ-001
## TASK-002 — two
Traces: REQ-001
Depends-on: TASK-003
### Test scenario — two
Traces: REQ-001
## TASK-003 — three
Traces: REQ-001
Depends-on: TASK-001
### Test scenario — three
Traces: REQ-001
## TASK-004 — self
Traces: REQ-001
Depends-on: TASK-004
### Test scenario — self
Traces: REQ-001
## TASK-005 — unknown and deleted
Traces: REQ-001
Depends-on: TASK-900, TASK-901
### Test scenario — refs
Traces: REQ-001
## Open questions
`
	result := ParseDocument(DocumentRequest{
		Kind: DocumentPlan, Mode: ValidateDraft, Markdown: markdown,
		ActiveIDs: []StableID{req}, IssuedIDs: []StableID{mustStableID(t, "TASK-901")},
	})
	want := []DiagnosticCode{
		DiagnosticDependencyCycle,
		DiagnosticDependencyCycle,
		DiagnosticDependencyCycle,
		DiagnosticSelfDependency,
		DiagnosticUnknownReference,
		DiagnosticDeletedReference,
	}
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %v, want %v\n%#v", got, want, result.Diagnostics)
	}
}

func TestPlanCoverageRequiresTasksForReqDecAndScenariosForAC(t *testing.T) {
	ids := []StableID{mustStableID(t, "REQ-1"), mustStableID(t, "DEC-1"), mustStableID(t, "AC-1")}
	markdown := `# Plan
## TASK-001 — partial
Traces: REQ-001
### Test scenario — partial
Traces: REQ-001
## Open questions
`
	result := ParseDocument(DocumentRequest{Kind: DocumentPlan, Mode: ValidateDraft, Markdown: markdown, ActiveIDs: ids})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{DiagnosticUncoveredElement, DiagnosticUncoveredElement}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
	if result.Diagnostics[0].Subject != "AC-1" || result.Diagnostics[1].Subject != "DEC-1" {
		t.Fatalf("coverage diagnostics not canonically ordered: %#v", result.Diagnostics)
	}
}
