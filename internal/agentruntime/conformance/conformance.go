// Package conformance contains provider-neutral runtime contract checks used
// by adapter tests. It intentionally depends only on agentruntime.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

// Factory starts a real adapter backed by that adapter's fake transport and
// returns one valid immutable thread configuration.
type Factory func(*testing.T) (agentruntime.Runtime, agentruntime.ThreadConfig)

// ScriptFactory starts an adapter against its fake provider transport. Steps
// are consumed in RunTurn order; the real adapter must still enforce its
// immutable thread config and provider-neutral response contract.
type ScriptFactory func(*testing.T, Script) Fixture

// ScriptStep is one named provider outcome. Prompt is used by runtime-level
// suites; Role and Turn identify application-level outcomes across independent
// provider processes. Artifact fields describe the file published before the
// artifact envelope is returned.
type ScriptStep struct {
	Name            string          `json:"name"`
	Prompt          string          `json:"prompt,omitempty"`
	Role            string          `json:"role,omitempty"`
	Turn            int             `json:"turn,omitempty"`
	Output          json.RawMessage `json:"output"`
	ArtifactName    string          `json:"artifact_name,omitempty"`
	ArtifactContent string          `json:"artifact_content,omitempty"`
}

// Script is the single source of truth shared by adapter transports and fake
// child processes.
type Script []ScriptStep

func (script Script) Outputs() []json.RawMessage {
	result := make([]json.RawMessage, len(script))
	for index, step := range script {
		result[index] = append(json.RawMessage(nil), step.Output...)
	}
	return result
}

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
	script := Script{
		{Name: "intent dialogue", Prompt: "intent dialogue", Output: json.RawMessage(`{"kind":"message","message":"intent question","decisions":[{"author":"agent","decision":"record scope","rationale":"the intent establishes the durable boundary","alternatives":[],"supersedes":[]}]}`)},
		{Name: "publish intent", Prompt: "publish intent", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "spec dialogue", Prompt: "spec dialogue", Output: json.RawMessage(`{"kind":"message","message":"spec question","decisions":[]}`)},
		{Name: "publish spec", Prompt: "publish spec", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "review spec", Prompt: "review spec", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "material decision", Prompt: "material decision", Output: json.RawMessage(`{"kind":"message","message":"material decision recorded","decisions":[{"author":"user","decision":"fix review finding SPEC-F-1","rationale":"the durable contract requires the correction","alternatives":[],"supersedes":[]}]}`)},
		{Name: "automatic rework", Prompt: "automatic rework", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "review approval", Prompt: "review approval", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "publish plan", Prompt: "publish plan", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "review plan", Prompt: "review plan", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
	}
	fixture := factory(t, script)
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

	resumeScript := Script{
		{Name: "resume dialogue", Prompt: "resume dialogue", Output: json.RawMessage(`{"kind":"message","message":"resume question","decisions":[]}`)},
		{Name: "resume publish", Prompt: "resume publish", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`)},
		{Name: "path injection", Prompt: "path injection", Output: json.RawMessage(`{"kind":"artifact","message":"","decisions":[],"path":"intent.md"}`)},
		{Name: "missing placeholder", Prompt: "missing placeholder", Output: json.RawMessage(`{"kind":"artifact","decisions":[]}`)},
	}
	resumedFixture := factory(t, resumeScript)
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

// ApplicationFactory starts one real adapter generation against the supplied
// provider-neutral script in an existing Git workspace.
type ApplicationFactory func(*testing.T, string, Script) ApplicationFixture

type ApplicationFixture struct {
	Runtime agentruntime.Runtime
	Wait    func() error
}

// ScriptedArtifacts decorates fake in-memory transports that cannot write an
// artifact themselves. The underlying adapter still receives and validates
// every scripted provider output. Process-backed fixtures should instead pass
// Script to the child and return their runtime directly.
func ScriptedArtifacts(runtime agentruntime.Runtime, script Script) agentruntime.Runtime {
	return &scriptedArtifactRuntime{runtime: runtime, script: append(Script(nil), script...)}
}

type scriptedThread struct {
	handle agentruntime.Thread
	config agentruntime.ThreadConfig
}

type scriptedArtifactRuntime struct {
	runtime agentruntime.Runtime
	script  Script
	next    int
	threads []scriptedThread
}

func (runtime *scriptedArtifactRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	handle, err := runtime.runtime.StartThread(config)
	if err == nil {
		runtime.threads = append(runtime.threads, scriptedThread{handle: handle, config: config.Clone()})
	}
	return handle, err
}

func (runtime *scriptedArtifactRuntime) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	if runtime.next >= len(runtime.script) {
		return nil, fmt.Errorf("conformance script exhausted at prompt %q", prompt)
	}
	step := runtime.script[runtime.next]
	config, ok := runtime.threadConfig(thread)
	if !ok {
		return nil, errors.New("conformance script received an unknown thread")
	}
	role := ScriptRole(config)
	if step.Role != "" && step.Role != role {
		return nil, fmt.Errorf("conformance step %q role = %q, want %q", step.Name, role, step.Role)
	}
	if step.ArtifactName != "" {
		if err := os.WriteFile(filepath.Join(config.ArtifactRoot, step.ArtifactName), []byte(step.ArtifactContent), 0o600); err != nil {
			return nil, fmt.Errorf("write conformance artifact for %s: %w", step.Name, err)
		}
	}
	runtime.next++
	return runtime.runtime.RunTurn(thread, prompt)
}

func (runtime *scriptedArtifactRuntime) threadConfig(thread agentruntime.Thread) (agentruntime.ThreadConfig, bool) {
	for _, candidate := range runtime.threads {
		left, right := reflect.ValueOf(candidate.handle), reflect.ValueOf(thread)
		if left.IsValid() && right.IsValid() && left.Type() == right.Type() && left.Type().Comparable() && left.Interface() == right.Interface() {
			return candidate.config.Clone(), true
		}
	}
	return agentruntime.ThreadConfig{}, false
}

func (runtime *scriptedArtifactRuntime) CloseThread(thread agentruntime.Thread) error {
	return runtime.runtime.CloseThread(thread)
}
func (runtime *scriptedArtifactRuntime) Interrupt() error { return runtime.runtime.Interrupt() }
func (runtime *scriptedArtifactRuntime) Close() error     { return runtime.runtime.Close() }

// ScriptRole returns the logical role encoded by an immutable specflow thread
// configuration. It is intentionally test-only and provider-neutral.
func ScriptRole(config agentruntime.ThreadConfig) string {
	if strings.Contains(string(config.OutputSchema), `"feature_id"`) {
		return "feature-id"
	}
	for _, role := range []string{"intent-author", "spec-author", "spec-reviewer", "plan-author", "plan-reviewer"} {
		if strings.Contains(config.BootstrapInstructions, "[roles/"+role+"]") {
			return role
		}
	}
	return "unknown"
}

// ApplicationParity runs the same durable ApplicationController flow and
// exact command availability through a real adapter, then closes it and
// resumes with a newly-created adapter generation.
func ApplicationParity(t *testing.T, identity specflow.RuntimeIdentity, factory ApplicationFactory) {
	t.Helper()
	workspace := applicationGitRoot(t)
	firstScript := firstApplicationScript()
	first := factory(t, workspace, firstScript)
	if first.Runtime == nil {
		t.Fatal("application fixture has no runtime")
	}
	application, registry, session := applicationHarness(t, workspace, identity, first.Runtime)

	progress, err := application.StartFeature("exercise shared provider parity")
	if err != nil {
		t.Fatal(err)
	}
	featureID := progress.FeatureID
	assertApplicationProgress(t, progress, specflow.StageIntent, specflow.StageDrafting, specflow.ReviewNotStarted, true, "/status", "/exit")
	progress = submitApplication(t, application, "write intent")
	assertApplicationProgress(t, progress, specflow.StageIntent, specflow.StagePublished, specflow.ReviewNotStarted, true, "/approve", "/status", "/exit")
	progress = submitApplication(t, application, "/approve")
	assertApplicationProgress(t, progress, specflow.StageSpec, specflow.StageDrafting, specflow.ReviewNotStarted, true, "/status", "/exit")
	progress = submitApplication(t, application, "write spec")
	assertApplicationProgress(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewNotStarted, true, "/review", "/approve", "/status", "/exit")
	progress = submitApplication(t, application, "/review")
	assertApplicationProgress(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewAwaitingDecisions, true, "/apply", "/status", "/exit")
	progress = submitApplication(t, application, "/apply")
	assertApplicationProgress(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewNotStarted, true, "/review", "/approve", "/status", "/exit")
	progress = submitApplication(t, application, "/review")
	assertApplicationProgress(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewCompleted, true, "/review", "/approve", "/status", "/exit")
	progress = submitApplication(t, application, "/approve")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StageDrafting, specflow.ReviewNotStarted, true, "/revise-spec", "/status", "/exit")

	closeApplicationFixture(t, application, registry, session, first)

	secondScript := resumedApplicationScript()
	second := factory(t, workspace, secondScript)
	if second.Runtime == nil {
		t.Fatal("resumed application fixture has no runtime")
	}
	resumed, resumedRegistry, resumedSession := applicationHarness(t, workspace, identity, second.Runtime)
	progress, err = resumed.Resume(featureID)
	if err != nil {
		t.Fatal(err)
	}
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StageDrafting, specflow.ReviewNotStarted, true, "/revise-spec", "/status", "/exit")
	progress = submitApplication(t, resumed, "write plan")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewNotStarted, true, "/review", "/approve", "/revise-spec", "/status", "/exit")
	progress = submitApplication(t, resumed, "/review")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewAwaitingDecisions, true, "/apply", "/revise-spec", "/status", "/exit")
	progress = submitApplication(t, resumed, "/apply")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewNotStarted, true, "/review", "/approve", "/revise-spec", "/status", "/exit")
	progress = submitApplication(t, resumed, "/review")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewCompleted, true, "/review", "/approve", "/revise-spec", "/status", "/exit")
	progress = submitApplication(t, resumed, "/approve")
	assertApplicationProgress(t, progress, specflow.StagePlan, specflow.StageCommitted, specflow.ReviewCompleted, false, "/revise-spec", "/status", "/exit")
	closeApplicationFixture(t, resumed, resumedRegistry, resumedSession, second)
}

func applicationHarness(t *testing.T, workspace string, identity specflow.RuntimeIdentity, runtime agentruntime.Runtime) (*specflow.ApplicationController, *specflow.SessionRegistry, *specflow.Session) {
	t.Helper()
	repository, err := specflow.NewFSFeatureRepository(workspace)
	if err != nil {
		t.Fatal(err)
	}
	session := specflow.NewSession(func(context.Context) (agentruntime.Runtime, error) { return runtime, nil })
	registry, err := specflow.NewSessionRegistry(session, repository)
	if err != nil {
		t.Fatal(err)
	}
	catalog := specflow.NewEmbeddedPromptCatalog()
	author, err := specflow.NewStageEngine(workspace, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := specflow.NewReviewEngine(workspace, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := specflow.NewFeatureController(repository, author, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := specflow.NewResumeManager(repository, registry, flow)
	if err != nil {
		t.Fatal(err)
	}
	application, err := specflow.NewApplicationController(workspace, session, manager, flow, identity)
	if err != nil {
		t.Fatal(err)
	}
	return application, registry, session
}

func closeApplicationFixture(t *testing.T, application *specflow.ApplicationController, registry *specflow.SessionRegistry, session *specflow.Session, fixture ApplicationFixture) {
	t.Helper()
	if _, err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if fixture.Wait != nil {
		if err := fixture.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func submitApplication(t *testing.T, application *specflow.ApplicationController, input string) specflow.Progress {
	t.Helper()
	progress, err := application.Submit(input)
	if err != nil {
		t.Fatalf("submit %q: %v", input, err)
	}
	return progress
}

func assertApplicationProgress(t *testing.T, progress specflow.Progress, stage specflow.Stage, status specflow.StageStatus, review specflow.ReviewStatus, textAllowed bool, commands ...string) {
	t.Helper()
	got := make([]string, len(progress.CommandHints))
	for index, hint := range progress.CommandHints {
		got[index] = hint.Command
	}
	if progress.CurrentStage != stage || progress.StageStatus != status || progress.ReviewStatus != review || progress.TextAllowed != textAllowed || !reflect.DeepEqual(got, commands) {
		t.Fatalf("progress = %s/%s/%s text=%t commands=%v, want %s/%s/%s text=%t commands=%v", progress.CurrentStage, progress.StageStatus, progress.ReviewStatus, progress.TextAllowed, got, stage, status, review, textAllowed, commands)
	}
}

func applicationGitRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("provider parity fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"config", "user.name", "Stepan Tests"}, {"config", "user.email", "stepan@example.invalid"}, {"config", "commit.gpgsign", "false"}, {"add", "README.md"}, {"commit", "--quiet", "-m", "initial"}} {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return root
}

func firstApplicationScript() Script {
	return Script{
		applicationStep("feature id", "feature-id", 1, `{"feature_id":"provider-parity"}`, "", ""),
		applicationStep("intent question", "intent-author", 1, messageOutput("What durable outcome should the intent guarantee?"), "", ""),
		applicationStep("intent artifact", "intent-author", 2, artifactOutput, "intent.md", ApplicationIntent()),
		applicationStep("spec question", "spec-author", 1, messageOutput("Which durable requirements should the specification cover?"), "", ""),
		applicationStep("spec artifact", "spec-author", 2, artifactOutput, "spec.md", ApplicationSpec(false)),
		applicationStep("spec material review", "spec-reviewer", 1, artifactOutput, "review.md", ReviewArtifact("SPEC-F-001", false)),
		applicationStep("spec rework", "spec-author", 3, artifactOutput, "spec.md", ApplicationSpec(true)),
		applicationStep("spec accepted review", "spec-reviewer", 2, artifactOutput, "review.md", ReviewArtifact("SPEC-F-001", true)),
		applicationStep("plan question", "plan-author", 1, messageOutput("Which tasks prove the accepted specification?"), "", ""),
	}
}

func resumedApplicationScript() Script {
	return Script{
		applicationStep("resumed plan question", "plan-author", 1, messageOutput("Which tasks prove the accepted specification?"), "", ""),
		applicationStep("plan artifact", "plan-author", 2, artifactOutput, "plan.md", ApplicationPlan(false)),
		applicationStep("plan material review", "plan-reviewer", 1, artifactOutput, "review.md", ReviewArtifact("PLAN-F-001", false)),
		applicationStep("plan rework", "plan-author", 3, artifactOutput, "plan.md", ApplicationPlan(true)),
		applicationStep("plan accepted review", "plan-reviewer", 2, artifactOutput, "review.md", ReviewArtifact("PLAN-F-001", true)),
	}
}

const artifactOutput = `{"kind":"artifact","message":"","decisions":[]}`

func applicationStep(name, role string, turn int, output, artifactName, artifactContent string) ScriptStep {
	return ScriptStep{Name: name, Role: role, Turn: turn, Output: json.RawMessage(output), ArtifactName: artifactName, ArtifactContent: artifactContent}
}

func messageOutput(message string) string {
	data, _ := json.Marshal(map[string]any{"kind": "message", "message": message, "decisions": []any{}})
	return string(data)
}

func ApplicationIntent() string {
	return "# Provider parity\n\n## Scope\n\nExercise the complete durable planning flow.\n\n## Open questions\n"
}

func ApplicationSpec(reworked bool) string {
	acceptance := "The state survives restart."
	if reworked {
		acceptance = "The state survives restart and explicit review recheck."
	}
	return "# Specification\n\n## REQ-001 — Durable flow\n\nThe provider-neutral flow persists its state.\n\n" +
		"## DEC-001 — Durable context\n\nUse documents, reviews, state, and mem-log.\n\n" +
		"## AC-001 — Resume\n\nTraces: REQ-001\n\n" + acceptance + "\n\n## Open questions\n"
}

func ApplicationPlan(reworked bool) string {
	result := "The automated suite completes the flow."
	if reworked {
		result = "The automated suite completes the reviewed flow."
	}
	return "# Plan\n\n## TASK-001 — Verify provider parity\n\nTraces: REQ-001, DEC-001\n\n" +
		"### Test scenario — complete flow\n\nTraces: AC-001\n\n" + result + "\n\n## Open questions\n"
}

// ReviewArtifact is the canonical pending/resolved material-review fixture.
func ReviewArtifact(id string, resolved bool) string {
	status, resolution, decision, decidedBy, rationale := "open", "", "pending", "none", ""
	if resolved {
		status = "resolved"
		resolution = "Resolution: The revised document now makes durable provider parity explicit.\n"
		decision = "fix"
		decidedBy = "user"
		rationale = "User accepted the pending recommendation with /apply."
	}
	return "# Review\n\n## " + id + " — Durable ambiguity\n\n" +
		"Severity: major\nStatus: " + status + "\n" +
		"Problem: The document leaves durable provider parity insufficiently explicit.\n" +
		"Location: whole document\nRecommendation: Clarify durable provider parity.\n" + resolution +
		"Decision: " + decision + "\nDecided-by: " + decidedBy + "\nRationale: " + rationale + "\n"
}
