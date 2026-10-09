package specflow

import (
	"reflect"
	"testing"
)

func TestValidSpecParsesElementsAndCanonicalReferences(t *testing.T) {
	markdown := `# Specification

#### REQ-0000001 — observable behavior

## DEC-42 — architecture

###### AC-007 — accepted

Traces: REQ-01

## Open questions
`
	result := ParseDocument(DocumentRequest{Kind: DocumentSpec, Mode: ValidateApproval, Markdown: markdown})
	if !result.Valid() {
		t.Fatalf("valid spec diagnostics: %#v", result.Diagnostics)
	}
	if len(result.Document.Elements) != 3 {
		t.Fatalf("elements = %#v", result.Document.Elements)
	}
	acceptance := result.Document.Elements[2]
	if acceptance.ID.String() != "AC-7" || len(acceptance.Traces) != 1 || acceptance.Traces[0].String() != "REQ-1" {
		t.Fatalf("canonical references not parsed: %#v", acceptance)
	}
}

func TestSpecReferenceDiagnosticsAreComplete(t *testing.T) {
	markdown := `# Specification
## REQ-001 — active
## AC-001 — missing traces
## AC-002 — deleted and wrong family
traces: REQ-009, DEC-001
Traces: REQ-nope
## TASK-001 — disallowed
## Open questions
`
	result := ParseDocument(DocumentRequest{
		Kind: DocumentSpec, Mode: ValidateDraft, Markdown: markdown,
		IssuedIDs: []StableID{mustStableID(t, "REQ-009")},
	})
	want := []DiagnosticCode{
		DiagnosticMissingTraces,
		DiagnosticInvalidFieldName,
		DiagnosticDeletedReference,
		DiagnosticInvalidTraceFamily,
		DiagnosticDuplicateField,
		DiagnosticInvalidReference,
		DiagnosticDisallowedID,
	}
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %v, want %v\n%#v", got, want, result.Diagnostics)
	}
}

func TestSpecUnknownRequirementReference(t *testing.T) {
	result := ParseDocument(DocumentRequest{
		Kind: DocumentSpec, Mode: ValidateDraft,
		Markdown: "# REQ-001 — active\n# AC-001 — criterion\nTraces: REQ-777\n# Open questions\n",
	})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{DiagnosticUnknownReference}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
}
