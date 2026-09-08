package specflow

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFeatureControllerReviseSpecDistinguishesUnchangedAndChangedDocument(t *testing.T) {
	t.Run("unchanged returns to plan without commit", func(t *testing.T) {
		feature := controllerSnapshot(t, StagePlan, StageCommitted, ReviewNotStarted)
		repository := newControllerRepositoryStub(feature)
		author := &controllerAuthorStub{}
		controller := newFeatureController(repository, author, &controllerReviewStub{})
		if _, err := controller.Open(feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		if progress, err := controller.ReviseSpec(""); err != nil || progress.Event != ControllerAuthorStarted || author.policy.Stage != StageSpec {
			t.Fatalf("revise spec = %#v, %v", progress, err)
		}
		progress, err := controller.Approve("")
		if err != nil || progress.CurrentStage != StagePlan || repository.approvals != 0 {
			t.Fatalf("unchanged approval = %#v, %v, commits=%d", progress, err, repository.approvals)
		}
	})

	t.Run("changed spec is reapproved and makes plan outdated", func(t *testing.T) {
		feature := controllerSnapshot(t, StagePlan, StageCommitted, ReviewNotStarted)
		repository := newControllerRepositoryStub(feature)
		author := &controllerAuthorStub{}
		controller := newFeatureController(repository, author, &controllerReviewStub{})
		if _, err := controller.Open(feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.ReviseSpec(""); err != nil {
			t.Fatal(err)
		}
		author.submit = func(string) (StageResult, error) {
			return StageResult{Stage: StageSpec, Outcome: StageRevisionPending, RevisionActions: []RevisionAction{RevisionApply, RevisionReject, RevisionRework}, Feature: repository.feature}, nil
		}
		if progress, err := controller.AuthorMessage("change the spec"); err != nil || len(progress.Revision) == 0 {
			t.Fatalf("pending revision = %#v, %v", progress, err)
		}
		author.decide = func(action RevisionAction, _ string) (StageResult, error) {
			if action != RevisionApply {
				t.Fatalf("action = %s", action)
			}
			repository.feature = withControllerDocumentRevision(t, repository.feature, StageSpec, strings.Repeat("f", 64))
			return StageResult{Stage: StageSpec, Outcome: StageRevisionApplied, Feature: repository.feature}, nil
		}
		repository.acceptExternal = func(request ExternalRevisionRequest) (ExternalRevisionResult, error) {
			if request.Stage != StageSpec {
				t.Fatalf("accepted stage = %s", request.Stage)
			}
			repository.feature = withControllerCurrentStage(t, repository.feature, StageSpec)
			return ExternalRevisionResult{Accepted: true, Feature: repository.feature}, nil
		}
		progress, err := controller.RevisionDecision(RevisionApply, "")
		if err != nil || progress.CurrentStage != StageSpec || progress.StageStatus != StagePublished {
			t.Fatalf("activate changed spec = %#v, %v", progress, err)
		}
		repository.approve = func(request ApproveStageRequest) (PhaseCommitResult, error) {
			updated := featureClone(repository.feature)
			snapshot := updated.State.Snapshot()
			spec := snapshot.Stages[StageSpec]
			spec.Status, spec.ApprovedHash = StageCommitted, spec.CurrentHash
			snapshot.Stages[StageSpec] = spec
			plan := snapshot.Stages[StagePlan]
			plan.Status, plan.Outdated = StagePublished, true
			snapshot.Stages[StagePlan] = plan
			snapshot.CurrentStage = StagePlan
			updated.State, _ = NewFlowStateFromSnapshot(snapshot)
			repository.feature = updated
			return PhaseCommitResult{Committed: true, Feature: updated}, nil
		}
		progress, err = controller.Approve("")
		plan, _ := repository.feature.State.Stage(StagePlan)
		if err != nil || progress.CurrentStage != StagePlan || progress.StageStatus != StagePublished || !plan.Outdated || plan.ApprovedHash == "" {
			t.Fatalf("reapproved spec = %#v, plan=%#v, err=%v", progress, plan, err)
		}
	})
}

func TestRepositoryAppliesExternalSpecAndPlanLifecycle(t *testing.T) {
	t.Run("plan approval is removed without touching upstream", func(t *testing.T) {
		root := initializedCommitRepository(t)
		repository, featureID, committed := committedPlanningFeature(t, root, "external-plan")
		intent, _ := committed.State.Stage(StageIntent)
		spec, _ := committed.State.Stage(StageSpec)
		planBefore, _ := committed.State.Stage(StagePlan)
		changed := strings.Replace(validRepositoryPlan(), "canonical flow", "externally revised canonical flow", 1)
		if err := os.WriteFile(committed.Target.PlanPath, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
		inspected, err := repository.InspectExternalRevision(ExternalRevisionRequest{FeatureID: featureID, Stage: StagePlan, At: repositoryTestTime.Add(6 * time.Minute)})
		if err != nil || !inspected.Validation.Valid() || inspected.Accepted {
			t.Fatalf("inspect plan = %#v, %v", inspected, err)
		}
		accepted, err := repository.AcceptExternalRevision(ExternalRevisionRequest{FeatureID: featureID, Stage: StagePlan, At: repositoryTestTime.Add(7 * time.Minute)})
		plan, _ := accepted.Feature.State.Stage(StagePlan)
		gotIntent, _ := accepted.Feature.State.Stage(StageIntent)
		gotSpec, _ := accepted.Feature.State.Stage(StageSpec)
		if err != nil || !accepted.Accepted || plan.Status != StagePublished || plan.CurrentHash == planBefore.CurrentHash || plan.ApprovedHash != planBefore.ApprovedHash {
			t.Fatalf("accept plan = %#v, state=%#v, %v", accepted, plan, err)
		}
		if gotIntent.ApprovedHash != intent.ApprovedHash || gotSpec.ApprovedHash != spec.ApprovedHash {
			t.Fatalf("upstream approvals changed: intent=%#v spec=%#v", gotIntent, gotSpec)
		}
	})

	t.Run("reapproved spec marks historical plan outdated", func(t *testing.T) {
		root := initializedCommitRepository(t)
		repository, featureID, committed := committedPlanningFeature(t, root, "external-spec")
		planBefore, _ := committed.State.Stage(StagePlan)
		changed := strings.Replace(validRepositorySpec(), "The flow persists its state.", "The externally revised flow persists its state and audit trail.", 1)
		if err := os.WriteFile(committed.Target.SpecPath, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.InspectExternalRevision(ExternalRevisionRequest{FeatureID: featureID, Stage: StageSpec, At: repositoryTestTime.Add(6 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		accepted, err := repository.AcceptExternalRevision(ExternalRevisionRequest{FeatureID: featureID, Stage: StageSpec, At: repositoryTestTime.Add(7 * time.Minute)})
		if err != nil || !accepted.Accepted || accepted.Feature.State.CurrentStage() != StageSpec {
			t.Fatalf("accept spec = %#v, %v", accepted, err)
		}
		approved, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageSpec, At: repositoryTestTime.Add(8 * time.Minute)})
		plan, _ := approved.Feature.State.Stage(StagePlan)
		if err != nil || !approved.Committed || plan.Status != StagePublished || !plan.Outdated || plan.ApprovedHash != planBefore.ApprovedHash {
			t.Fatalf("reapprove spec = %#v, plan=%#v, %v", approved, plan, err)
		}
	})
}

func committedPlanningFeature(t *testing.T, root, suffix string) (*FSFeatureRepository, string, FeatureSnapshot) {
	t.Helper()
	repository, featureID := publishedIntentFeature(t, root, suffix, "External revisions")
	intentApproval, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !intentApproval.Committed {
		t.Fatalf("approve intent: %#v, %v", intentApproval, err)
	}
	intent := intentApproval.Feature.Documents[StageIntent]
	specRoot := writeRepositoryArtifact(t, root, "spec.md", validRepositorySpec())
	specPublication, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: specRoot, Mode: ValidateDraft,
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: intent.Hash}}, At: repositoryTestTime.Add(2 * time.Minute),
	})
	if err != nil || !specPublication.Published {
		t.Fatalf("publish spec: %#v, %v", specPublication, err)
	}
	specApproval, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageSpec, At: repositoryTestTime.Add(3 * time.Minute)})
	if err != nil || !specApproval.Committed {
		t.Fatalf("approve spec: %#v, %v", specApproval, err)
	}
	planRoot := writeRepositoryArtifact(t, root, "plan.md", validRepositoryPlan())
	planPublication, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StagePlan, ArtifactRoot: planRoot, Mode: ValidateDraft,
		ActiveIDs:      activeDocumentIDs(specApproval.Feature, []Stage{StageIntent, StageSpec}),
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: specApproval.Feature.Documents[StageIntent].Hash}, {Stage: StageSpec, Hash: specApproval.Feature.Documents[StageSpec].Hash}},
		At:             repositoryTestTime.Add(4 * time.Minute),
	})
	if err != nil || !planPublication.Published {
		t.Fatalf("publish plan: %#v, %v", planPublication, err)
	}
	planApproval, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StagePlan, At: repositoryTestTime.Add(5 * time.Minute)})
	if err != nil || !planApproval.Committed {
		t.Fatalf("approve plan: %#v, %v", planApproval, err)
	}
	return repository, featureID, planApproval.Feature
}

func TestFeatureControllerRequiresUserClassificationForCommittedIntentRevision(t *testing.T) {
	for _, classification := range []IntentRevisionClassification{IntentRevisionNonMaterial, IntentRevisionMaterial} {
		t.Run(string(classification), func(t *testing.T) {
			feature := controllerSnapshot(t, StagePlan, StageCommitted, ReviewNotStarted)
			changeHash := strings.Repeat("9", 64)
			feature.Changes.DocumentRevisions = []ArtifactChange{{Class: ChangeDocumentRevision, Stage: StageIntent, ActualHash: changeHash}}
			repository := newControllerRepositoryStub(feature)
			author := &controllerAuthorStub{reread: func() (StageResult, error) {
				return StageResult{Stage: StageIntent, Outcome: StageExternalChangeRead, Message: "intent re-read"}, nil
			}}
			repository.inspectExternal = func(request ExternalRevisionRequest) (ExternalRevisionResult, error) {
				return ExternalRevisionResult{Validation: DocumentResult{}, Feature: repository.feature}, nil
			}
			controller := newFeatureController(repository, author, &controllerReviewStub{})
			if _, err := controller.Open(feature.Target.ID); err != nil {
				t.Fatal(err)
			}
			progress, err := controller.Status()
			if err != nil || progress.Event != ControllerExternalRead || progress.TextAllowed || !hasControllerHint(progress.CommandHints, "/material") {
				t.Fatalf("classification prompt = %#v, %v", progress, err)
			}
			if _, err := controller.AuthorMessage("guess for me"); !errors.Is(err, ErrControllerCommandInvalid) {
				t.Fatalf("controller classified intent itself: %v", err)
			}

			if classification == IntentRevisionNonMaterial {
				downstreamSpec, _ := repository.feature.State.Stage(StageSpec)
				downstreamPlan, _ := repository.feature.State.Stage(StagePlan)
				repository.reviseIntent = func(ReviseIntentRequest) (PhaseCommitResult, error) {
					updated := featureClone(repository.feature)
					snapshot := updated.State.Snapshot()
					intent := snapshot.Stages[StageIntent]
					intent.CurrentHash, intent.ApprovedHash = changeHash, changeHash
					snapshot.Stages[StageIntent] = intent
					updated.State, _ = NewFlowStateFromSnapshot(snapshot)
					updated.Changes = ChangeInspection{}
					repository.feature = updated
					return PhaseCommitResult{Committed: true, Feature: updated}, nil
				}
				progress, err = controller.ClassifyIntentRevision(classification, "", "")
				spec, _ := repository.feature.State.Stage(StageSpec)
				plan, _ := repository.feature.State.Stage(StagePlan)
				if err != nil || progress.Event != ControllerExternalApplied || spec.ApprovedHash != downstreamSpec.ApprovedHash || plan.ApprovedHash != downstreamPlan.ApprovedHash {
					t.Fatalf("non-material result = %#v, spec=%#v plan=%#v err=%v", progress, spec, plan, err)
				}
				return
			}

			newID := "2026-08-30-material-successor"
			repository.supersedeIntent = func(request SupersedeIntentRequest) (SupersessionResult, error) {
				if request.NewFeatureID != newID {
					t.Fatalf("new feature = %q", request.NewFeatureID)
				}
				old := featureClone(repository.feature)
				oldSnapshot := old.State.Snapshot()
				oldSnapshot.FlowStatus = FlowSuperseded
				oldSnapshot.Supersession.SupersededBy = newID
				old.State, _ = NewFlowStateFromSnapshot(oldSnapshot)
				created := controllerSnapshot(t, StageIntent, StagePublished, ReviewNotStarted)
				created.Target.ID = newID
				newSnapshot := created.State.Snapshot()
				newSnapshot.Supersession.Supersedes = feature.Target.ID
				created.State, _ = NewFlowStateFromSnapshot(newSnapshot)
				repository.feature = created
				return SupersessionResult{Committed: true, Old: old, New: created}, nil
			}
			progress, err = controller.ClassifyIntentRevision(classification, newID, "")
			if err != nil || progress.Event != ControllerSuperseded || progress.FeatureID != newID || author.policy.Stage != StageIntent {
				t.Fatalf("material result = %#v, author=%#v err=%v", progress, author.policy, err)
			}
		})
	}
}

func TestFeatureControllerExternalPlanRevisionAndProtectedArtifacts(t *testing.T) {
	t.Run("plan returns to published without changing upstream approvals", func(t *testing.T) {
		feature := controllerSnapshot(t, StagePlan, StageCommitted, ReviewNotStarted)
		intent, _ := feature.State.Stage(StageIntent)
		spec, _ := feature.State.Stage(StageSpec)
		feature.Changes.DocumentRevisions = []ArtifactChange{{Class: ChangeDocumentRevision, Stage: StagePlan, ActualHash: strings.Repeat("8", 64)}}
		repository := newControllerRepositoryStub(feature)
		author := &controllerAuthorStub{reread: func() (StageResult, error) {
			return StageResult{Stage: StagePlan, Outcome: StageExternalChangeRead}, nil
		}}
		repository.inspectExternal = func(ExternalRevisionRequest) (ExternalRevisionResult, error) {
			return ExternalRevisionResult{Validation: DocumentResult{}, Feature: repository.feature}, nil
		}
		repository.acceptExternal = func(ExternalRevisionRequest) (ExternalRevisionResult, error) {
			repository.feature = withControllerDocumentRevision(t, repository.feature, StagePlan, strings.Repeat("8", 64))
			repository.feature.Changes = ChangeInspection{}
			return ExternalRevisionResult{Accepted: true, Feature: repository.feature}, nil
		}
		controller := newFeatureController(repository, author, &controllerReviewStub{})
		if _, err := controller.Open(feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		progress, err := controller.Status()
		gotIntent, _ := repository.feature.State.Stage(StageIntent)
		gotSpec, _ := repository.feature.State.Stage(StageSpec)
		if err != nil || progress.StageStatus != StagePublished || gotIntent.ApprovedHash != intent.ApprovedHash || gotSpec.ApprovedHash != spec.ApprovedHash {
			t.Fatalf("plan revision = %#v, intent=%#v spec=%#v err=%v", progress, gotIntent, gotSpec, err)
		}
	})

	t.Run("service-owned artifact remains blocking", func(t *testing.T) {
		feature := controllerSnapshot(t, StagePlan, StagePublished, ReviewNotStarted)
		feature.Changes.Blocking = []ArtifactChange{{Class: ChangeProtectedArtifact, Path: "state.json", Message: "protected state changed"}}
		repository := newControllerRepositoryStub(feature)
		author := &controllerAuthorStub{}
		controller := newFeatureController(repository, author, &controllerReviewStub{})
		if _, err := controller.Open(feature.Target.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := controller.Status(); !errors.Is(err, ErrRepositoryBlocked) || len(author.starts) != 0 {
			t.Fatalf("protected change = %v, starts=%v", err, author.starts)
		}
	})
}

func withControllerCurrentStage(t *testing.T, feature FeatureSnapshot, stage Stage) FeatureSnapshot {
	t.Helper()
	snapshot := feature.State.Snapshot()
	snapshot.CurrentStage = stage
	feature.State, _ = NewFlowStateFromSnapshot(snapshot)
	feature.Changes = ChangeInspection{}
	return feature
}

func withControllerDocumentRevision(t *testing.T, feature FeatureSnapshot, stage Stage, currentHash string) FeatureSnapshot {
	t.Helper()
	snapshot := feature.State.Snapshot()
	state := snapshot.Stages[stage]
	state.Status = StagePublished
	state.CurrentHash = currentHash
	snapshot.Stages[stage] = state
	feature.State, _ = NewFlowStateFromSnapshot(snapshot)
	return feature
}
