package specflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestApplicationControllerRunsCompletePlanningFlowWithTwoReviewReworkCycles(t *testing.T) {
	root := initializedCommitRepository(t)
	runtime := &fullFlowRuntime{configs: make(map[int]agentruntime.ThreadConfig), turns: make(map[Role]int), firstIntentQuestion: "What outcome should this planning flow produce?"}
	repository := newTestFeatureRepository(t, root)
	registry, err := NewSessionRegistry(runtime, repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	catalog := NewEmbeddedPromptCatalog()
	author, err := NewStageEngine(root, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewReviewEngine(root, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := NewFeatureController(repository, author, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewResumeManager(repository, registry, flow)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplicationController(root, runtime, manager, flow, RuntimeIdentity{Provider: "codex", Model: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	application.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }

	progress := submitApplication(t, application, "start", "Build the complete planning lifecycle")
	if progress.FeatureID != "2026-08-30-complete-flow" || progress.CurrentStage != StageIntent || progress.StageStatus != StageDrafting {
		t.Fatalf("begin progress = %#v", progress)
	}
	progress = submitApplication(t, application, "intent draft", "Write the intent")
	assertApplicationProgress(t, progress, StageIntent, StagePublished, ReviewNotStarted, "/approve")
	progress = submitApplication(t, application, "intent approval", "/approve")
	assertApplicationProgress(t, progress, StageSpec, StageDrafting, ReviewNotStarted)

	progress = submitApplication(t, application, "spec draft", "Write the specification")
	assertApplicationProgress(t, progress, StageSpec, StagePublished, ReviewNotStarted, "/review", "/approve")
	progress = submitApplication(t, application, "spec review", "/review")
	assertApplicationProgress(t, progress, StageSpec, StagePublished, ReviewCompleted, "/review", "/approve")
	if progress.Review.ReworkAttempts != 1 || len(progress.Review.Progress) != 2 {
		t.Fatalf("spec review did not expose automatic rework: %#v", progress.Review)
	}
	progress = submitApplication(t, application, "spec approval", "/approve")
	assertApplicationProgress(t, progress, StagePlan, StageDrafting, ReviewNotStarted)

	progress = submitApplication(t, application, "plan draft", "Write the plan")
	assertApplicationProgress(t, progress, StagePlan, StagePublished, ReviewNotStarted, "/review", "/approve")
	progress = submitApplication(t, application, "plan review", "/review")
	assertApplicationProgress(t, progress, StagePlan, StagePublished, ReviewCompleted, "/review", "/approve")
	if progress.Review.ReworkAttempts != 1 || len(progress.Review.Progress) != 2 {
		t.Fatalf("plan review did not expose automatic rework: %#v", progress.Review)
	}
	progress = submitApplication(t, application, "plan approval", "/approve")
	assertApplicationProgress(t, progress, StagePlan, StageCommitted, ReviewCompleted)

	feature, err := repository.Load(progress.FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feature.Documents) != 3 || len(feature.Reviews) != 2 || !strings.HasSuffix(filepath.ToSlash(feature.Reviews[0].Path), "/reviews/spec-001.md") || !strings.HasSuffix(filepath.ToSlash(feature.Reviews[1].Path), "/reviews/plan-001.md") {
		t.Fatalf("durable artifacts = documents:%#v reviews:%#v", feature.Documents, feature.Reviews)
	}
	flows, err := application.DiscoverResumable()
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 0 {
		t.Fatalf("committed plan remained resumable: %#v", flows)
	}
	wantRoles := []Role{RoleIntentAuthor, RoleSpecAuthor, RoleSpecReviewer, RolePlanAuthor, RolePlanReviewer}
	if !reflect.DeepEqual(runtime.startedRoles, wantRoles) {
		t.Fatalf("role sessions = %v, want %v", runtime.startedRoles, wantRoles)
	}
	log := gitOutputForTest(t, root, "log", "-3", "--format=%s")
	for _, message := range []string{
		"feature(2026-08-30-complete-flow): approve plan",
		"feature(2026-08-30-complete-flow): approve spec",
		"feature(2026-08-30-complete-flow): approve intent",
	} {
		if !strings.Contains(log, message) {
			t.Fatalf("phase commit %q missing from:\n%s", message, log)
		}
	}
}

func TestApplicationControllerStartFeatureReturnsInitialAuthorQuestion(t *testing.T) {
	root := initializedCommitRepository(t)
	runtime := &fullFlowRuntime{
		configs:             make(map[int]agentruntime.ThreadConfig),
		turns:               make(map[Role]int),
		firstIntentQuestion: "Какой результат должен увидеть пользователь?",
	}
	application, repository, registry := newApplicationHarness(t, root, runtime)
	t.Cleanup(func() { _ = registry.Close() })
	application.now = func() time.Time { return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) }

	progress, err := application.StartFeature("Добавить поддержку нового агента")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := progress.Message, runtime.firstIntentQuestion; got != want {
		t.Fatalf("initial author message = %q, want %q; turns=%v progress=%#v", got, want, runtime.turns, progress)
	}
	if !reflect.DeepEqual(runtime.intentInputs, []string{"Добавить поддержку нового агента"}) {
		t.Fatalf("intent inputs = %#v", runtime.intentInputs)
	}
	feature, err := repository.Load(progress.FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	briefs, userMessages := 0, 0
	for _, entry := range feature.Journal {
		if entry.Kind == MemLogBrief {
			briefs++
		}
		if entry.Kind == MemLogUserMessage {
			userMessages++
		}
	}
	if briefs != 1 || userMessages != 0 {
		t.Fatalf("initial journal entries: briefs=%d user_messages=%d journal=%#v", briefs, userMessages, feature.Journal)
	}
}

func TestApplicationControllerResumeDiscardsPendingRevisionAndStartsFromPublishedDocument(t *testing.T) {
	root := initializedCommitRepository(t)
	firstRuntime := &fullFlowRuntime{configs: make(map[int]agentruntime.ThreadConfig), turns: make(map[Role]int), firstIntentQuestion: "What should be resumed?"}
	application, repository, registry := newApplicationHarness(t, root, firstRuntime)
	application.now = func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }

	progress := submitApplication(t, application, "start", "Resume a pending revision")
	progress = submitApplication(t, application, "published intent", "Write the intent")
	intentPath := filepath.Join(root, filepath.FromSlash(progress.Path))
	published, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	progress = submitApplication(t, application, "pending intent revision", "Revise the intent")
	if len(progress.Revision) != 3 || progress.Diff == "" {
		t.Fatalf("revision was not pending: %#v", progress)
	}
	intentRoot := firstRuntime.artifactRoot(RoleIntentAuthor)
	if _, err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(intentRoot); !os.IsNotExist(err) {
		t.Fatalf("pending artifact root remains after close: %v", err)
	}
	current, err := os.ReadFile(intentPath)
	if err != nil || string(current) != string(published) {
		t.Fatalf("published document changed on close: %q, %v", current, err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}

	secondRuntime := &fullFlowRuntime{configs: make(map[int]agentruntime.ThreadConfig), turns: make(map[Role]int)}
	resumedApplication, _, resumedRegistry := newApplicationHarness(t, root, secondRuntime)
	t.Cleanup(func() { _ = resumedRegistry.Close() })
	flows, err := resumedApplication.DiscoverResumable()
	if err != nil || len(flows) != 1 || flows[0].FeatureID != progress.FeatureID {
		t.Fatalf("resumable flows = %#v, %v", flows, err)
	}
	resumed, err := resumedApplication.Resume(progress.FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	assertApplicationProgress(t, resumed, StageIntent, StagePublished, ReviewNotStarted, "/approve")
	if len(resumed.Revision) != 0 || secondRuntime.turns[RoleIntentAuthor] != 0 || !reflect.DeepEqual(secondRuntime.startedRoles, []Role{RoleIntentAuthor}) {
		t.Fatalf("resume continued pending turn: progress=%#v roles=%v turns=%v", resumed, secondRuntime.startedRoles, secondRuntime.turns)
	}
	loaded, err := repository.Load(progress.FeatureID)
	if err != nil || string(loaded.Documents[StageIntent].Content) != string(published) {
		t.Fatalf("durable published revision = %#v, %v", loaded.Documents[StageIntent], err)
	}
}

func newApplicationHarness(t *testing.T, root string, runtime *fullFlowRuntime) (*ApplicationController, *FSFeatureRepository, *SessionRegistry) {
	t.Helper()
	repository := newTestFeatureRepository(t, root)
	registry, err := NewSessionRegistry(runtime, repository)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewEmbeddedPromptCatalog()
	author, err := NewStageEngine(root, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewReviewEngine(root, registry, repository, catalog)
	if err != nil {
		t.Fatal(err)
	}
	flow, err := NewFeatureController(repository, author, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewResumeManager(repository, registry, flow)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplicationController(root, runtime, manager, flow, RuntimeIdentity{Provider: "codex", Model: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	return application, repository, registry
}

func submitApplication(t *testing.T, application *ApplicationController, name, input string) Progress {
	t.Helper()
	var (
		progress Progress
		err      error
	)
	if name == "start" {
		progress, err = application.StartFeature(input)
	} else {
		progress, err = application.Submit(input)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return progress
}

func assertApplicationProgress(t *testing.T, progress Progress, stage Stage, status StageStatus, review ReviewStatus, hints ...string) {
	t.Helper()
	if progress.CurrentStage != stage || progress.StageStatus != status || progress.ReviewStatus != review {
		t.Fatalf("progress state = %#v, want %s/%s/%s", progress, stage, status, review)
	}
	for _, hint := range hints {
		if !hasControllerHint(progress.CommandHints, hint) {
			t.Fatalf("progress lacks %s: %#v", hint, progress.CommandHints)
		}
	}
}

type fullFlowRuntime struct {
	next                int
	configs             map[int]agentruntime.ThreadConfig
	turns               map[Role]int
	startedRoles        []Role
	firstIntentQuestion string
	intentInputs        []string
}

func (r *fullFlowRuntime) artifactRoot(role Role) string {
	for _, config := range r.configs {
		if roleFromPrompt(config.BootstrapInstructions) == role {
			return config.ArtifactRoot
		}
	}
	return ""
}

func (r *fullFlowRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.next++
	r.configs[r.next] = config.Clone()
	if role := roleFromPrompt(config.BootstrapInstructions); role != "" {
		r.startedRoles = append(r.startedRoles, role)
	}
	return r.next, nil
}

func (r *fullFlowRuntime) RunTurn(thread agentruntime.Thread, input string) (json.RawMessage, error) {
	config := r.configs[thread.(int)]
	role := roleFromPrompt(config.BootstrapInstructions)
	if role == "" {
		return json.RawMessage(`{"feature_id":"complete-flow"}`), nil
	}
	if role == RoleIntentAuthor {
		r.intentInputs = append(r.intentInputs, input)
	}
	r.turns[role]++
	if role == RoleIntentAuthor && r.turns[role] == 1 && r.firstIntentQuestion != "" {
		payload, err := json.Marshal(Envelope{Kind: KindMessage, Message: r.firstIntentQuestion, Decisions: []Decision{}})
		return payload, err
	}
	artifactTurn := r.turns[role]
	if role == RoleIntentAuthor && r.firstIntentQuestion != "" {
		artifactTurn--
	}
	artifact := fullFlowArtifact(role, artifactTurn)
	filename := "review.md"
	if role == RoleIntentAuthor {
		filename = "intent.md"
	} else if role == RoleSpecAuthor {
		filename = "spec.md"
	} else if role == RolePlanAuthor {
		filename = "plan.md"
	}
	if err := os.WriteFile(filepath.Join(config.ArtifactRoot, filename), []byte(artifact), 0o600); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"kind":"artifact","message":"","decisions":[]}`), nil
}

func (r *fullFlowRuntime) CloseThread(agentruntime.Thread) error { return nil }

func roleFromPrompt(prompt string) Role {
	for _, role := range []Role{RoleIntentAuthor, RoleSpecAuthor, RoleSpecReviewer, RolePlanAuthor, RolePlanReviewer} {
		if strings.Contains(prompt, "[roles/"+string(role)+"]") {
			return role
		}
	}
	return ""
}

func fullFlowArtifact(role Role, turn int) string {
	switch role {
	case RoleIntentAuthor:
		if turn == 1 {
			return validIntent("Complete planning flow")
		}
		return validIntent("Revised complete planning flow")
	case RoleSpecAuthor:
		if turn == 1 {
			return validRepositorySpec()
		}
		return strings.Replace(validRepositorySpec(), "The state survives restart.", "The state survives restart and review recheck.", 1)
	case RolePlanAuthor:
		if turn == 1 {
			return validRepositoryPlan()
		}
		return strings.Replace(validRepositoryPlan(), "leaves the plan committed.", "leaves the reviewed plan committed.", 1)
	case RoleSpecReviewer:
		return fullFlowReview("SPEC-F-001", turn > 1)
	case RolePlanReviewer:
		return fullFlowReview("PLAN-F-001", turn > 1)
	default:
		return ""
	}
}

func fullFlowReview(id string, resolved bool) string {
	status := "open"
	resolution := ""
	if resolved {
		status = "resolved"
		resolution = "Resolution: The reviewed document now satisfies the contract.\n"
	}
	return "# Review\n\n## " + id + " — Contract issue\n\n" +
		"Severity: major\nStatus: " + status + "\n" +
		"Problem: The document violates a deterministic document rule.\n" +
		"Location: whole document\nRecommendation: Repair the document structure.\n" + resolution +
		"Decision: fix\nDecided-by: reviewer\nRationale: The document contract requires this correction.\n"
}

func gitOutputForTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
