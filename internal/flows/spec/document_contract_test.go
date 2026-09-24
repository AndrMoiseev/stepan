package specflow

import (
	"reflect"
	"strings"
	"testing"
)

func TestDocumentParserRecognizesAllHeadingFamiliesAndCanonicalAliases(t *testing.T) {
	huge := "1234567890123456789012345678901234567890"
	markdown := strings.Join([]string{
		"# REQ-001 — requirement",
		"## DEC-02 — decision",
		"### AC-3 — acceptance",
		"#### TASK-0004 — task",
		"##### SPEC-F-05 — spec finding",
		"###### PLAN-F-" + huge + " — plan finding",
		"",
		"```markdown",
		"# REQ-999 — ignored fenced example",
		"```",
		"",
		"# Open questions",
	}, "\n")

	result := ParseDocument(DocumentRequest{Kind: DocumentIntent, Mode: ValidateDraft, Markdown: markdown})
	got := make([]string, 0, len(result.ObservedIDs))
	for _, observed := range result.ObservedIDs {
		got = append(got, observed.ID.String())
	}
	want := []string{"REQ-1", "DEC-2", "AC-3", "TASK-4", "SPEC-F-5", "PLAN-F-" + huge}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("observed IDs = %v, want %v", got, want)
	}
	if result.ObservedIDs[0].Spelling != "REQ-001" || result.ObservedIDs[5].Level != 6 {
		t.Fatalf("source identity metadata was not preserved: %#v", result.ObservedIDs)
	}
	if containsSubject(result.Diagnostics, "REQ-999") {
		t.Fatal("fenced heading was parsed as a document identity")
	}
}

func TestDocumentParserReportsAllIDDiagnosticsAndStillReturnsObservedIDs(t *testing.T) {
	markdown := "# REQ-000 — zero\n## REQ-nope — malformed\n### REQ-001 — first\n#### REQ-1 — alias duplicate\n# Open questions\n"
	issued := []StableID{mustStableID(t, "REQ-001")}
	result := ParseDocument(DocumentRequest{Kind: DocumentSpec, Mode: ValidateDraft, Markdown: markdown, IssuedIDs: issued})

	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{
		DiagnosticInvalidID,
		DiagnosticInvalidID,
		DiagnosticReusedID,
		DiagnosticDuplicateID,
		DiagnosticReusedID,
	}) {
		t.Fatalf("diagnostic codes = %v", got)
	}
	if len(result.ObservedIDs) != 2 || result.ObservedIDs[0].ID != result.ObservedIDs[1].ID {
		t.Fatalf("valid aliases were not both returned: %#v", result.ObservedIDs)
	}
	for i := 1; i < len(result.Diagnostics); i++ {
		if result.Diagnostics[i-1].Line > result.Diagnostics[i].Line {
			t.Fatalf("diagnostics not ordered by source: %#v", result.Diagnostics)
		}
	}
}

func TestRetainedIdentityIsNotReportedAsReused(t *testing.T) {
	id := mustStableID(t, "REQ-7")
	result := ParseDocument(DocumentRequest{
		Kind: DocumentSpec, Mode: ValidateDraft,
		Markdown:  "# REQ-007 — retained\n# Open questions\n",
		IssuedIDs: []StableID{id}, RetainedIDs: []StableID{id},
	})
	if hasDiagnostic(result, DiagnosticReusedID) {
		t.Fatalf("retained identity reported as reused: %#v", result.Diagnostics)
	}
}

func TestInvalidRequestDiagnosticsPrecedeDocumentDiagnostics(t *testing.T) {
	result := ParseDocument(DocumentRequest{Kind: "unknown", Mode: "later", Markdown: "# REQ-000 — bad\n"})
	if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, []DiagnosticCode{
		DiagnosticInvalidRequest, DiagnosticInvalidRequest, DiagnosticInvalidID,
	}) {
		t.Fatalf("diagnostics = %v (%#v)", got, result.Diagnostics)
	}
}

func TestMachineFieldsBelongToNearestScenarioOrStableHeading(t *testing.T) {
	result := ParseDocument(DocumentRequest{
		Kind: DocumentPlan, Mode: ValidateDraft,
		ActiveIDs: []StableID{mustStableID(t, "REQ-1"), mustStableID(t, "AC-1")},
		Markdown: `# Plan
## TASK-001 — task
### Scope
Traces: REQ-001
### Test scenario — nested
#### Setup
Traces: AC-001
## Open questions
`,
	})
	if !result.Valid() {
		t.Fatalf("nested fields were assigned incorrectly: %#v", result.Diagnostics)
	}
	if got := result.Document.Elements[0].Traces[0].String(); got != "REQ-1" {
		t.Fatalf("task trace = %s", got)
	}
	if got := result.Document.TestScenarios[0].Traces[0].String(); got != "AC-1" {
		t.Fatalf("scenario trace = %s", got)
	}
}

func TestIntentAndOpenQuestionsContract(t *testing.T) {
	tests := []struct {
		name     string
		mode     DocumentValidationMode
		markdown string
		want     []DiagnosticCode
	}{
		{name: "empty draft section", mode: ValidateDraft, markdown: "# Intent\n\n## Open questions\n", want: nil},
		{name: "deferred draft question", mode: ValidateDraft, markdown: "# Intent\n## Open questions\n- Defer this\n", want: nil},
		{name: "deferred approval question", mode: ValidateApproval, markdown: "# Intent\n## Open questions\n- Defer this\n", want: []DiagnosticCode{DiagnosticOpenQuestionsUnresolved}},
		{name: "None is content", mode: ValidateApproval, markdown: "# Intent\n## Open questions\nNone\n", want: []DiagnosticCode{DiagnosticOpenQuestionsUnresolved}},
		{name: "missing section", mode: ValidateDraft, markdown: "# Intent\n", want: []DiagnosticCode{DiagnosticMissingOpenQuestions}},
		{name: "duplicate section", mode: ValidateDraft, markdown: "# Open questions\n## Open questions\n", want: []DiagnosticCode{DiagnosticDuplicateOpenQuestions}},
		{name: "intent IDs forbidden", mode: ValidateDraft, markdown: "# Intent\n## REQ-001 — no\n## Open questions\n", want: []DiagnosticCode{DiagnosticDisallowedID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ParseDocument(DocumentRequest{Kind: DocumentIntent, Mode: test.mode, Markdown: test.markdown})
			if got := diagnosticCodes(result.Diagnostics); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("diagnostics = %v, want %v (%#v)", got, test.want, result.Diagnostics)
			}
		})
	}
}

func diagnosticCodes(diagnostics []DocumentDiagnostic) []DiagnosticCode {
	if len(diagnostics) == 0 {
		return nil
	}
	result := make([]DiagnosticCode, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		result = append(result, diagnostic.Code)
	}
	return result
}

func hasDiagnostic(result DocumentResult, code DiagnosticCode) bool {
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func containsSubject(diagnostics []DocumentDiagnostic, subject string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Subject == subject {
			return true
		}
	}
	return false
}
