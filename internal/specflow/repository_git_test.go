package specflow

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRepositoryRequiresCleanTreeForCreateAndApproval(t *testing.T) {
	t.Run("create reports staged and unstaged paths", func(t *testing.T) {
		root := initializedCommitRepository(t)
		writeGitTestFile(t, root, "staged.txt", "staged\n")
		gitRun(t, root, "add", "--", "staged.txt")
		writeGitTestFile(t, root, "tracked.txt", "unstaged\n")

		repository := newTestFeatureRepository(t, root)
		preflightErr := repository.PreflightCreate()
		if !errors.Is(preflightErr, ErrRepositoryBlocked) || !errors.Is(preflightErr, ErrRepositoryDirty) ||
			!strings.Contains(preflightErr.Error(), "staged.txt") || !strings.Contains(preflightErr.Error(), "tracked.txt") {
			t.Fatalf("PreflightCreate() error = %v, want every dirty path", preflightErr)
		}
		_, err := repository.Create(CreateFeatureRequest{FeatureID: "2026-08-30-dirty-create", Brief: "Must be clean", At: repositoryTestTime})
		if !errors.Is(err, ErrRepositoryBlocked) || !errors.Is(err, ErrRepositoryDirty) || !strings.Contains(err.Error(), "staged.txt") || !strings.Contains(err.Error(), "tracked.txt") {
			t.Fatalf("Create() error = %v, want every dirty path", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(featuresDirectoryPath), "2026-08-30-dirty-create")); !os.IsNotExist(statErr) {
			t.Fatalf("blocked create changed repository: %v", statErr)
		}
	})

	t.Run("approval returns every outside blocker without changing state", func(t *testing.T) {
		root := initializedCommitRepository(t)
		repository, featureID := publishedIntentFeature(t, root, "dirty-approve", "Approval intent")
		before := mustReadState(t, root, featureID)
		writeGitTestFile(t, root, "outside-staged.txt", "staged\n")
		gitRun(t, root, "add", "--", "outside-staged.txt")
		writeGitTestFile(t, root, "tracked.txt", "unstaged\n")

		result, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if result.Committed || len(result.Blocking) != 2 {
			t.Fatalf("approval = %#v", result)
		}
		paths := []string{result.Blocking[0].Path, result.Blocking[1].Path}
		sort.Strings(paths)
		if strings.Join(paths, ",") != "outside-staged.txt,tracked.txt" {
			t.Fatalf("blocking paths = %v", paths)
		}
		after := mustReadState(t, root, featureID)
		if string(before) != string(after) {
			t.Fatal("blocked approval changed state.json")
		}
		if got := gitHeadMessage(t, root); got != "initial" {
			t.Fatalf("HEAD message = %q", got)
		}
	})
}

func TestRepositoryApprovalValidatesAndCommitsOnlyFeature(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, featureID := publishedIntentFeature(t, root, "approve-intent", "Committed intent")

	result, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !result.Committed || len(result.Blocking) != 0 {
		t.Fatalf("Approve() = %#v, %v", result, err)
	}
	intentState, _ := result.Feature.State.Stage(StageIntent)
	if intentState.Status != StageCommitted || intentState.ApprovedHash != result.Feature.Documents[StageIntent].Hash {
		t.Fatalf("intent state = %#v", intentState)
	}
	if got, want := gitHeadMessage(t, root), "feature("+featureID+"): approve intent"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	for _, path := range gitHeadPaths(t, root) {
		if !strings.HasPrefix(path, filepath.ToSlash(filepath.Join(featuresDirectoryPath, featureID))+"/") {
			t.Fatalf("phase commit captured path outside feature: %q", path)
		}
	}
	if status := gitStatus(t, root); status != "" {
		t.Fatalf("phase commit left dirty tree: %q", status)
	}

	invalidRoot := initializedCommitRepository(t)
	invalidRepository, invalidID := publishedIntentFeature(t, invalidRoot, "open-question", "Question intent")
	content := validIntent("Question intent") + "\n- Deferred choice\n"
	// Publish the changed bytes through the normal artifact seam so the approval
	// validator, rather than external-change protection, observes the question.
	artifactRoot := writeRepositoryArtifact(t, invalidRoot, "intent.md", content)
	if _, err := invalidRepository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: invalidID, Stage: StageIntent, ArtifactRoot: artifactRoot, Mode: ValidateDraft}); err != nil {
		t.Fatal(err)
	}
	blocked, err := invalidRepository.Approve(ApproveStageRequest{FeatureID: invalidID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || blocked.Committed || !hasApprovalBlocker(blocked.Blocking, string(DiagnosticOpenQuestionsUnresolved)) {
		t.Fatalf("open-question approval = %#v, %v", blocked, err)
	}
}

func TestRepositoryCheckpointCommitsStableDraftWithoutApprovingIt(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, featureID := publishedIntentFeature(t, root, "checkpoint-intent", "Checkpoint intent")

	feature, err := repository.Checkpoint(CheckpointRequest{
		FeatureID: featureID,
		Stage:     StageIntent,
		Role:      RoleIntentAuthor,
		Kind:      CheckpointAuthorDraft,
		At:        repositoryTestTime.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := feature.State.Stage(StageIntent)
	if state.Status != StagePublished || state.ApprovedHash != "" {
		t.Fatalf("checkpoint approved the draft: %#v", state)
	}
	if got, want := gitHeadMessage(t, root), "feature("+featureID+"): checkpoint intent author-draft"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	if status := gitStatus(t, root); status != "" {
		t.Fatalf("checkpoint left dirty tree: %q", status)
	}
}

func TestRepositorySequentialApprovalsPersistCurrentStage(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, featureID := publishedIntentFeature(t, root, "canonical-stages", "Canonical stages")

	intentResult, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !intentResult.Committed {
		t.Fatalf("approve intent = %#v, %v", intentResult, err)
	}
	assertCurrentControllerStage(t, intentResult.Feature, StageSpec, StageDrafting)
	durableIntent := loadDurableControllerStage(t, root, featureID, StageSpec, StageDrafting)

	intent := durableIntent.Documents[StageIntent]
	specRoot := writeRepositoryArtifact(t, root, "spec.md", validRepositorySpec())
	specPublication, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: specRoot, Mode: ValidateDraft,
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: intent.Hash}}, At: repositoryTestTime.Add(2 * time.Minute),
	})
	if err != nil || !specPublication.Published {
		t.Fatalf("publish spec = %#v, %v", specPublication.Validation.Diagnostics, err)
	}
	specResult, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageSpec, At: repositoryTestTime.Add(3 * time.Minute)})
	if err != nil || !specResult.Committed {
		t.Fatalf("approve spec = %#v, %v", specResult, err)
	}
	assertCurrentControllerStage(t, specResult.Feature, StagePlan, StageDrafting)
	durableSpec := loadDurableControllerStage(t, root, featureID, StagePlan, StageDrafting)

	planRoot := writeRepositoryArtifact(t, root, "plan.md", validRepositoryPlan())
	planPublication, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StagePlan, ArtifactRoot: planRoot, Mode: ValidateDraft,
		ActiveIDs: activeDocumentIDs(durableSpec, []Stage{StageIntent, StageSpec}),
		UpstreamHashes: []UpstreamHash{
			{Stage: StageIntent, Hash: durableSpec.Documents[StageIntent].Hash},
			{Stage: StageSpec, Hash: durableSpec.Documents[StageSpec].Hash},
		},
		At: repositoryTestTime.Add(4 * time.Minute),
	})
	if err != nil || !planPublication.Published {
		t.Fatalf("publish plan = %#v, %v", planPublication.Validation.Diagnostics, err)
	}
	planResult, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StagePlan, At: repositoryTestTime.Add(5 * time.Minute)})
	if err != nil || !planResult.Committed {
		t.Fatalf("approve plan = %#v, %v", planResult, err)
	}
	assertCurrentControllerStage(t, planResult.Feature, StagePlan, StageCommitted)
	durablePlan := loadDurableControllerStage(t, root, featureID, StagePlan, StageCommitted)
	if durablePlan.State.Status() != FlowActive {
		t.Fatalf("committed plan closed flow: %s", durablePlan.State.Status())
	}
}

func loadDurableControllerStage(t *testing.T, root, featureID string, stage Stage, status StageStatus) FeatureSnapshot {
	t.Helper()
	reopened := newTestFeatureRepository(t, root)
	feature, err := reopened.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	assertCurrentControllerStage(t, feature, stage, status)
	return feature
}

func assertCurrentControllerStage(t *testing.T, feature FeatureSnapshot, stage Stage, status StageStatus) {
	t.Helper()
	if feature.State.CurrentStage() != stage {
		t.Fatalf("current stage = %s, want %s", feature.State.CurrentStage(), stage)
	}
	stageState, _ := feature.State.Stage(stage)
	if stageState.Status != status {
		t.Fatalf("%s status = %s, want %s", stage, stageState.Status, status)
	}
}

func validRepositoryPlan() string {
	return `# Plan

## TASK-001 — implement canonical flow

Traces: REQ-001, DEC-001

### Test scenario — canonical flow

Traces: AC-001

The controller advances through the fixed stages and leaves the plan committed.

## Open questions
`
}

func TestRepositoryCommitFailureRollsBackPublishedStateAndPreservesIndex(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, featureID := publishedIntentFeature(t, root, "commit-failure", "Retry approval")
	installFailingPreCommitHook(t, root)

	result, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if !errors.Is(err, ErrPhaseCommit) || result.Committed {
		t.Fatalf("Approve() = %#v, %v", result, err)
	}
	intentState, _ := result.Feature.State.Stage(StageIntent)
	if intentState.Status != StagePublished || intentState.ApprovedHash != "" {
		t.Fatalf("failed commit state = %#v", intentState)
	}
	if got := gitHeadMessage(t, root); got != "initial" {
		t.Fatalf("failed commit moved HEAD to %q", got)
	}
	if !journalContains(result.Feature.Journal, MemLogError, "phase commit failed") {
		t.Fatal("failed commit was not recorded in mem-log")
	}
	if status := gitStatus(t, root); strings.Contains(status, "M  ") || strings.Contains(status, "A  ") {
		t.Fatalf("failed commit left feature paths staged: %q", status)
	}
}

func TestRepositoryRecoversCrashAroundPhaseCommitWithoutCommitState(t *testing.T) {
	t.Run("before commit rolls back to published", func(t *testing.T) {
		root := initializedCommitRepository(t)
		featureID := "2026-08-30-crash-before"
		faults := &scriptedRepositoryFaults{step: "phase-commit", path: featureID, remaining: 1}
		repository, err := newFSFeatureRepository(root, faults)
		if err != nil {
			t.Fatal(err)
		}
		repository.now = func() time.Time { return repositoryTestTime }
		publishIntentWithRepository(t, repository, root, featureID, "Crash before commit")

		if _, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)}); err == nil {
			t.Fatal("injected crash did not interrupt approval")
		}
		crashed := decodeTestState(t, mustReadState(t, root, featureID))
		stage, _ := crashed.Stage(StageIntent)
		if stage.Status != StageCommitted {
			t.Fatalf("pre-crash state = %#v", stage)
		}

		loaded, err := newTestFeatureRepository(t, root).Load(featureID)
		if err != nil {
			t.Fatal(err)
		}
		stage, _ = loaded.State.Stage(StageIntent)
		if stage.Status != StagePublished || stage.ApprovedHash != "" || !journalContains(loaded.Journal, MemLogRecovery, "rolled back") {
			t.Fatalf("recovered feature = %#v, journal=%#v", stage, loaded.Journal)
		}
		if stateText := string(mustReadState(t, root, featureID)); strings.Contains(stateText, "commit_sha") || strings.Contains(stateText, "committing") {
			t.Fatalf("state contains forbidden Git operation metadata: %s", stateText)
		}
	})

	t.Run("after commit accepts committed state", func(t *testing.T) {
		root := initializedCommitRepository(t)
		featureID := "2026-08-30-crash-after"
		faults := &scriptedRepositoryFaults{step: "phase-complete", path: featureID, remaining: 1}
		repository, err := newFSFeatureRepository(root, faults)
		if err != nil {
			t.Fatal(err)
		}
		repository.now = func() time.Time { return repositoryTestTime }
		publishIntentWithRepository(t, repository, root, featureID, "Crash after commit")

		if _, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)}); err == nil {
			t.Fatal("injected completion crash did not interrupt approval")
		}
		loaded, err := newTestFeatureRepository(t, root).Load(featureID)
		if err != nil {
			t.Fatal(err)
		}
		stage, _ := loaded.State.Stage(StageIntent)
		if stage.Status != StageCommitted {
			t.Fatalf("committed state was rolled back: %#v", stage)
		}
		if got, want := gitHeadMessage(t, root), "feature("+featureID+"): approve intent"; got != want {
			t.Fatalf("message = %q, want %q", got, want)
		}
	})
}

func TestRepositoryCrashAfterIndexPreparationCleansOnlyManagedPaths(t *testing.T) {
	root := initializedCommitRepository(t)
	featureID := "2026-08-30-index-crash"
	faults := &scriptedRepositoryFaults{step: "phase-commit", path: featureID, remaining: 1}
	repository, err := newFSFeatureRepository(root, faults)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return repositoryTestTime }
	publishIntentWithRepository(t, repository, root, featureID, "Index crash")
	if _, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)}); err == nil {
		t.Fatal("injected crash did not interrupt approval")
	}
	managedPrefix := filepath.ToSlash(filepath.Join(featuresDirectoryPath, featureID)) + "/"
	if tracked := gitRun(t, root, "ls-files", "--", managedPrefix+"intent.md", managedPrefix+"state.json", managedPrefix+"mem-log.md"); tracked == "" {
		t.Fatal("test did not reach transient intent-to-add window")
	}

	writeGitTestFile(t, root, "user-staged.txt", "preserve me\n")
	gitRun(t, root, "add", "--", "user-staged.txt")
	if _, err := newTestFeatureRepository(t, root).Load(featureID); err != nil {
		t.Fatal(err)
	}
	if staged := gitRun(t, root, "diff", "--cached", "--name-only"); staged != "user-staged.txt" {
		t.Fatalf("recovery changed user index entries: %q", staged)
	}
	if tracked := gitRun(t, root, "ls-files", "--", managedPrefix+"intent.md", managedPrefix+"state.json", managedPrefix+"mem-log.md"); tracked != "" {
		t.Fatalf("transient managed index entries remain: %q", tracked)
	}
}

func TestRepositoryPhaseRecoveryBlocksAmbiguousBytesWithoutOverwrite(t *testing.T) {
	root := initializedCommitRepository(t)
	featureID := "2026-08-30-ambiguous-phase"
	faults := &scriptedRepositoryFaults{step: "phase-commit", path: featureID, remaining: 1}
	repository, err := newFSFeatureRepository(root, faults)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return repositoryTestTime }
	feature := publishIntentWithRepository(t, repository, root, featureID, "Ambiguous phase")
	if _, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)}); err == nil {
		t.Fatal("injected crash did not interrupt approval")
	}
	ambiguous := []byte("user bytes that match no known hash\n")
	if err := os.WriteFile(feature.Target.StatePath, ambiguous, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestFeatureRepository(t, root).Load(featureID); !errors.Is(err, ErrRepositoryBlocked) || !strings.Contains(err.Error(), "neither pre-operation nor intended") {
		t.Fatalf("ambiguous recovery error = %v", err)
	}
	got, err := os.ReadFile(feature.Target.StatePath)
	if err != nil || string(got) != string(ambiguous) {
		t.Fatalf("ambiguous bytes overwritten: %q, %v", got, err)
	}
}

func TestRepositoryRevisesCommittedIntentImmediately(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, featureID := publishedIntentFeature(t, root, "revise-intent", "Original intent")
	approved, err := repository.Approve(ApproveStageRequest{FeatureID: featureID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !approved.Committed {
		t.Fatalf("initial approval: %#v, %v", approved, err)
	}
	changed := validIntent("Minor revision")
	if err := os.WriteFile(approved.Feature.Target.IntentPath, []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := repository.ReviseIntent(ReviseIntentRequest{FeatureID: featureID, At: repositoryTestTime.Add(2 * time.Minute)})
	if err != nil || !result.Committed {
		t.Fatalf("ReviseIntent() = %#v, %v", result, err)
	}
	intentState, _ := result.Feature.State.Stage(StageIntent)
	if intentState.Status != StageCommitted || intentState.CurrentHash != hash([]byte(changed)) || intentState.ApprovedHash != hash([]byte(changed)) {
		t.Fatalf("revised intent state = %#v", intentState)
	}
	if got, want := gitHeadMessage(t, root), "feature("+featureID+"): revise intent"; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestRepositorySupersedesFeatureWithLinkedCopyInOneCommit(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, oldID := publishedIntentFeature(t, root, "old-feature", "Original intent")
	approved, err := repository.Approve(ApproveStageRequest{FeatureID: oldID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !approved.Committed {
		t.Fatalf("initial approval: %#v, %v", approved, err)
	}
	original := append([]byte(nil), approved.Feature.Documents[StageIntent].Content...)
	changed := []byte(validIntent("Materially different intent"))
	if err := os.WriteFile(approved.Feature.Target.IntentPath, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	newID := "2026-08-30-new-feature"

	result, err := repository.SupersedeIntent(SupersedeIntentRequest{OldFeatureID: oldID, NewFeatureID: newID, At: repositoryTestTime.Add(2 * time.Minute)})
	if err != nil || !result.Committed {
		t.Fatalf("SupersedeIntent() = %#v, %v", result, err)
	}
	if result.Old.State.Status() != FlowSuperseded || result.Old.State.Snapshot().Supersession.SupersededBy != newID {
		t.Fatalf("old state = %#v", result.Old.State.Snapshot())
	}
	if result.New.State.Status() != FlowActive || result.New.State.Snapshot().Supersession.Supersedes != oldID {
		t.Fatalf("new state = %#v", result.New.State.Snapshot())
	}
	newIntent, _ := result.New.State.Stage(StageIntent)
	if newIntent.Status != StagePublished || newIntent.CurrentHash != hash(changed) || newIntent.ApprovedHash != "" {
		t.Fatalf("new intent state = %#v", newIntent)
	}
	if string(result.New.Documents[StageIntent].Content) != string(changed) || string(result.Old.Documents[StageIntent].Content) != string(original) {
		t.Fatalf("intent copies: old=%q new=%q", result.Old.Documents[StageIntent].Content, result.New.Documents[StageIntent].Content)
	}
	if got, want := gitHeadMessage(t, root), "feature("+oldID+"): supersede with "+newID; got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
	for _, path := range gitHeadPaths(t, root) {
		if !strings.Contains(path, "/"+oldID+"/") && !strings.Contains(path, "/"+newID+"/") {
			t.Fatalf("supersession captured third path: %q", path)
		}
	}
	reopened, err := newTestFeatureRepository(t, root).Load(oldID)
	if err != nil || reopened.State.Status() != FlowSuperseded {
		t.Fatalf("old feature reactivated: status=%s err=%v", reopened.State.Status(), err)
	}
}

func TestRepositoryFailedSupersessionRestoresOldFlowAndRemovesNewFlow(t *testing.T) {
	root := initializedCommitRepository(t)
	repository, oldID := publishedIntentFeature(t, root, "failed-old", "Original intent")
	approved, err := repository.Approve(ApproveStageRequest{FeatureID: oldID, Stage: StageIntent, At: repositoryTestTime.Add(time.Minute)})
	if err != nil || !approved.Committed {
		t.Fatalf("initial approval: %#v, %v", approved, err)
	}
	changed := []byte(validIntent("Material change to retry"))
	if err := os.WriteFile(approved.Feature.Target.IntentPath, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	installFailingPreCommitHook(t, root)
	newID := "2026-08-30-failed-new"

	result, err := repository.SupersedeIntent(SupersedeIntentRequest{OldFeatureID: oldID, NewFeatureID: newID, At: repositoryTestTime.Add(2 * time.Minute)})
	if !errors.Is(err, ErrPhaseCommit) || result.Committed {
		t.Fatalf("SupersedeIntent() = %#v, %v", result, err)
	}
	if result.Old.State.Status() != FlowActive || result.Old.State.Snapshot().Supersession.SupersededBy != "" {
		t.Fatalf("failed supersession changed old flow: %#v", result.Old.State.Snapshot())
	}
	gotIntent, readErr := os.ReadFile(result.Old.Target.IntentPath)
	if readErr != nil || !bytes.Equal(gotIntent, changed) {
		t.Fatalf("manual revision was not preserved: %q, %v", gotIntent, readErr)
	}
	newTarget, _ := FeatureTargetForID(root, newID)
	if _, statErr := os.Stat(newTarget.Directory); !os.IsNotExist(statErr) {
		t.Fatalf("failed supersession left new feature: %v", statErr)
	}
}

func initializedCommitRepository(t *testing.T) string {
	t.Helper()
	root := initRepository(t)
	gitRun(t, root, "config", "user.email", "stepan-tests@example.invalid")
	gitRun(t, root, "config", "user.name", "Stepan Tests")
	writeGitTestFile(t, root, "tracked.txt", "initial\n")
	gitRun(t, root, "add", "--", "tracked.txt")
	gitRun(t, root, "commit", "-q", "-m", "initial")
	return root
}

func publishedIntentFeature(t *testing.T, root, suffix, title string) (*FSFeatureRepository, string) {
	t.Helper()
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-" + suffix
	publishIntentWithRepository(t, repository, root, featureID, title)
	return repository, featureID
}

func publishIntentWithRepository(t *testing.T, repository *FSFeatureRepository, root, featureID, title string) FeatureSnapshot {
	t.Helper()
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: title, At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	artifactRoot := writeRepositoryArtifact(t, root, "intent.md", validIntent(title))
	publication, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageIntent, ArtifactRoot: artifactRoot, Mode: ValidateDraft})
	if err != nil || !publication.Published {
		t.Fatalf("publish intent: %#v, %v", publication.Validation.Diagnostics, err)
	}
	return publication.Feature
}

func writeGitTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func gitHeadMessage(t *testing.T, root string) string {
	t.Helper()
	return gitRun(t, root, "show", "-s", "--format=%s", "HEAD")
}

func gitHeadPaths(t *testing.T, root string) []string {
	t.Helper()
	text := gitRun(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if text == "" {
		return nil
	}
	return strings.Fields(strings.ReplaceAll(text, "\\", "/"))
}

func mustReadState(t *testing.T, root, featureID string) []byte {
	t.Helper()
	target, err := FeatureTargetForID(root, featureID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeTestState(t *testing.T, data []byte) FlowState {
	t.Helper()
	state, err := DecodeFlowState(data)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func hasApprovalBlocker(values []ApprovalBlocker, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func journalContains(entries []MemLogEntry, kind MemLogEventKind, text string) bool {
	for _, entry := range entries {
		if entry.Kind == kind && strings.Contains(entry.Body, text) {
			return true
		}
	}
	return false
}

func installFailingPreCommitHook(t *testing.T, root string) {
	t.Helper()
	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
