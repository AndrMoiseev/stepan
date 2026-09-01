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

// ProviderParity exercises the complete contract shared by Codex and Claude:
// author and reviewer messages/artifacts, independent logical sessions,
// closing one role without affecting another, a fresh resume thread, and
// rejection of provider output that tries to smuggle an artifact path or omit
// a required transport placeholder.
func ProviderParity(t *testing.T, factory ScriptFactory) {
	t.Helper()
	outputs := []json.RawMessage{
		json.RawMessage(`{"kind":"message","message":"author question","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"message","message":"review complete","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`),
		json.RawMessage(`{"kind":"artifact","message":"","decisions":[],"path":"intent.md"}`),
		json.RawMessage(`{"kind":"artifact","decisions":[]}`),
	}
	fixture := factory(t, outputs)
	if fixture.Runtime == nil || fixture.Workspace == "" || len(fixture.OutputSchema) == 0 || fixture.Decode == nil {
		t.Fatal("provider fixture is incomplete")
	}
	t.Cleanup(func() { _ = fixture.Runtime.Close() })

	authorRoot := externalArtifactRoot(t, fixture.Workspace)
	reviewerRoot := externalArtifactRoot(t, fixture.Workspace)
	authorConfig := roleConfig(fixture, "effective intent-author prompt", authorRoot)
	assertWorkspacePolicy(t, fixture, authorConfig)
	author := startRole(t, fixture, authorConfig)
	reviewer := startRole(t, fixture, roleConfig(fixture, "effective spec-reviewer prompt", reviewerRoot))

	assertEnvelope(t, fixture, author, "ask", "message", "author question")
	writeArtifact(t, authorRoot, "intent.md", "# Intent\n")
	assertEnvelope(t, fixture, author, "write", "artifact", "")
	assertArtifact(t, authorRoot, "intent.md", "# Intent\n")

	writeArtifact(t, reviewerRoot, "review.md", "# Review\n")
	assertEnvelope(t, fixture, reviewer, "review", "artifact", "")
	assertArtifact(t, reviewerRoot, "review.md", "# Review\n")

	if err := fixture.Runtime.CloseThread(author); err != nil {
		t.Fatalf("close author thread: %v", err)
	}
	if _, err := fixture.Runtime.RunTurn(author, "closed"); err == nil {
		t.Fatal("closed author thread accepted a turn")
	}
	assertEnvelope(t, fixture, reviewer, "continue reviewer", "message", "review complete")
	if err := fixture.Runtime.CloseThread(reviewer); err != nil {
		t.Fatalf("close reviewer thread: %v", err)
	}

	resumeRoot := externalArtifactRoot(t, fixture.Workspace)
	resume := startRole(t, fixture, roleConfig(fixture, "effective plan-author prompt\ndurable resume context", resumeRoot))
	writeArtifact(t, resumeRoot, "plan.md", "# Plan\n")
	assertEnvelope(t, fixture, resume, "resume", "artifact", "")

	invalidRoot := externalArtifactRoot(t, fixture.Workspace)
	invalid := startRole(t, fixture, roleConfig(fixture, "effective intent-author prompt", invalidRoot))
	if _, err := fixture.Runtime.RunTurn(invalid, "path injection"); err == nil {
		t.Fatal("adapter accepted artifact path in provider output")
	}
	if _, err := fixture.Runtime.RunTurn(invalid, "missing placeholder"); err == nil {
		t.Fatal("adapter accepted artifact without required message placeholder")
	}

	if err := fixture.Runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	if fixture.Wait != nil {
		if err := fixture.Wait(); err != nil {
			t.Fatalf("provider fixture: %v", err)
		}
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

func assertEnvelope(t *testing.T, fixture Fixture, thread agentruntime.Thread, prompt, kind, message string) {
	t.Helper()
	raw, err := fixture.Runtime.RunTurn(thread, prompt)
	if err != nil {
		t.Fatalf("run %s turn: %v", kind, err)
	}
	envelope, err := fixture.Decode(raw)
	if err != nil {
		t.Fatalf("decode %s turn: %v", kind, err)
	}
	if envelope.Kind != kind || envelope.Message != message || envelope.DecisionCount != 0 {
		t.Fatalf("domain envelope = %#v, want kind %q message %q", envelope, kind, message)
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
