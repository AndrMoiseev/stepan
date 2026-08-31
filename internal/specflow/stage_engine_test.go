package specflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestStageEngineFirstAndSubsequentDraftsUseDifferentLifecycle(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-stage-lifecycle"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Author lifecycle", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	runner := &stageRunner{steps: []stageRuntimeStep{
		{artifact: validIntent("First")},
		{artifact: validIntent("Rejected")},
		{artifact: validIntent("Needs rework")},
		{artifact: validIntent("Applied")},
		{artifact: validIntent("Discarded on close")},
	}}
	engine := newTestStageEngine(t, root, runner, repository)
	if _, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: StageIntent}); err != nil {
		t.Fatal(err)
	}

	first, err := engine.Submit("Create the intent")
	if err != nil || first.Outcome != StageDraftPublished {
		t.Fatalf("first draft = %#v, %v", first, err)
	}
	published, _ := repository.Load(featureID)
	if got := string(published.Documents[StageIntent].Content); got != validIntent("First") {
		t.Fatalf("first published bytes = %q", got)
	}

	pending, err := engine.Submit("Revise it")
	if err != nil || pending.Outcome != StageRevisionPending {
		t.Fatalf("pending revision = %#v, %v", pending, err)
	}
	if !strings.Contains(pending.Diff, "--- intent.md") || !strings.Contains(pending.Diff, "+# Rejected") {
		t.Fatalf("pending diff = %q", pending.Diff)
	}
	if !reflect.DeepEqual(pending.RevisionActions, []RevisionAction{RevisionApply, RevisionReject, RevisionRework}) {
		t.Fatalf("revision actions = %#v", pending.RevisionActions)
	}
	stillPublished, _ := repository.Load(featureID)
	if got := string(stillPublished.Documents[StageIntent].Content); got != validIntent("First") {
		t.Fatalf("pending revision changed published bytes: %q", got)
	}
	if rejected, err := engine.Decide(RevisionReject, ""); err != nil || rejected.Outcome != StageRevisionRejected {
		t.Fatalf("reject = %#v, %v", rejected, err)
	}

	if _, err := engine.Submit("Try another revision"); err != nil {
		t.Fatal(err)
	}
	reworked, err := engine.Decide(RevisionRework, "Keep the intent focused on the observable outcome")
	if err != nil || reworked.Outcome != StageRevisionPending {
		t.Fatalf("rework = %#v, %v", reworked, err)
	}
	if runner.threads != 1 {
		t.Fatalf("rework started another author thread: %d", runner.threads)
	}
	applied, err := engine.Decide(RevisionApply, "")
	if err != nil || applied.Outcome != StageRevisionApplied {
		t.Fatalf("apply = %#v, %v", applied, err)
	}
	loaded, _ := repository.Load(featureID)
	if got := string(loaded.Documents[StageIntent].Content); got != validIntent("Applied") {
		t.Fatalf("applied bytes = %q", got)
	}

	if _, err := engine.Submit("Draft one more revision"); err != nil {
		t.Fatal(err)
	}
	artifactRoot := runner.configs[0].ArtifactRoot
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifactRoot); !os.IsNotExist(err) {
		t.Fatalf("artifact root after close: %v", err)
	}
	closed, _ := repository.Load(featureID)
	if got := string(closed.Documents[StageIntent].Content); got != validIntent("Applied") {
		t.Fatalf("close changed published revision: %q", got)
	}
	if !journalContainsKind(closed.Journal, MemLogDiff) {
		t.Fatal("pending revision diff was not retained in mem-log")
	}
}

func TestStageEnginePublishesDeferredQuestionWithoutResolvingIt(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-deferred-question"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Defer an ambiguity", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	deferred := "# Intent\n\n## Scope\n\nShip the explicit part.\n\n## Open questions\n\n- Which retention period should be used?\n"
	runner := &stageRunner{steps: []stageRuntimeStep{{message: "Which retention period should the intent require?"}, {artifact: deferred}}}
	engine := newTestStageEngine(t, root, runner, repository)
	if _, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: StageIntent}); err != nil {
		t.Fatal(err)
	}
	question, err := engine.Submit("Explore the ambiguity first")
	if err != nil || question.Outcome != StageAuthorMessage {
		t.Fatalf("author message = %#v, %v", question, err)
	}
	if _, err := os.Stat(filepath.Join(runner.configs[0].ArtifactRoot, "intent.md")); !os.IsNotExist(err) {
		t.Fatalf("message turn published an artifact placeholder: %v", err)
	}
	result, err := engine.Submit("Explicitly defer the retention question")
	if err != nil || result.Outcome != StageDraftPublished {
		t.Fatalf("deferred draft = %#v, %v", result, err)
	}
	if got := string(result.Feature.Documents[StageIntent].Content); got != deferred {
		t.Fatalf("published deferred draft = %q", got)
	}
	approval := ParseDocument(DocumentRequest{Kind: DocumentIntent, Mode: ValidateApproval, Markdown: deferred})
	if approval.Valid() || !hasDiagnostic(approval, DiagnosticOpenQuestionsUnresolved) {
		t.Fatalf("approval diagnostics = %#v", approval.Diagnostics)
	}
}

func TestStageEngineParserRepairStopsAtThreeAndResetsAfterUserInput(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-parser-repair"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Repair malformed drafts", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	runner := &stageRunner{steps: []stageRuntimeStep{
		{artifact: "# Missing open questions\n"},
		{artifact: "# Still missing open questions\n"},
		{artifact: "# Missing it again\n"},
		{artifact: validIntent("Recovered")},
	}}
	engine := newTestStageEngine(t, root, runner, repository)
	if _, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: StageIntent}); err != nil {
		t.Fatal(err)
	}

	exhausted, err := engine.Submit("Create a draft")
	if err != nil || exhausted.Outcome != StageAuthorDiagnostics {
		t.Fatalf("exhausted repair = %#v, %v", exhausted, err)
	}
	if runner.turns != DefaultRetryLimit || !strings.Contains(exhausted.Message, "missing-open-questions") {
		t.Fatalf("turns=%d diagnostics=%q", runner.turns, exhausted.Message)
	}
	if len(runner.prompts) != DefaultRetryLimit || !strings.Contains(runner.prompts[1], "deterministic structural validation") {
		t.Fatalf("repair prompts = %#v", runner.prompts)
	}

	recovered, err := engine.Submit("I clarified the structure; try again")
	if err != nil || recovered.Outcome != StageDraftPublished {
		t.Fatalf("new user loop = %#v, %v", recovered, err)
	}
	if runner.turns != DefaultRetryLimit+1 {
		t.Fatalf("new user input did not start a fresh loop: %d turns", runner.turns)
	}
}

func TestStageEngineParserRepairRetainsAlreadyReservedIDs(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-parser-ids"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Keep IDs during repair", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	intentRoot := writeRepositoryArtifact(t, root, "intent.md", validIntent("Committed intent"))
	if publication, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageIntent, ArtifactRoot: intentRoot, Mode: ValidateDraft}); err != nil || !publication.Published {
		t.Fatalf("publish intent: %#v, %v", publication, err)
	}
	if approval, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime}); err != nil || !approval.Committed {
		t.Fatalf("approve intent: %#v, %v", approval, err)
	}

	invalid := strings.Replace(validRepositorySpec(), "REQ-001", "REQ-101", -1)
	invalid = strings.Replace(invalid, "\n## Open questions\n", "\n", 1)
	valid := strings.Replace(validRepositorySpec(), "REQ-001", "REQ-101", -1)
	runner := &stageRunner{steps: []stageRuntimeStep{{artifact: invalid}, {artifact: valid}}}
	engine := newTestStageEngine(t, root, runner, repository)
	if _, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: StageSpec}); err != nil {
		t.Fatal(err)
	}
	result, err := engine.Submit("Draft the specification")
	if err != nil || result.Outcome != StageDraftPublished {
		t.Fatalf("repaired spec = %#v, %v", result, err)
	}
	if hasDiagnostic(ParseDocument(DocumentRequest{Kind: DocumentSpec, Mode: ValidateDraft, Markdown: string(result.Feature.Documents[StageSpec].Content)}), DiagnosticReusedID) {
		t.Fatal("ID retained by an automatic repair was treated as reused")
	}
	if !containsStableID(result.Feature.State.IssuedIDs(), mustStableID(t, "REQ-101")) {
		t.Fatalf("observed ID was not reserved: %v", result.Feature.State.IssuedIDs())
	}
}

func TestStageEngineReturnsExternalDocumentReactionToController(t *testing.T) {
	root := t.TempDir()
	featureID := "2026-08-30-external-revision"
	target, err := FeatureTargetForID(root, featureID)
	if err != nil {
		t.Fatal(err)
	}
	repository := &stageRepositoryStub{feature: FeatureSnapshot{
		Target: target, State: NewFlowState(), Documents: map[Stage]DocumentArtifact{
			StageIntent: {Stage: StageIntent, Path: target.IntentPath, Hash: strings.Repeat("a", 64), Content: []byte(validIntent("Original"))},
		},
	}}
	runner := &stageRunner{steps: []stageRuntimeStep{{message: "The intent changed materially; the controller must choose the revision path."}}}
	engine := newTestStageEngine(t, root, runner, repository)
	if _, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: StageIntent}); err != nil {
		t.Fatal(err)
	}
	repository.feature.Documents[StageIntent] = DocumentArtifact{
		Stage: StageIntent, Path: target.IntentPath, Hash: strings.Repeat("b", 64), Content: []byte(validIntent("Externally changed")),
	}
	result, err := engine.ReReadCurrentDocument()
	if err != nil || result.Outcome != StageExternalChangeRead {
		t.Fatalf("external reread = %#v, %v", result, err)
	}
	if runner.threads != 1 || !strings.Contains(runner.prompts[0], target.IntentPath) {
		t.Fatalf("external reread did not use the live author: threads=%d prompt=%q", runner.threads, runner.prompts[0])
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStagePoliciesDriveOneEngineForAllAuthorRoles(t *testing.T) {
	root := t.TempDir()
	featureID := "2026-08-30-policy"
	state := stateWithCommittedStages(t, StageIntent, StageSpec)
	target, err := FeatureTargetForID(root, featureID)
	if err != nil {
		t.Fatal(err)
	}
	repository := &stageRepositoryStub{feature: FeatureSnapshot{
		Target: target, State: state,
		Documents: map[Stage]DocumentArtifact{
			StageIntent: {Stage: StageIntent, Path: target.IntentPath, Hash: strings.Repeat("a", 64), Content: []byte(validIntent("Intent"))},
			StageSpec:   {Stage: StageSpec, Path: target.SpecPath, Hash: strings.Repeat("b", 64), Content: []byte(validRepositorySpec())},
		},
	}}

	for _, test := range []struct {
		stage     Stage
		role      Role
		filename  string
		upstreams []Stage
		review    bool
	}{
		{StageIntent, RoleIntentAuthor, "intent.md", []Stage{}, false},
		{StageSpec, RoleSpecAuthor, "spec.md", []Stage{StageIntent}, true},
		{StagePlan, RolePlanAuthor, "plan.md", []Stage{StageIntent, StageSpec}, true},
	} {
		t.Run(string(test.stage), func(t *testing.T) {
			runner := &stageRunner{}
			engine := newTestStageEngine(t, root, runner, repository)
			policy, err := engine.Start(StartStageRequest{FeatureID: featureID, Stage: test.stage})
			if err != nil {
				t.Fatal(err)
			}
			if policy.AuthorRole != test.role || policy.ArtifactFilename != test.filename || policy.ReviewAvailable != test.review ||
				!slices.Equal(policy.UpstreamStages, test.upstreams) {
				t.Fatalf("policy = %#v", policy)
			}
			if containsString(policy.Commands, "/review") != test.review {
				t.Fatalf("review command in %#v", policy.Commands)
			}
			config := runner.configs[0]
			if config.Workspace != root || config.ArtifactRoot == "" || !strings.Contains(config.BootstrapInstructions, "Fixed artifact filename: "+test.filename) {
				t.Fatalf("thread config = %#v", config)
			}
			for _, upstream := range test.upstreams {
				if !strings.Contains(config.BootstrapInstructions, "- "+string(upstream)+":") {
					t.Fatalf("prompt lacks %s upstream: %s", upstream, config.BootstrapInstructions)
				}
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type stageRuntimeStep struct {
	message  string
	artifact string
	decision []Decision
}

type stageRunner struct {
	steps   []stageRuntimeStep
	configs []agentruntime.ThreadConfig
	prompts []string
	turns   int
	threads int
	closed  int
}

func (r *stageRunner) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.threads++
	r.configs = append(r.configs, config.Clone())
	return r.threads, nil
}

func (r *stageRunner) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	if thread != r.threads || r.turns >= len(r.steps) {
		return nil, fmt.Errorf("unexpected stage turn %d", r.turns+1)
	}
	r.prompts = append(r.prompts, prompt)
	step := r.steps[r.turns]
	r.turns++
	if step.artifact != "" {
		filename := "intent.md"
		bootstrap := r.configs[thread.(int)-1].BootstrapInstructions
		for _, candidate := range []string{"intent.md", "spec.md", "plan.md"} {
			if strings.Contains(bootstrap, "Fixed artifact filename: "+candidate) {
				filename = candidate
				break
			}
		}
		if err := os.WriteFile(filepath.Join(r.configs[thread.(int)-1].ArtifactRoot, filename), []byte(step.artifact), 0o600); err != nil {
			return nil, err
		}
		return json.Marshal(Envelope{Kind: KindArtifact, Message: "", Decisions: nonNilDecisions(step.decision)})
	}
	return json.Marshal(Envelope{Kind: KindMessage, Message: step.message, Decisions: nonNilDecisions(step.decision)})
}

func (r *stageRunner) CloseThread(agentruntime.Thread) error {
	r.closed++
	return nil
}

func nonNilDecisions(value []Decision) []Decision {
	if value == nil {
		return []Decision{}
	}
	return value
}

func newTestStageEngine(t *testing.T, root string, runner dialogueRunner, repository FeatureRepository) *StageEngine {
	t.Helper()
	engine, err := NewStageEngine(root, runner, repository, NewEmbeddedPromptCatalog())
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return repositoryTestTime.Add(10 * time.Minute) }
	return engine
}

func journalContainsKind(entries []MemLogEntry, kind MemLogEventKind) bool {
	for _, entry := range entries {
		if entry.Kind == kind {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func stateWithCommittedStages(t *testing.T, committed ...Stage) FlowState {
	t.Helper()
	snapshot := NewFlowState().Snapshot()
	for _, stage := range committed {
		value := snapshot.Stages[stage]
		value.Status = StageCommitted
		value.CurrentHash = strings.Repeat(string(stage[0]), 64)
		value.ApprovedHash = value.CurrentHash
		snapshot.Stages[stage] = value
	}
	state, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

type stageRepositoryStub struct{ feature FeatureSnapshot }

func (r *stageRepositoryStub) Create(CreateFeatureRequest) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *stageRepositoryStub) Load(string) (FeatureSnapshot, error) { return r.feature, nil }
func (r *stageRepositoryStub) InspectAuthorDraft(DraftArtifactRequest) (DraftInspection, error) {
	return DraftInspection{}, fmt.Errorf("unexpected InspectAuthorDraft")
}
func (r *stageRepositoryStub) PublishAuthorDraft(DraftArtifactRequest) (DraftPublication, error) {
	return DraftPublication{}, fmt.Errorf("unexpected PublishAuthorDraft")
}
func (r *stageRepositoryStub) PublishReview(ReviewArtifactRequest) (ReviewPublication, error) {
	return ReviewPublication{}, fmt.Errorf("unexpected PublishReview")
}
func (r *stageRepositoryStub) RecordDecision(string, Stage, Role, Decision) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *stageRepositoryStub) RecordActivity(string, MemLogEntry) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *stageRepositoryStub) DiscardPending(_ string, _ Stage, artifactRoot string) (FeatureSnapshot, error) {
	return r.feature, removeArtifact(artifactRoot)
}
func (r *stageRepositoryStub) Approve(ApproveStageRequest) (PhaseCommitResult, error) {
	return PhaseCommitResult{}, fmt.Errorf("unexpected Approve")
}
func (r *stageRepositoryStub) ReviseIntent(ReviseIntentRequest) (PhaseCommitResult, error) {
	return PhaseCommitResult{}, fmt.Errorf("unexpected ReviseIntent")
}
func (r *stageRepositoryStub) SupersedeIntent(SupersedeIntentRequest) (SupersessionResult, error) {
	return SupersessionResult{}, fmt.Errorf("unexpected SupersedeIntent")
}
func (r *stageRepositoryStub) InspectChanges(string) (ChangeInspection, error) {
	return ChangeInspection{}, nil
}
func (r *stageRepositoryStub) Recover(string) (RecoveryResult, error) {
	return RecoveryResult{}, nil
}
