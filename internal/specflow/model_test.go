package specflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestStableIDCanonicalizesNumericSuffix(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"REQ-001":     "REQ-1",
		"DEC-00012":   "DEC-12",
		"AC-9":        "AC-9",
		"TASK-010":    "TASK-10",
		"SPEC-F-0003": "SPEC-F-3",
		"PLAN-F-42":   "PLAN-F-42",
	} {
		id, err := ParseStableID(input)
		if err != nil {
			t.Fatalf("ParseStableID(%q): %v", input, err)
		}
		if got := id.String(); got != want {
			t.Errorf("ParseStableID(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "REQ-000", "REQ-0", "REQ--1", "REQ-1x", "UNKNOWN-1"} {
		if _, err := ParseStableID(input); !errors.Is(err, ErrInvalidDomainValue) {
			t.Errorf("ParseStableID(%q) error = %v, want domain error", input, err)
		}
	}
	if _, err := json.Marshal(StableID{}); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("marshal zero StableID error = %v", err)
	}
}

func TestStableIDHasNoNumericSuffixUpperBound(t *testing.T) {
	t.Parallel()

	const suffix = "18446744073709551616000000000000000000000000000000000000000000000001"
	id, err := ParseStableID("REQ-000" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := id.String(), "REQ-"+suffix; got != want {
		t.Fatalf("canonical ID = %q, want %q", got, want)
	}
	if got := id.Number(); got != suffix {
		t.Fatalf("numeric suffix = %q, want %q", got, suffix)
	}

	aliases := map[StableID]string{id: "retained"}
	canonical, err := ParseStableID("REQ-" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	if aliases[canonical] != "retained" {
		t.Fatal("canonical StableID is not comparable map identity")
	}

	encoded, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	var decoded StableID
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != id {
		t.Fatalf("round trip changed StableID: got %q, want %q", decoded, id)
	}
}

func TestFingerprintEqualityUsesOnlyNormalizedDocumentHashes(t *testing.T) {
	t.Parallel()

	a, err := NewFingerprint("target", []UpstreamHash{{Stage: StageSpec, Hash: "spec"}, {Stage: StageIntent, Hash: "intent"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFingerprint("target", []UpstreamHash{{Stage: StageIntent, Hash: "intent"}, {Stage: StageSpec, Hash: "spec"}})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatalf("fingerprints with the same target and upstream map are not equal: %#v %#v", a, b)
	}
	c, err := NewFingerprint("other", a.UpstreamHashes())
	if err != nil {
		t.Fatal(err)
	}
	if a.Equal(c) {
		t.Fatal("different target hashes compare equal")
	}

	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Fingerprint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !a.Equal(decoded) {
		t.Fatalf("fingerprint round trip changed value: %s", encoded)
	}
}

func TestFindingIdentityAndDecisionRules(t *testing.T) {
	t.Parallel()

	contractID := mustStableID(t, "SPEC-F-001")
	contract, err := NewFinding(FindingInput{
		ID:        contractID,
		Kind:      FindingContractViolation,
		Severity:  SeverityMajor,
		Problem:   "Missing Traces",
		Rationale: "The document contract requires Traces",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contract.WithSeverity(SeverityMinor); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("changed severity error = %v", err)
	}
	if got := contract.Snapshot().Severity; got != SeverityMajor {
		t.Fatalf("failed severity update changed original finding to %q", got)
	}
	if _, err := contract.Update(FindingUpdate{
		Status:   FindingDismissed,
		Decision: FindingDecisionRecord{Decision: DecisionDismiss, DecidedBy: DecidedByUser, Rationale: "ignore"},
	}); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("dismiss contract finding error = %v", err)
	}

	material, err := NewFinding(FindingInput{
		ID:       mustStableID(t, "PLAN-F-2"),
		Kind:     FindingMaterial,
		Severity: SeverityBlocker,
		Problem:  "Task split changes architecture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := material.Snapshot().Decision; got.Decision != DecisionPending || got.DecidedBy != DecidedByNone {
		t.Fatalf("initial material decision = %#v", got)
	}
	if _, err := material.Update(FindingUpdate{
		Status:     FindingResolved,
		Resolution: "Changed the task split",
		Decision:   FindingDecisionRecord{Decision: DecisionFix, DecidedBy: DecidedByReviewer, Rationale: "better"},
	}); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("reviewer material decision error = %v", err)
	}
	if got := material.Snapshot().Status; got != FindingOpen {
		t.Fatalf("failed update mutated original finding to %q", got)
	}
	resolved, err := material.Update(FindingUpdate{
		Status:     FindingResolved,
		Resolution: "Changed the task split",
		Decision:   FindingDecisionRecord{Decision: DecisionFix, DecidedBy: DecidedByUser, Rationale: "Matches intended architecture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Snapshot(); got.Status != FindingResolved || got.Decision.DecidedBy != DecidedByUser {
		t.Fatalf("resolved finding = %#v", got)
	}
}

func TestStageRoleAndStatusValuesRejectUnknownStrings(t *testing.T) {
	t.Parallel()

	if _, err := ParseStage("implementation"); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("unknown stage error = %v", err)
	}
	if _, err := ParseRole("intent-reviewer"); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("unknown role error = %v", err)
	}
	if _, err := ReviewerRole(StageIntent); !errors.Is(err, ErrInvalidDomainValue) {
		t.Fatalf("intent reviewer error = %v", err)
	}
	for name, valid := range map[string]bool{
		"flow":   FlowStatus("paused").Valid(),
		"stage":  StageStatus("approved").Valid(),
		"review": ReviewStatus("paused").Valid(),
	} {
		if valid {
			t.Errorf("unknown %s status accepted", name)
		}
	}
}

func TestEnvelopeNormalizesMessageAndArtifact(t *testing.T) {
	t.Parallel()

	message, err := DecodeEnvelope([]byte(`{"kind":"message","message":"question","decisions":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != KindMessage || message.Message != "question" {
		t.Fatalf("message envelope = %#v", message)
	}
	artifact, err := DecodeEnvelope([]byte(`{"kind":"artifact","message":"","decisions":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != KindArtifact || artifact.Message != "" {
		t.Fatalf("artifact envelope = %#v", artifact)
	}

	invalid := []string{
		`{"kind":"artifact","decisions":[]}`,
		`{"kind":"artifact","message":"artifact text","decisions":[]}`,
		`{"kind":"message","message":"question"}`,
		`{"kind":"message","message":"","decisions":[]}`,
		`{"kind":"artifact","message":"","decisions":[],"path":"intent.md"}`,
	}
	for _, value := range invalid {
		if _, err := DecodeEnvelope([]byte(value)); err == nil {
			t.Errorf("DecodeEnvelope accepted %s", value)
		}
	}

	var schema map[string]any
	if err := json.Unmarshal(DialogueSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if _, exists := schema["oneOf"]; exists {
		t.Fatalf("Codex dialogue schema contains oneOf: %s", DialogueSchema())
	}
	required, ok := schema["required"].([]any)
	if !ok || !reflect.DeepEqual(required, []any{"kind", "message", "decisions"}) {
		t.Fatalf("dialogue required properties = %#v", schema["required"])
	}
	if strings.Contains(string(DialogueSchema()), `"path"`) {
		t.Fatalf("dialogue schema exposes artifact path: %s", DialogueSchema())
	}
}

func mustStableID(t *testing.T, value string) StableID {
	t.Helper()
	id, err := ParseStableID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
