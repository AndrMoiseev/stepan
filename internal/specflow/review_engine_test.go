package specflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestReviewEnginePublishesNewReportForEveryExplicitReview(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-completed"
	preparePublishedSpec(t, root, repository, featureID)
	runner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: "# Review\n"}}}
	engine := newTestReviewEngine(t, root, runner, repository)

	result, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || result.Outcome != ReviewReportCompleted || result.Status != ReviewCompleted {
		t.Fatalf("review = %#v, %v", result, err)
	}
	if result.RunID != 1 || result.Path != "reviews/spec-001.md" || runner.threads != 1 || runner.turns != 1 {
		t.Fatalf("review identity/runtime = %#v, threads=%d turns=%d", result, runner.threads, runner.turns)
	}
	loaded, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Reviews) != 1 {
		t.Fatalf("published reviews = %#v", loaded.Reviews)
	}
	report := string(loaded.Reviews[0].Content)
	for _, want := range []string{"review_id: SPEC-REVIEW-001", "status: completed", "provider: \"codex\"", "model: \"gpt-test\"", "accepted_for_revision: " + result.CurrentFingerprint.TargetHash()} {
		if !strings.Contains(report, want) {
			t.Fatalf("report does not contain %q:\n%s", want, report)
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	duplicateRunner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: "# Review\n"}}}
	duplicateEngine := newTestReviewEngine(t, root, duplicateRunner, repository)
	duplicate, err := duplicateEngine.Start(testStartReviewRequest(featureID))
	if err != nil || duplicate.Outcome != ReviewReportCompleted || duplicate.RunID != 2 || duplicate.Path != "reviews/spec-002.md" {
		t.Fatalf("duplicate = %#v, %v", duplicate, err)
	}
	if duplicateRunner.threads != 1 || duplicateRunner.turns != 1 {
		t.Fatalf("explicit review did not start agent: threads=%d turns=%d", duplicateRunner.threads, duplicateRunner.turns)
	}
	stillLoaded, err := repository.Load(featureID)
	if err != nil || len(stillLoaded.Reviews) != 2 {
		t.Fatalf("explicit review reports: %#v, %v", stillLoaded.Reviews, err)
	}
}

func TestReviewEngineReusesReviewerSessionForNextExplicitRun(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-session-reuse"
	preparePublishedSpec(t, root, repository, featureID)
	runner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: "# Review\n"}, {artifact: pendingSpecReview("SPEC-F-001")}}}
	engine := newTestReviewEngine(t, root, runner, repository)

	first, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || first.Outcome != ReviewReportCompleted || first.RunID != 1 {
		t.Fatalf("first review = %#v, %v", first, err)
	}
	changed := strings.Replace(validRepositorySpec(), "The state survives restart.", "The state survives every restart.", 1)
	if err := publishSpecRevision(root, repository, featureID, changed); err != nil {
		t.Fatal(err)
	}
	second, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || second.Outcome != ReviewMaterialDecisions || second.RunID != 2 {
		t.Fatalf("second review = %#v, %v", second, err)
	}
	if runner.threads != 1 || runner.turns != 2 {
		t.Fatalf("reviewer session was not reused: threads=%d turns=%d", runner.threads, runner.turns)
	}
	if len(second.Feature.Reviews) != 2 {
		t.Fatalf("numbered reports = %#v", second.Feature.Reviews)
	}
}

func TestReviewEngineRejectsIntentAndParsesTargetBeforeStartingAgent(t *testing.T) {
	root := initRepository(t)
	target, err := FeatureTargetForID(root, "2026-08-30-review-gates")
	if err != nil {
		t.Fatal(err)
	}
	state := stateWithPublishedStage(t, StageSpec)
	invalid := []byte("# Specification\n\n## REQ-001 — Missing required sections\n")
	feature := FeatureSnapshot{
		Target: target, State: state,
		Documents: map[Stage]DocumentArtifact{
			StageIntent: {Stage: StageIntent, Path: target.IntentPath, Hash: strings.Repeat("a", 64), Content: []byte(validIntent("Intent"))},
			StageSpec:   {Stage: StageSpec, Path: target.SpecPath, Hash: strings.Repeat("b", 64), Content: invalid},
		},
	}
	repository := &reviewRepositoryStub{feature: feature}
	runner := &reviewRunner{}
	engine := newTestReviewEngine(t, root, runner, repository)

	if _, err := engine.Start(StartReviewRequest{FeatureID: target.ID, Stage: StageIntent, Provider: "codex", Model: "gpt-test"}); err == nil || !strings.Contains(err.Error(), "no agent review") {
		t.Fatalf("intent review error = %v", err)
	}
	result, err := engine.Start(testStartReviewRequest(target.ID))
	if err != nil || result.Outcome != ReviewTargetDiagnostics || len(result.Diagnostics) == 0 {
		t.Fatalf("invalid target result = %#v, %v", result, err)
	}
	if runner.threads != 0 || runner.turns != 0 || len(repository.publications) != 0 {
		t.Fatalf("invalid target reached reviewer/repository: runner=%#v publications=%d", runner, len(repository.publications))
	}
}

func TestReviewEngineFingerprintRerunUsesSameNumberedRun(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-rerun"
	before := preparePublishedSpec(t, root, repository, featureID)
	changed := strings.Replace(validRepositorySpec(), "# Specification", "# Revised specification", 1)
	runner := &reviewRunner{steps: []reviewRuntimeStep{
		{artifact: "# Review\n", after: func() error { return publishSpecRevision(root, repository, featureID, changed) }},
		{artifact: "# Review\n"},
	}}
	engine := newTestReviewEngine(t, root, runner, repository)

	drift, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || drift.Outcome != ReviewFingerprintChanged {
		t.Fatalf("drift = %#v, %v", drift, err)
	}
	if drift.RunID != 1 || len(drift.FingerprintActions) != 2 || drift.CurrentFingerprint.TargetHash() == before.Documents[StageSpec].Hash {
		t.Fatalf("drift metadata = %#v", drift)
	}
	intermediate, err := repository.Load(featureID)
	intermediateState, _ := intermediate.State.Stage(StageSpec)
	if err != nil || len(intermediate.Reviews) != 1 || len(intermediateState.Reviews) != 1 ||
		intermediateState.Reviews[0].Status != ReviewRunning || intermediateState.Reviews[0].AcceptedFingerprint != nil {
		t.Fatalf("drift did not keep one live unaccepted report: %#v, %v", intermediateState.Reviews, err)
	}

	completed, err := engine.DecideFingerprint(ReviewFingerprintRerun)
	if err != nil || completed.Outcome != ReviewReportCompleted || completed.RunID != drift.RunID {
		t.Fatalf("rerun = %#v, %v", completed, err)
	}
	if runner.threads != 1 || runner.turns != 2 {
		t.Fatalf("rerun did not reuse reviewer: threads=%d turns=%d", runner.threads, runner.turns)
	}
	stageState, _ := completed.Feature.State.Stage(StageSpec)
	if len(stageState.Reviews) != 1 || !stageState.Reviews[0].OriginalFingerprint.Equal(drift.OriginalFingerprint) ||
		stageState.Reviews[0].AcceptedFingerprint == nil || !stageState.Reviews[0].AcceptedFingerprint.Equal(completed.CurrentFingerprint) {
		t.Fatalf("rerun fingerprints = %#v", stageState.Reviews)
	}
	if stageState.Reviews[0].OriginalFingerprint.Equal(*stageState.Reviews[0].AcceptedFingerprint) {
		t.Fatal("rerun lost the distinct started and accepted fingerprints")
	}
}

func TestReviewEngineAcceptsChangedFingerprintAndLogsBothRevisions(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-accept"
	preparePublishedSpec(t, root, repository, featureID)
	changed := strings.Replace(validRepositorySpec(), "Use a durable repository.", "Use one durable repository.", 1)
	runner := &reviewRunner{steps: []reviewRuntimeStep{{
		artifact: pendingSpecReview("SPEC-F-001"),
		after:    func() error { return publishSpecRevision(root, repository, featureID, changed) },
	}}}
	engine := newTestReviewEngine(t, root, runner, repository)

	drift, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || drift.Outcome != ReviewFingerprintChanged {
		t.Fatalf("drift = %#v, %v", drift, err)
	}
	accepted, err := engine.DecideFingerprint(ReviewFingerprintAccept)
	if err != nil || accepted.Outcome != ReviewMaterialDecisions || accepted.Status != ReviewAwaitingDecisions {
		t.Fatalf("accept = %#v, %v", accepted, err)
	}
	if len(accepted.MaterialFindings) != 1 || accepted.MaterialFindings[0].ID.String() != "SPEC-F-1" {
		t.Fatalf("material findings = %#v", accepted.MaterialFindings)
	}
	stageState, _ := accepted.Feature.State.Stage(StageSpec)
	if len(stageState.Reviews) != 1 || stageState.Reviews[0].AcceptedFingerprint == nil ||
		!stageState.Reviews[0].AcceptedFingerprint.Equal(accepted.CurrentFingerprint) {
		t.Fatalf("accepted state = %#v", stageState.Reviews)
	}
	last := accepted.Feature.Journal[len(accepted.Feature.Journal)-1]
	if last.Kind != MemLogReview || !strings.Contains(last.Body, "started_revision="+drift.OriginalFingerprint.TargetHash()) ||
		!strings.Contains(last.Body, "accepted_for_revision="+accepted.CurrentFingerprint.TargetHash()) {
		t.Fatalf("accepted fingerprint journal = %#v", last)
	}
}

func TestReviewEngineSeparatesContractFixesFromMaterialDecisions(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-contract"
	preparePublishedSpec(t, root, repository, featureID)
	runner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: contractSpecReview("SPEC-F-001")}}}
	engine := newTestReviewEngine(t, root, runner, repository)

	result, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || result.Outcome != ReviewReworkRequired || result.Status != ReviewAutomaticRework {
		t.Fatalf("contract review = %#v, %v", result, err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Kind != FindingContractViolation || len(result.MaterialFindings) != 0 ||
		result.Findings[0].Decision.DecidedBy != DecidedByReviewer || result.Findings[0].Decision.Decision != DecisionFix {
		t.Fatalf("contract finding classification = %#v", result.Findings)
	}
}

func TestReviewEngineRepairsChangedFindingIdentityAndRetainsCompleteHistory(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-findings"
	feature := preparePublishedSpec(t, root, repository, featureID)
	fingerprint, err := NewFingerprint(feature.Documents[StageSpec].Hash, []UpstreamHash{{Stage: StageIntent, Hash: feature.Documents[StageIntent].Hash}})
	if err != nil {
		t.Fatal(err)
	}
	firstBody := resolvedSpecReview("SPEC-F-001", "Original problem")
	firstRoot := writeRepositoryArtifact(t, root, "review.md", firstBody)
	first, err := repository.PublishReview(ReviewArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: firstRoot, Status: ReviewCompleted,
		Original: fingerprint, Accepted: &fingerprint, Attempts: 1, ActiveIDs: []StableID{mustStableID(t, "REQ-1")},
		Provider: "codex", Model: "gpt-test", CreatedAt: repositoryTestTime, UpdatedAt: repositoryTestTime,
	})
	if err != nil || !first.Published {
		t.Fatalf("first review = %#v, %v", first, err)
	}
	changedSpec := strings.Replace(validRepositorySpec(), "The flow persists its state.", "The flow durably persists its state.", 1)
	if err := publishSpecRevision(root, repository, featureID, changedSpec); err != nil {
		t.Fatal(err)
	}

	invalid := resolvedSpecReview("SPEC-F-001", "Materially changed problem")
	repaired := supersededSpecReview()
	runner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: invalid}, {artifact: repaired}}}
	engine := newTestReviewEngine(t, root, runner, repository)
	result, err := engine.Start(testStartReviewRequest(featureID))
	if err != nil || result.Outcome != ReviewMaterialDecisions || result.RunID != 2 {
		t.Fatalf("second review = %#v, %v", result, err)
	}
	if runner.turns != 2 {
		t.Fatalf("immutable finding violation did not cause parser repair: turns=%d", runner.turns)
	}
	if len(result.Findings) != 2 || result.Findings[0].ID.String() != "SPEC-F-1" ||
		result.Findings[0].Status != FindingSuperseded || result.Findings[0].SupersededBy.String() != "SPEC-F-2" ||
		result.Findings[1].ID.String() != "SPEC-F-2" {
		t.Fatalf("finding identity/history = %#v", result.Findings)
	}
}

func TestReviewEngineCrashLeavesPreReviewStateAndDoesNotResume(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-crash"
	before := preparePublishedSpec(t, root, repository, featureID)
	runner := &reviewRunner{steps: []reviewRuntimeStep{{err: errors.New("runtime exited")}}}
	engine := newTestReviewEngine(t, root, runner, repository)

	if _, err := engine.Start(testStartReviewRequest(featureID)); err == nil || !strings.Contains(err.Error(), "runtime exited") {
		t.Fatalf("crash error = %v", err)
	}
	after, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	beforeState, afterState := before.State.Snapshot(), after.State.Snapshot()
	if !reflect.DeepEqual(beforeState, afterState) || len(after.Reviews) != 0 {
		t.Fatalf("crash mutated durable review state: before=%#v after=%#v", beforeState, afterState)
	}
	if runner.closed != 1 {
		t.Fatalf("crashed reviewer thread was not closed: %d", runner.closed)
	}
	if _, err := engine.Submit("resume"); !errors.Is(err, ErrReviewNotStarted) {
		t.Fatalf("crashed review resumed: %v", err)
	}
}

type reviewRuntimeStep struct {
	artifact string
	message  string
	after    func() error
	err      error
}

type reviewRunner struct {
	steps   []reviewRuntimeStep
	configs []agentruntime.ThreadConfig
	prompts []string
	threads int
	turns   int
	closed  int
}

func (r *reviewRunner) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.threads++
	r.configs = append(r.configs, config.Clone())
	return r.threads, nil
}

func (r *reviewRunner) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	if thread != r.threads || r.turns >= len(r.steps) {
		return nil, fmt.Errorf("unexpected review turn %d", r.turns+1)
	}
	r.prompts = append(r.prompts, prompt)
	step := r.steps[r.turns]
	r.turns++
	if step.err != nil {
		return nil, step.err
	}
	if step.artifact != "" {
		if err := os.WriteFile(filepath.Join(r.configs[thread.(int)-1].ArtifactRoot, "review.md"), []byte(step.artifact), 0o600); err != nil {
			return nil, err
		}
		if step.after != nil {
			if err := step.after(); err != nil {
				return nil, err
			}
		}
		return json.Marshal(Envelope{Kind: KindArtifact, Message: "", Decisions: []Decision{}})
	}
	if step.after != nil {
		if err := step.after(); err != nil {
			return nil, err
		}
	}
	return json.Marshal(Envelope{Kind: KindMessage, Message: step.message, Decisions: []Decision{}})
}

func (r *reviewRunner) CloseThread(agentruntime.Thread) error {
	r.closed++
	return nil
}

func newTestReviewEngine(t *testing.T, root string, runner dialogueRunner, repository FeatureRepository) *ReviewEngine {
	t.Helper()
	engine, err := NewReviewEngine(root, runner, repository, NewEmbeddedPromptCatalog())
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return repositoryTestTime.Add(10 * time.Minute) }
	return engine
}

func testStartReviewRequest(featureID string) StartReviewRequest {
	return StartReviewRequest{FeatureID: featureID, Stage: StageSpec, Provider: "codex", Model: "gpt-test"}
}

func preparePublishedSpec(t *testing.T, root string, repository *FSFeatureRepository, featureID string) FeatureSnapshot {
	t.Helper()
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Review the spec", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	intentRoot := writeRepositoryArtifact(t, root, "intent.md", validIntent("Review intent"))
	intent, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageIntent, ArtifactRoot: intentRoot, Mode: ValidateDraft})
	if err != nil || !intent.Published {
		t.Fatalf("publish intent = %#v, %v", intent, err)
	}
	approved, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !approved.Committed {
		t.Fatalf("approve intent = %#v, %v", approved, err)
	}
	specRoot := writeRepositoryArtifact(t, root, "spec.md", validRepositorySpec())
	spec, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: specRoot, Mode: ValidateDraft,
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: approved.Feature.Documents[StageIntent].Hash}},
	})
	if err != nil || !spec.Published {
		t.Fatalf("publish spec = %#v, %v", spec, err)
	}
	return spec.Feature
}

func publishSpecRevision(root string, repository *FSFeatureRepository, featureID, markdown string) error {
	feature, err := repository.Load(featureID)
	if err != nil {
		return err
	}
	artifactRoot, err := CreateArtifactRoot(root)
	if err != nil {
		return err
	}
	defer removeArtifact(artifactRoot)
	if err := os.WriteFile(filepath.Join(artifactRoot, "spec.md"), []byte(markdown), 0o600); err != nil {
		return err
	}
	current := feature.Documents[StageSpec]
	parsed := ParseDocument(DocumentRequest{Kind: DocumentSpec, Mode: ValidateDraft, Markdown: string(current.Content)})
	publication, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: artifactRoot, Mode: ValidateDraft,
		ActiveIDs: activeDocumentIDs(feature, []Stage{StageIntent}), RetainedIDs: observedStableIDs(parsed.ObservedIDs),
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: feature.Documents[StageIntent].Hash}},
	})
	if err != nil {
		return err
	}
	if !publication.Published {
		return fmt.Errorf("revised spec invalid: %#v", publication.Validation.Diagnostics)
	}
	return nil
}

func stateWithPublishedStage(t *testing.T, stage Stage) FlowState {
	t.Helper()
	snapshot := NewFlowState().Snapshot()
	value := snapshot.Stages[stage]
	value.Status = StagePublished
	value.CurrentHash = strings.Repeat(string(stage[0]), 64)
	snapshot.Stages[stage] = value
	state, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func resolvedSpecReview(id, problem string) string {
	return fmt.Sprintf(`# Review

## %s — Resolved material issue

Severity: major
Status: resolved
Problem: %s
Location: whole document
Recommendation: Clarify the behavior.
Resolution: The document now clarifies the behavior.
Decision: fix
Decided-by: user
Rationale: The clarification is required.
`, id, problem)
}

func contractSpecReview(id string) string {
	return fmt.Sprintf(`# Review

## %s — Contract violation

Severity: major
Status: open
Problem: The specification violates a deterministic document rule.
Location: whole document
Recommendation: Repair the document structure.
Decision: fix
Decided-by: reviewer
Rationale: The document contract requires this correction.
`, id)
}

func supersededSpecReview() string {
	return `# Review

## SPEC-F-001 — Superseded issue

Severity: major
Status: superseded
Problem: Original problem
Location: whole document
Recommendation: Clarify the behavior.
Superseded-by: SPEC-F-002
Decision: fix
Decided-by: user
Rationale: The clarification is required.

## SPEC-F-002 — Changed material issue

Severity: major
Status: open
Problem: Materially changed problem
Location: whole document
Recommendation: Clarify the changed behavior.
Decision: pending
Decided-by: none
Rationale:
`
}

type reviewRepositoryStub struct {
	feature      FeatureSnapshot
	publications []ReviewArtifactRequest
}

func (r *reviewRepositoryStub) Create(CreateFeatureRequest) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *reviewRepositoryStub) Load(string) (FeatureSnapshot, error) { return r.feature, nil }
func (r *reviewRepositoryStub) InspectAuthorDraft(DraftArtifactRequest) (DraftInspection, error) {
	return DraftInspection{}, fmt.Errorf("unexpected InspectAuthorDraft")
}
func (r *reviewRepositoryStub) PublishAuthorDraft(DraftArtifactRequest) (DraftPublication, error) {
	return DraftPublication{}, fmt.Errorf("unexpected PublishAuthorDraft")
}
func (r *reviewRepositoryStub) PublishReview(request ReviewArtifactRequest) (ReviewPublication, error) {
	r.publications = append(r.publications, request)
	return ReviewPublication{}, fmt.Errorf("unexpected PublishReview")
}
func (r *reviewRepositoryStub) RecordDecision(string, Stage, Role, Decision) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *reviewRepositoryStub) RecordActivity(string, MemLogEntry) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *reviewRepositoryStub) Checkpoint(CheckpointRequest) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *reviewRepositoryStub) DiscardPending(string, Stage, string) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *reviewRepositoryStub) Approve(ApproveStageRequest) (PhaseCommitResult, error) {
	return PhaseCommitResult{}, fmt.Errorf("unexpected Approve")
}
func (r *reviewRepositoryStub) ReviseIntent(ReviseIntentRequest) (PhaseCommitResult, error) {
	return PhaseCommitResult{}, fmt.Errorf("unexpected ReviseIntent")
}
func (r *reviewRepositoryStub) InspectExternalRevision(ExternalRevisionRequest) (ExternalRevisionResult, error) {
	return ExternalRevisionResult{}, fmt.Errorf("unexpected InspectExternalRevision")
}
func (r *reviewRepositoryStub) AcceptExternalRevision(ExternalRevisionRequest) (ExternalRevisionResult, error) {
	return ExternalRevisionResult{}, fmt.Errorf("unexpected AcceptExternalRevision")
}
func (r *reviewRepositoryStub) SupersedeIntent(SupersedeIntentRequest) (SupersessionResult, error) {
	return SupersessionResult{}, fmt.Errorf("unexpected SupersedeIntent")
}
func (r *reviewRepositoryStub) InspectChanges(string) (ChangeInspection, error) {
	return ChangeInspection{}, nil
}
func (r *reviewRepositoryStub) Recover(string) (RecoveryResult, error) {
	return RecoveryResult{}, nil
}
