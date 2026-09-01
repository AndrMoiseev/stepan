package specflow

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFeatureControllerEnforcesCanonicalStageGates(t *testing.T) {
	repository := newControllerRepositoryStub(controllerSnapshot(t, StageIntent, StagePublished, ReviewNotStarted))
	author := &controllerAuthorStub{}
	reviewer := &controllerReviewStub{}
	controller := newFeatureController(repository, author, reviewer)
	controller.now = func() time.Time { return repositoryTestTime }

	if _, err := controller.Open(repository.feature.Target.ID); err != nil {
		t.Fatal(err)
	}
	if progress, err := controller.StartStage(StageSpec, ""); !errors.Is(err, ErrControllerCommandInvalid) || progress.CurrentStage != StageIntent {
		t.Fatalf("premature spec = %#v, %v", progress, err)
	}
	if _, err := controller.StartCurrentStage(""); err != nil {
		t.Fatal(err)
	}

	progress, err := controller.Approve("")
	if err != nil || progress.CurrentStage != StageSpec || progress.StageStatus != StageDrafting {
		t.Fatalf("intent approval = %#v, %v", progress, err)
	}
	if progress.FlowStatus != FlowActive || !reflect.DeepEqual(author.starts, []Stage{StageIntent, StageSpec}) {
		t.Fatalf("after intent: progress=%#v starts=%v", progress, author.starts)
	}
	if progress, err = controller.StartStage(StagePlan, ""); !errors.Is(err, ErrControllerCommandInvalid) || progress.CurrentStage != StageSpec {
		t.Fatalf("premature plan = %#v, %v", progress, err)
	}

	repository.feature = withControllerStage(t, repository.feature, StageSpec, StagePublished, ReviewNotStarted)
	progress, err = controller.Approve("")
	if err != nil || progress.CurrentStage != StagePlan || progress.StageStatus != StageDrafting {
		t.Fatalf("spec approval = %#v, %v", progress, err)
	}
	if !reflect.DeepEqual(author.starts, []Stage{StageIntent, StageSpec, StagePlan}) {
		t.Fatalf("author starts = %v", author.starts)
	}

	repository.feature = withControllerStage(t, repository.feature, StagePlan, StagePublished, ReviewNotStarted)
	progress, err = controller.Approve("")
	if err != nil || progress.FlowStatus != FlowActive || progress.CurrentStage != StagePlan || progress.StageStatus != StageCommitted {
		t.Fatalf("plan approval = %#v, %v", progress, err)
	}
	if len(author.starts) != 3 {
		t.Fatalf("committed plan opened an extra stage: %v", author.starts)
	}
	if repository.approvals != 3 {
		t.Fatalf("approval calls = %d", repository.approvals)
	}
}

func TestFeatureControllerTreatsReviewAsOptionalUntilStarted(t *testing.T) {
	t.Run("not started permits approval", func(t *testing.T) {
		repository := newControllerRepositoryStub(controllerSnapshot(t, StageSpec, StagePublished, ReviewNotStarted))
		controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
		if _, err := controller.Open(repository.feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		if progress, err := controller.Approve(""); err != nil || progress.CurrentStage != StagePlan || repository.approvals != 1 {
			t.Fatalf("optional approval = %#v, %v, calls=%d", progress, err, repository.approvals)
		}
	})

	for _, status := range []ReviewStatus{ReviewRunning, ReviewAwaitingDecisions, ReviewAutomaticRework, ReviewEscalated} {
		t.Run(string(status), func(t *testing.T) {
			repository := newControllerRepositoryStub(controllerSnapshot(t, StageSpec, StagePublished, status))
			controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
			if _, err := controller.Open(repository.feature.Target.ID); err != nil {
				t.Fatal(err)
			}
			progress, err := controller.Approve("")
			if !errors.Is(err, ErrControllerCommandInvalid) || progress.ReviewStatus != status {
				t.Fatalf("approval = %#v, %v", progress, err)
			}
			if repository.approvals != 0 {
				t.Fatalf("blocked review reached phase approval: %d", repository.approvals)
			}
			if hasControllerHint(progress.CommandHints, "/approve") {
				t.Fatalf("blocked review advertised /approve: %#v", progress.CommandHints)
			}
		})
	}

	t.Run("completed permits approval", func(t *testing.T) {
		repository := newControllerRepositoryStub(controllerSnapshot(t, StageSpec, StagePublished, ReviewCompleted))
		controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
		if _, err := controller.Open(repository.feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Approve(""); err != nil || repository.approvals != 1 {
			t.Fatalf("completed review approval = %v, calls=%d", err, repository.approvals)
		}
	})
}

func TestFeatureControllerApprovalPreflightNeverInventsPartialState(t *testing.T) {
	blockers := []ApprovalBlocker{
		{Code: string(DiagnosticOpenQuestionsUnresolved), Path: "spec.md", Message: "open question"},
		{Code: "protected-artifact", Path: "state.json", Message: "state changed"},
		{Code: "outside-feature-change", Path: "outside.txt", Message: "outside dirty"},
	}
	repository := newControllerRepositoryStub(controllerSnapshot(t, StageSpec, StagePublished, ReviewNotStarted))
	repository.approve = func(request ApproveStageRequest) (PhaseCommitResult, error) {
		return PhaseCommitResult{Feature: repository.feature, Blocking: append([]ApprovalBlocker(nil), blockers...)}, nil
	}
	controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
	if _, err := controller.Open(repository.feature.Target.ID); err != nil {
		t.Fatal(err)
	}

	progress, err := controller.Approve("")
	if err != nil || progress.CurrentStage != StageSpec || progress.StageStatus != StagePublished || !reflect.DeepEqual(progress.Blocking, blockers) {
		t.Fatalf("blocked approval = %#v, %v", progress, err)
	}
	if repository.approvals != 1 {
		t.Fatalf("phase operation calls = %d", repository.approvals)
	}
	loadsAfterFirst := repository.loads
	if _, err := controller.Status(); err != nil || repository.loads != loadsAfterFirst+1 {
		t.Fatalf("next command did not reload durable state: err=%v loads=%d", err, repository.loads)
	}
}

func TestFeatureControllerCommitFailureReloadsPublishedSnapshot(t *testing.T) {
	repository := newControllerRepositoryStub(controllerSnapshot(t, StageSpec, StagePublished, ReviewNotStarted))
	repository.approve = func(request ApproveStageRequest) (PhaseCommitResult, error) {
		return PhaseCommitResult{Feature: repository.feature}, fmt.Errorf("%w: hook rejected commit", ErrPhaseCommit)
	}
	controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
	if _, err := controller.Open(repository.feature.Target.ID); err != nil {
		t.Fatal(err)
	}

	progress, err := controller.Approve("")
	if !errors.Is(err, ErrPhaseCommit) || progress.CurrentStage != StageSpec || progress.StageStatus != StagePublished {
		t.Fatalf("failed commit = %#v, %v", progress, err)
	}
	loads := repository.loads
	if _, err := controller.Status(); err != nil || repository.loads != loads+1 {
		t.Fatalf("retry snapshot reload = %v, loads=%d", err, repository.loads)
	}
}

func TestFeatureControllerReturnsContextSpecificProgress(t *testing.T) {
	feature := controllerSnapshot(t, StagePlan, StagePublished, ReviewAwaitingDecisions)
	feature.Documents[StageIntent] = DocumentArtifact{Stage: StageIntent, Path: "intent.md"}
	feature.Documents[StageSpec] = DocumentArtifact{Stage: StageSpec, Path: "spec.md"}
	feature.Documents[StagePlan] = DocumentArtifact{Stage: StagePlan, Path: "plan.md"}
	repository := newControllerRepositoryStub(feature)
	controller := newFeatureController(repository, &controllerAuthorStub{}, &controllerReviewStub{})
	if _, err := controller.Open(feature.Target.ID); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.Status()
	if err != nil {
		t.Fatal(err)
	}
	if !progress.TextAllowed || progress.Path != "plan.md" || len(progress.Documents) != 3 {
		t.Fatalf("progress = %#v", progress)
	}
	want := []string{"/apply", "/revise-spec", "/status", "/exit"}
	got := make([]string, len(progress.CommandHints))
	for i, hint := range progress.CommandHints {
		got[i] = hint.Command
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hints = %v, want %v", got, want)
	}

	before := repository.feature.State.Snapshot()
	unknown, err := controller.Execute(ControllerCommand{Kind: "unknown"})
	if !errors.Is(err, ErrControllerCommandInvalid) || unknown.CurrentStage != StagePlan || !reflect.DeepEqual(repository.feature.State.Snapshot(), before) {
		t.Fatalf("unknown command = %#v, %v", unknown, err)
	}
}

func TestFeatureControllerSessionCloseKeepsPublishedState(t *testing.T) {
	repository := newControllerRepositoryStub(controllerSnapshot(t, StageIntent, StagePublished, ReviewNotStarted))
	author := &controllerAuthorStub{}
	reviewer := &controllerReviewStub{}
	controller := newFeatureController(repository, author, reviewer)
	if _, err := controller.Open(repository.feature.Target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.StartCurrentStage(""); err != nil {
		t.Fatal(err)
	}
	progress, err := controller.Close()
	if err != nil || progress.Event != ControllerSessionClosed || progress.StageStatus != StagePublished {
		t.Fatalf("close = %#v, %v", progress, err)
	}
	if author.closes != 1 || reviewer.closes != 1 {
		t.Fatalf("close calls author=%d reviewer=%d", author.closes, reviewer.closes)
	}
}

type controllerRepositoryStub struct {
	feature         FeatureSnapshot
	loads           int
	approvals       int
	approve         func(ApproveStageRequest) (PhaseCommitResult, error)
	inspectExternal func(ExternalRevisionRequest) (ExternalRevisionResult, error)
	acceptExternal  func(ExternalRevisionRequest) (ExternalRevisionResult, error)
	reviseIntent    func(ReviseIntentRequest) (PhaseCommitResult, error)
	supersedeIntent func(SupersedeIntentRequest) (SupersessionResult, error)
}

func newControllerRepositoryStub(feature FeatureSnapshot) *controllerRepositoryStub {
	repository := &controllerRepositoryStub{feature: feature}
	repository.approve = func(request ApproveStageRequest) (PhaseCommitResult, error) {
		repository.feature = advanceControllerStage(featureClone(repository.feature), request.Stage)
		return PhaseCommitResult{Committed: true, Feature: repository.feature}, nil
	}
	return repository
}

func (r *controllerRepositoryStub) Create(CreateFeatureRequest) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *controllerRepositoryStub) Load(string) (FeatureSnapshot, error) {
	r.loads++
	return featureClone(r.feature), nil
}
func (r *controllerRepositoryStub) Approve(request ApproveStageRequest) (PhaseCommitResult, error) {
	r.approvals++
	return r.approve(request)
}
func (r *controllerRepositoryStub) InspectExternalRevision(request ExternalRevisionRequest) (ExternalRevisionResult, error) {
	if r.inspectExternal != nil {
		return r.inspectExternal(request)
	}
	return ExternalRevisionResult{}, errors.New("unexpected InspectExternalRevision")
}
func (r *controllerRepositoryStub) AcceptExternalRevision(request ExternalRevisionRequest) (ExternalRevisionResult, error) {
	if r.acceptExternal != nil {
		return r.acceptExternal(request)
	}
	return ExternalRevisionResult{}, errors.New("unexpected AcceptExternalRevision")
}
func (r *controllerRepositoryStub) InspectAuthorDraft(DraftArtifactRequest) (DraftInspection, error) {
	return DraftInspection{}, errors.New("unexpected InspectAuthorDraft")
}
func (r *controllerRepositoryStub) PublishAuthorDraft(DraftArtifactRequest) (DraftPublication, error) {
	return DraftPublication{}, errors.New("unexpected PublishAuthorDraft")
}
func (r *controllerRepositoryStub) PublishReview(ReviewArtifactRequest) (ReviewPublication, error) {
	return ReviewPublication{}, errors.New("unexpected PublishReview")
}
func (r *controllerRepositoryStub) RecordDecision(string, Stage, Role, Decision) (FeatureSnapshot, error) {
	return FeatureSnapshot{}, errors.New("unexpected RecordDecision")
}
func (r *controllerRepositoryStub) RecordActivity(string, MemLogEntry) (FeatureSnapshot, error) {
	return FeatureSnapshot{}, errors.New("unexpected RecordActivity")
}
func (r *controllerRepositoryStub) DiscardPending(string, Stage, string) (FeatureSnapshot, error) {
	return r.feature, nil
}
func (r *controllerRepositoryStub) ReviseIntent(request ReviseIntentRequest) (PhaseCommitResult, error) {
	if r.reviseIntent != nil {
		return r.reviseIntent(request)
	}
	return PhaseCommitResult{}, errors.New("unexpected ReviseIntent")
}
func (r *controllerRepositoryStub) SupersedeIntent(request SupersedeIntentRequest) (SupersessionResult, error) {
	if r.supersedeIntent != nil {
		return r.supersedeIntent(request)
	}
	return SupersessionResult{}, errors.New("unexpected SupersedeIntent")
}
func (r *controllerRepositoryStub) InspectChanges(string) (ChangeInspection, error) {
	return r.feature.Changes, nil
}
func (r *controllerRepositoryStub) Recover(string) (RecoveryResult, error) {
	return RecoveryResult{}, nil
}

type controllerAuthorStub struct {
	active bool
	policy StagePolicy
	starts []Stage
	closes int
	submit func(string) (StageResult, error)
	decide func(RevisionAction, string) (StageResult, error)
	reread func() (StageResult, error)
}

func (a *controllerAuthorStub) Policy() (StagePolicy, bool) { return a.policy, a.active }
func (a *controllerAuthorStub) Start(request StartStageRequest) (StagePolicy, error) {
	policy, err := PolicyForStage(request.Stage)
	if err != nil {
		return StagePolicy{}, err
	}
	a.active, a.policy = true, policy
	a.starts = append(a.starts, request.Stage)
	return policy, nil
}
func (a *controllerAuthorStub) SubmitBrief(message string) (StageResult, error) {
	return a.Submit(message)
}
func (a *controllerAuthorStub) Submit(message string) (StageResult, error) {
	if a.submit != nil {
		return a.submit(message)
	}
	return StageResult{}, errors.New("unexpected Submit")
}
func (a *controllerAuthorStub) Decide(action RevisionAction, scope string) (StageResult, error) {
	if a.decide != nil {
		return a.decide(action, scope)
	}
	return StageResult{}, errors.New("unexpected Decide")
}
func (a *controllerAuthorStub) ReReadCurrentDocument() (StageResult, error) {
	if a.reread != nil {
		return a.reread()
	}
	return StageResult{}, errors.New("unexpected ReReadCurrentDocument")
}
func (a *controllerAuthorStub) Close() error {
	a.closes++
	a.active = false
	a.policy = StagePolicy{}
	return nil
}

type controllerReviewStub struct{ closes int }

func (r *controllerReviewStub) Start(StartReviewRequest) (ReviewResult, error) {
	return ReviewResult{}, errors.New("unexpected Start")
}
func (r *controllerReviewStub) Submit(string) (ReviewResult, error) {
	return ReviewResult{}, errors.New("unexpected Submit")
}
func (r *controllerReviewStub) Apply() (ReviewResult, error) {
	return ReviewResult{}, errors.New("unexpected Apply")
}
func (r *controllerReviewStub) Decide(MaterialFindingDecision) (ReviewResult, error) {
	return ReviewResult{}, errors.New("unexpected Decide")
}
func (r *controllerReviewStub) DecideFingerprint(ReviewFingerprintAction) (ReviewResult, error) {
	return ReviewResult{}, errors.New("unexpected DecideFingerprint")
}
func (r *controllerReviewStub) Close() error { r.closes++; return nil }

func controllerSnapshot(t *testing.T, current Stage, status StageStatus, review ReviewStatus) FeatureSnapshot {
	t.Helper()
	snapshot := NewFlowState().Snapshot()
	snapshot.CurrentStage = current
	for _, stage := range stages {
		state := snapshot.Stages[stage]
		switch {
		case stage.order() < current.order():
			state.Status = StageCommitted
			state.CurrentHash = strings.Repeat(string('a'+rune(stage.order())), 64)
			state.ApprovedHash = state.CurrentHash
		case stage == current:
			state.Status = status
			state.ReviewStatus = review
			if status == StagePublished || status == StageCommitted {
				state.CurrentHash = strings.Repeat(string('a'+rune(stage.order())), 64)
			}
			if status == StageCommitted {
				state.ApprovedHash = state.CurrentHash
			}
		default:
			state.Status = StageNotStarted
		}
		snapshot.Stages[stage] = state
	}
	state, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return FeatureSnapshot{Target: FeatureTarget{ID: "2026-08-30-controller"}, State: state, Documents: map[Stage]DocumentArtifact{}}
}

func withControllerStage(t *testing.T, feature FeatureSnapshot, stage Stage, status StageStatus, review ReviewStatus) FeatureSnapshot {
	t.Helper()
	snapshot := feature.State.Snapshot()
	snapshot.CurrentStage = stage
	state := snapshot.Stages[stage]
	state.Status = status
	state.ReviewStatus = review
	if status == StagePublished {
		state.CurrentHash = strings.Repeat(string('a'+rune(stage.order())), 64)
	}
	snapshot.Stages[stage] = state
	updated, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	feature.State = updated
	return feature
}

func advanceControllerStage(feature FeatureSnapshot, stage Stage) FeatureSnapshot {
	snapshot := feature.State.Snapshot()
	state := snapshot.Stages[stage]
	state.Status = StageCommitted
	state.ApprovedHash = state.CurrentHash
	snapshot.Stages[stage] = state
	if next, ok := nextPlanningStage(stage); ok {
		nextState := snapshot.Stages[next]
		nextState.Status = StageDrafting
		snapshot.Stages[next] = nextState
		snapshot.CurrentStage = next
	}
	feature.State, _ = NewFlowStateFromSnapshot(snapshot)
	return feature
}

func featureClone(feature FeatureSnapshot) FeatureSnapshot {
	documents := make(map[Stage]DocumentArtifact, len(feature.Documents))
	for stage, document := range feature.Documents {
		document.Content = append([]byte(nil), document.Content...)
		documents[stage] = document
	}
	feature.Documents = documents
	feature.Reviews = append([]ReviewArtifact(nil), feature.Reviews...)
	feature.Journal = append([]MemLogEntry(nil), feature.Journal...)
	return feature
}

func hasControllerHint(hints []CommandHint, command string) bool {
	for _, hint := range hints {
		if hint.Command == command {
			return true
		}
	}
	return false
}
