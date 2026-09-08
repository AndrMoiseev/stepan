package specflow

import (
	"strings"
	"testing"
)

func TestReviewMaterialApplyPublishesScopedReworkWithoutAutomaticReview(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-rework"
	preparePublishedSpec(t, root, repository, featureID)

	reworked := strings.Replace(validRepositorySpec(), "The state survives restart.", "The state survives restart and recheck.", 1)
	authorRunner := &stageRunner{steps: []stageRuntimeStep{{artifact: reworked}}}
	author := newTestStageEngine(t, root, authorRunner, repository)
	if _, err := author.Start(StartStageRequest{FeatureID: featureID, Stage: StageSpec}); err != nil {
		t.Fatal(err)
	}
	reviewerRunner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: mixedReviewWithPendingMaterials()}}}
	review := newTestReviewEngine(t, root, reviewerRunner, repository)

	started, err := review.Start(testStartReviewRequest(featureID))
	if err != nil || started.Outcome != ReviewMaterialDecisions || len(started.MaterialFindings) != 2 || authorRunner.turns != 0 {
		t.Fatalf("initial review = %#v, author turns=%d, err=%v", started, authorRunner.turns, err)
	}
	completed, err := review.ApplyPendingMaterial(author)
	if err != nil || completed.Outcome != ReviewReportCompleted || completed.Status != ReviewNotStarted || completed.ReworkAttempts != 1 {
		t.Fatalf("completed rework = %#v, %v", completed, err)
	}
	if authorRunner.turns != 1 || reviewerRunner.turns != 1 {
		t.Fatalf("runtime turns: author=%d reviewer=%d", authorRunner.turns, reviewerRunner.turns)
	}
	if len(completed.Progress) != 2 || completed.Progress[0].Kind != ReviewProgressReworkStarted ||
		completed.Progress[1].Kind != ReviewProgressDiff || completed.Progress[1].Diff == "" {
		t.Fatalf("progress = %#v", completed.Progress)
	}
	if strings.Contains(authorRunner.prompts[0], "pending recommendations") ||
		!strings.Contains(authorRunner.prompts[0], "reviews\\spec-001.md") && !strings.Contains(authorRunner.prompts[0], "reviews/spec-001.md") ||
		!strings.Contains(authorRunner.prompts[0], "SPEC-F-1") || !strings.Contains(authorRunner.prompts[0], "SPEC-F-2") || !strings.Contains(authorRunner.prompts[0], "SPEC-F-3") {
		t.Fatalf("author did not receive only report and ordered scope:\n%s", authorRunner.prompts[0])
	}
	loaded, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Documents[StageSpec].Content) != reworked {
		t.Fatal("automatic rework did not publish the author artifact")
	}
	report := string(loaded.Reviews[0].Content)
	if strings.Count(report, "Decided-by: user") != 2 || strings.Count(report, "Status: open") != 3 {
		t.Fatalf("review provenance/resolutions were not retained:\n%s", report)
	}
}

func TestReviewDismissalRequiresRationaleAndNeverChangesDocument(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-dismiss"
	before := preparePublishedSpec(t, root, repository, featureID)
	reviewerRunner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: mixedReviewWithOneMaterial()}}}
	review := newTestReviewEngine(t, root, reviewerRunner, repository)
	started, err := review.Start(testStartReviewRequest(featureID))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := review.DecideMaterial(MaterialFindingDecision{FindingID: started.Findings[0].ID, Decision: DecisionDismiss, Rationale: "Ignore it."}, nil); err == nil || !strings.Contains(err.Error(), "cannot be dismissed") {
		t.Fatalf("contract dismissal error = %v", err)
	}
	if _, err := review.DecideMaterial(MaterialFindingDecision{FindingID: started.MaterialFindings[0].ID, Decision: DecisionDismiss}, nil); err == nil || !strings.Contains(err.Error(), "rationale") {
		t.Fatalf("missing dismissal rationale error = %v", err)
	}
	result, err := review.DecideMaterial(MaterialFindingDecision{
		FindingID: started.MaterialFindings[0].ID, Decision: DecisionDismiss,
		Rationale: "The current scope deliberately excludes this behavior.",
	}, nil)
	if err != nil || result.Outcome != ReviewReworkRequired || result.Status != ReviewAutomaticRework {
		t.Fatalf("dismissal result = %#v, %v", result, err)
	}
	loaded, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Documents[StageSpec].Hash != before.Documents[StageSpec].Hash ||
		!strings.Contains(string(loaded.Reviews[0].Content), "Status: dismissed") ||
		!strings.Contains(string(loaded.Reviews[0].Content), "Decided-by: user") {
		t.Fatalf("dismissal changed document or lost provenance: %#v", loaded.Reviews)
	}
}

func TestReviewApplyInvokesAuthorOnceAndNextReviewIsExplicit(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-retry"
	preparePublishedSpec(t, root, repository, featureID)
	reworked := strings.Replace(validRepositorySpec(), "The state survives restart.", "The state survives every restart.", 1)
	authorRunner := &stageRunner{steps: []stageRuntimeStep{{artifact: reworked}}}
	author := newTestStageEngine(t, root, authorRunner, repository)
	if _, err := author.Start(StartStageRequest{FeatureID: featureID, Stage: StageSpec}); err != nil {
		t.Fatal(err)
	}
	reviewerRunner := &reviewRunner{steps: []reviewRuntimeStep{
		{artifact: pendingSpecReview("SPEC-F-001")},
		{artifact: openFixedReview(false)},
	}}
	review := newTestReviewEngine(t, root, reviewerRunner, repository)
	if _, err := review.Start(testStartReviewRequest(featureID)); err != nil {
		t.Fatal(err)
	}
	applied, err := review.ApplyPendingMaterial(author)
	if err != nil || applied.Outcome != ReviewReportCompleted || applied.Status != ReviewNotStarted || applied.ReworkAttempts != 1 {
		t.Fatalf("applied review = %#v, %v", applied, err)
	}
	if authorRunner.turns != 1 || reviewerRunner.turns != 1 {
		t.Fatalf("unexpected automatic turns: author=%d reviewer=%d", authorRunner.turns, reviewerRunner.turns)
	}

	newRun, err := review.Start(testStartReviewRequest(featureID))
	if err != nil || newRun.RunID != 2 || newRun.ReworkAttempts != 0 || newRun.Outcome != ReviewReworkRequired {
		t.Fatalf("explicit new review = %#v, %v", newRun, err)
	}
	if authorRunner.turns != 1 || reviewerRunner.turns != 2 {
		t.Fatalf("explicit review turns: author=%d reviewer=%d", authorRunner.turns, reviewerRunner.turns)
	}
	newState, _ := newRun.Feature.State.Stage(StageSpec)
	if newState.RetryCounters.ReviewRework != 0 {
		t.Fatalf("new review budget was not reset: %d", newState.RetryCounters.ReviewRework)
	}
}

func TestReviewAuthorMaterialQuestionContinuesInAuthorDialogue(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-review-author-question"
	preparePublishedSpec(t, root, repository, featureID)
	reworked := strings.Replace(validRepositorySpec(), "The state survives restart.", "The state favors durability and survives restart.", 1)
	authorRunner := &stageRunner{steps: []stageRuntimeStep{
		{message: "Should persistence favor latency or durability?"},
		{artifact: reworked},
	}}
	author := newTestStageEngine(t, root, authorRunner, repository)
	if _, err := author.Start(StartStageRequest{FeatureID: featureID, Stage: StageSpec}); err != nil {
		t.Fatal(err)
	}
	reviewerRunner := &reviewRunner{steps: []reviewRuntimeStep{{artifact: contractSpecReview("SPEC-F-001")}}}
	review := newTestReviewEngine(t, root, reviewerRunner, repository)
	started, err := review.Start(testStartReviewRequest(featureID))
	if err != nil || started.Outcome != ReviewReworkRequired {
		t.Fatalf("started review = %#v, %v", started, err)
	}
	result, err := review.AutomaticRework(author)
	if err != nil || result.Outcome != ReviewAuthorDecision || result.Status != ReviewAuthorDialogue ||
		!strings.Contains(result.Message, "latency or durability") || result.ReworkAttempts != 1 {
		t.Fatalf("author question = %#v, %v", result, err)
	}
	stageState, _ := result.Feature.State.Stage(StageSpec)
	if stageState.ReviewStatus != ReviewAuthorDialogue || authorRunner.turns != 1 || reviewerRunner.turns != 1 {
		t.Fatalf("dialogue ownership: state=%s author=%d reviewer=%d", stageState.ReviewStatus, authorRunner.turns, reviewerRunner.turns)
	}
	published, err := author.Submit("Favor durability.")
	if err != nil || published.Outcome != StageReviewReworkPublished {
		t.Fatalf("continued author dialogue = %#v, %v", published, err)
	}
	publishedState, _ := published.Feature.State.Stage(StageSpec)
	if publishedState.ReviewStatus != ReviewNotStarted || reviewerRunner.turns != 1 {
		t.Fatalf("published dialogue state=%s reviewer=%d", publishedState.ReviewStatus, reviewerRunner.turns)
	}
}

func mixedReviewWithPendingMaterials() string {
	return `# Review

## SPEC-F-001 — Contract issue

Severity: major
Status: open
Problem: The document violates a deterministic rule.
Location: whole document
Recommendation: Repair the structure.
Decision: fix
Decided-by: reviewer
Rationale: The document contract requires this correction.

## SPEC-F-002 — First material issue

Severity: major
Status: open
Problem: The first behavior is unclear.
Location: whole document
Recommendation: Clarify the first behavior.
Decision: pending
Decided-by: none
Rationale:

## SPEC-F-003 — Second material issue

Severity: minor
Status: open
Problem: The second behavior is unclear.
Location: whole document
Recommendation: Clarify the second behavior.
Decision: pending
Decided-by: none
Rationale:
`
}

func mixedReviewWithOneMaterial() string {
	return strings.Replace(mixedReviewWithPendingMaterials(), `
## SPEC-F-003 — Second material issue

Severity: minor
Status: open
Problem: The second behavior is unclear.
Location: whole document
Recommendation: Clarify the second behavior.
Decision: pending
Decided-by: none
Rationale:
`, "", 1)
}

func resolvedMixedReview() string {
	return `# Review

## SPEC-F-001 — Contract issue

Severity: major
Status: resolved
Problem: The document violates a deterministic rule.
Location: whole document
Recommendation: Repair the structure.
Resolution: The contract is now satisfied.
Decision: fix
Decided-by: reviewer
Rationale: The document contract requires this correction.

## SPEC-F-002 — First material issue

Severity: major
Status: resolved
Problem: The first behavior is unclear.
Location: whole document
Recommendation: Clarify the first behavior.
Resolution: The first behavior is now explicit.
Decision: fix
Decided-by: user
Rationale: User accepted the pending recommendation with /apply.

## SPEC-F-003 — Second material issue

Severity: minor
Status: resolved
Problem: The second behavior is unclear.
Location: whole document
Recommendation: Clarify the second behavior.
Resolution: The second behavior is now explicit.
Decision: fix
Decided-by: user
Rationale: User accepted the pending recommendation with /apply.
`
}

func openFixedReview(includeSecond bool) string {
	result := `# Review

## SPEC-F-001 — Material issue

Severity: major
Status: open
Problem: The flow leaves an important behavior unclear.
Location: whole document
Recommendation: Clarify the behavior.
Decision: fix
Decided-by: user
Rationale: User accepted the pending recommendation with /apply.
`
	if includeSecond {
		result += `
## SPEC-F-002 — New material issue

Severity: minor
Status: open
Problem: Recheck found a new material ambiguity.
Location: whole document
Recommendation: Clarify the new ambiguity.
Decision: fix
Decided-by: user
Rationale: User accepted the pending recommendation with /apply.
`
	}
	return result
}

func openFixedReviewWithNewPending() string {
	return openFixedReview(false) + `
## SPEC-F-002 — New material issue

Severity: minor
Status: open
Problem: Recheck found a new material ambiguity.
Location: whole document
Recommendation: Clarify the new ambiguity.
Decision: pending
Decided-by: none
Rationale:
`
}

func contractReviewWithNewPending() string {
	return contractSpecReview("SPEC-F-001") + `
## SPEC-F-002 — Persistence tradeoff

Severity: major
Status: open
Problem: The persistence tradeoff requires a user decision.
Location: whole document
Recommendation: Choose the required persistence behavior.
Decision: pending
Decided-by: none
Rationale:
`
}
