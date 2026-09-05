// Package conformance contains provider-neutral runtime contract checks used
// by adapter tests. It intentionally depends only on agentruntime.
package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// Factory starts a real adapter backed by that adapter's fake transport and
// returns one valid immutable thread configuration.
type Factory func(*testing.T) (agentruntime.Runtime, agentruntime.ThreadConfig)

// ScriptFactory starts an adapter against its fake provider transport. Outputs
// are consumed in RunTurn order; the real adapter must still enforce its
// immutable thread config and provider-neutral response contract.
type ScriptFactory func(*testing.T, []json.RawMessage) Fixture

type Fixture struct {
	Runtime      agentruntime.Runtime
	Workspace    string
	OutputSchema json.RawMessage
	Decode       func(json.RawMessage) (DomainEnvelope, error)
	Wait         func() error
	WriteAllowed func(agentruntime.ThreadConfig, string) bool
}

type DomainEnvelope struct {
	Kind          string
	Message       string
	DecisionCount int
}

// PathRoots names the filesystem regions used by provider write-policy tests.
// LinkRoot must be a link or junction inside ArtifactRoot that resolves into
// ExternalRoot; adapters remain responsible for creating the platform fixture.
type PathRoots struct {
	WorkspaceRoot string
	ArtifactRoot  string
	SiblingRoot   string
	ExternalRoot  string
	LinkRoot      string
}

// PathCase is one provider-neutral write-policy expectation.
type PathCase struct {
	Name        string
	Target      string
	WantAllowed bool
}

// WritePathCases defines the common path boundary exercised by provider
// adapters. The existing artifact fixture is named existing.md; all other
// targets may be absent so adapters must validate newly-created paths too.
func WritePathCases(roots PathRoots) []PathCase {
	return []PathCase{
		{Name: "workspace", Target: filepath.Join(roots.WorkspaceRoot, "source.go")},
		{Name: "artifact existing", Target: filepath.Join(roots.ArtifactRoot, "existing.md"), WantAllowed: true},
		{Name: "artifact new target", Target: filepath.Join(roots.ArtifactRoot, "new", "document.md"), WantAllowed: true},
		{Name: "artifact sibling", Target: filepath.Join(roots.SiblingRoot, "other.md")},
		{Name: "external", Target: filepath.Join(roots.ExternalRoot, "other.md")},
		{Name: "link escape", Target: filepath.Join(roots.LinkRoot, "new.md")},
	}
}

// ClosedThread verifies a lifecycle rule shared by every provider: a closed
// logical thread cannot accept another input, while closing the runtime remains
// safe afterwards.
func ClosedThread(t *testing.T, factory Factory) {
	t.Helper()
	runtime, config := factory(t)
	t.Cleanup(func() { _ = runtime.Close() })
	thread, err := runtime.StartThread(config)
	if err != nil {
		t.Fatalf("start thread: %v", err)
	}
	if err := runtime.CloseThread(thread); err != nil {
		t.Fatalf("close thread: %v", err)
	}
	if _, err := runtime.RunTurn(thread, "must not run"); err == nil {
		t.Fatal("closed thread accepted a turn")
	}
}

// ProviderParity exercises the complete provider-neutral planning contract:
// intent/spec/plan author and reviewer dialogue, decisions, publication,
// automatic rework, approval, independent logical sessions, and close. It then
// starts a second runtime generation for durable resume and verifies that no
// provider thread identity is needed to continue the flow.
func ProviderParity(t *testing.T, factory ScriptFactory) {
	t.Helper()
	outputs := []json.RawMessage{
		json.RawMessage(`{"kind":"message","message":"intent question","decisions":[{"author":"agent","decision":"record scope","rationale":"the intent establishes the durable boundary","alternatives":[],"supersedes":[]}]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"message","message":"spec question","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"message","message":"material decision recorded","decisions":[{"author":"user","decision":"fix review finding SPEC-F-1","rationale":"the durable contract requires the correction","alternatives":[],"supersedes":[]}]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
	}
	fixture := factory(t, outputs)
	assertFixture(t, fixture)
	t.Cleanup(func() { _ = fixture.Runtime.Close() })

	intentRoot := externalArtifactRoot(t, fixture.Workspace)
	intentConfig := roleConfig(fixture, "effective intent-author prompt", intentRoot)
	assertWorkspacePolicy(t, fixture, intentConfig)
	intentAuthor := startRole(t, fixture, intentConfig)
	assertEnvelope(t, fixture, intentAuthor, "intent dialogue", "message", "intent question", 1)
	writeArtifact(t, intentRoot, "intent.md", "# Intent\n")
	assertEnvelope(t, fixture, intentAuthor, "publish intent", "artifact", "", 0)
	assertArtifact(t, intentRoot, "intent.md", "# Intent\n")

	specRoot := externalArtifactRoot(t, fixture.Workspace)
	specAuthor := startRole(t, fixture, roleConfig(fixture, "effective spec-author prompt", specRoot))
	assertEnvelope(t, fixture, specAuthor, "spec dialogue", "message", "spec question", 0)
	writeArtifact(t, specRoot, "spec.md", "# Specification\n")
	assertEnvelope(t, fixture, specAuthor, "publish spec", "artifact", "", 0)

	reviewerRoot := externalArtifactRoot(t, fixture.Workspace)
	reviewer := startRole(t, fixture, roleConfig(fixture, "effective spec-reviewer prompt", reviewerRoot))
	writeArtifact(t, reviewerRoot, "review.md", "# Review\n")
	assertEnvelope(t, fixture, reviewer, "review spec", "artifact", "", 0)
	assertEnvelope(t, fixture, reviewer, "material decision", "message", "material decision recorded", 1)
	writeArtifact(t, specRoot, "spec.md", "# Revised specification\n")
	assertEnvelope(t, fixture, specAuthor, "automatic rework", "artifact", "", 0)
	writeArtifact(t, reviewerRoot, "review.md", "# Approved review\n")
	assertEnvelope(t, fixture, reviewer, "review approval", "artifact", "", 0)

	if err := fixture.Runtime.CloseThread(specAuthor); err != nil {
		t.Fatalf("close spec author thread: %v", err)
	}
	if _, err := fixture.Runtime.RunTurn(specAuthor, "closed"); err == nil {
		t.Fatal("closed author thread accepted a turn")
	}
	if err := fixture.Runtime.CloseThread(reviewer); err != nil {
		t.Fatalf("close reviewer thread: %v", err)
	}

	planRoot := externalArtifactRoot(t, fixture.Workspace)
	planAuthor := startRole(t, fixture, roleConfig(fixture, "effective plan-author prompt", planRoot))
	writeArtifact(t, planRoot, "plan.md", "# Plan\n")
	assertEnvelope(t, fixture, planAuthor, "publish plan", "artifact", "", 0)
	planReviewerRoot := externalArtifactRoot(t, fixture.Workspace)
	planReviewer := startRole(t, fixture, roleConfig(fixture, "effective plan-reviewer prompt", planReviewerRoot))
	writeArtifact(t, planReviewerRoot, "review.md", "# Plan review\n")
	assertEnvelope(t, fixture, planReviewer, "review plan", "artifact", "", 0)

	if err := fixture.Runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if fixture.Wait != nil {
		if err := fixture.Wait(); err != nil {
			t.Fatalf("provider fixture: %v", err)
		}
	}

	resumeOutputs := []json.RawMessage{
		json.RawMessage(`{"kind":"message","message":"resume question","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[],"path":"intent.md"}`),
		json.RawMessage(`{"kind":"artifact","decisions":[]}`),
	}
	resumedFixture := factory(t, resumeOutputs)
	assertFixture(t, resumedFixture)
	t.Cleanup(func() { _ = resumedFixture.Runtime.Close() })
	resumeRoot := externalArtifactRoot(t, resumedFixture.Workspace)
	resume := startRole(t, resumedFixture, roleConfig(resumedFixture, "effective plan-author prompt\ndurable documents, reviews, state, and mem-log", resumeRoot))
	assertEnvelope(t, resumedFixture, resume, "resume dialogue", "message", "resume question", 0)
	writeArtifact(t, resumeRoot, "plan.md", "# Resumed plan\n")
	assertEnvelope(t, resumedFixture, resume, "resume publish", "artifact", "", 0)

	invalidRoot := externalArtifactRoot(t, resumedFixture.Workspace)
	invalid := startRole(t, resumedFixture, roleConfig(resumedFixture, "effective intent-author prompt", invalidRoot))
	if _, err := resumedFixture.Runtime.RunTurn(invalid, "path injection"); err == nil {
		t.Fatal("adapter accepted artifact path in provider output")
	}
	if _, err := resumedFixture.Runtime.RunTurn(invalid, "missing placeholder"); err == nil {
		t.Fatal("adapter accepted artifact without required message placeholder")
	}
	if err := resumedFixture.Runtime.Close(); err != nil {
		t.Fatalf("close resumed runtime: %v", err)
	}
	if resumedFixture.Wait != nil {
		if err := resumedFixture.Wait(); err != nil {
			t.Fatalf("resumed provider fixture: %v", err)
		}
	}
}

func assertFixture(t *testing.T, fixture Fixture) {
	t.Helper()
	if fixture.Runtime == nil || fixture.Workspace == "" || len(fixture.OutputSchema) == 0 || fixture.Decode == nil {
		t.Fatal("provider fixture is incomplete")
	}
}

func roleConfig(fixture Fixture, prompt, artifactRoot string) agentruntime.ThreadConfig {
	return agentruntime.ThreadConfig{
		BootstrapInstructions: prompt,
		OutputSchema:          append(json.RawMessage(nil), fixture.OutputSchema...),
		Workspace:             fixture.Workspace,
		ArtifactRoot:          artifactRoot,
	}
}

func startRole(t *testing.T, fixture Fixture, config agentruntime.ThreadConfig) agentruntime.Thread {
	t.Helper()
	thread, err := fixture.Runtime.StartThread(config)
	if err != nil {
		t.Fatalf("start role thread: %v", err)
	}
	return thread
}

func assertWorkspacePolicy(t *testing.T, fixture Fixture, config agentruntime.ThreadConfig) {
	t.Helper()
	if fixture.WriteAllowed == nil {
		t.Fatal("provider fixture does not expose its write policy")
	}
	if fixture.WriteAllowed(config, filepath.Join(config.Workspace, "intent.md")) {
		t.Fatal("provider can write a project artifact directly")
	}
	if !fixture.WriteAllowed(config, filepath.Join(config.ArtifactRoot, "intent.md")) {
		t.Fatal("provider cannot write the fixed artifact in its external root")
	}
	if fixture.WriteAllowed(config, filepath.Join(filepath.Dir(config.ArtifactRoot), "escape.md")) {
		t.Fatal("provider can write outside its external artifact root")
	}
}

func assertEnvelope(t *testing.T, fixture Fixture, thread agentruntime.Thread, prompt, kind, message string, decisionCount int) {
	t.Helper()
	raw, err := fixture.Runtime.RunTurn(thread, prompt)
	if err != nil {
		t.Fatalf("run %s turn: %v", kind, err)
	}
	envelope, err := fixture.Decode(raw)
	if err != nil {
		t.Fatalf("decode %s turn: %v", kind, err)
	}
	if envelope.Kind != kind || envelope.Message != message || envelope.DecisionCount != decisionCount {
		t.Fatalf("domain envelope = %#v, want kind %q message %q decisions %d", envelope, kind, message, decisionCount)
	}
}

func externalArtifactRoot(t *testing.T, workspace string) string {
	t.Helper()
	root := t.TempDir()
	relative, err := filepath.Rel(workspace, root)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		root, err = os.MkdirTemp("", "stepan-conformance-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
	return root
}

func writeArtifact(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture artifact: %v", err)
	}
}

func assertArtifact(t *testing.T, root, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil || string(data) != want {
		t.Fatalf("artifact %s = %q, %v", name, data, err)
	}
}
