package specflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeCreatesFreshRoleFromDurableContextWithoutThreadHandle(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-resume-durable-context"
	feature := preparePublishedSpec(t, root, repository, featureID)
	publishInterruptedReview(t, root, repository, feature, ReviewRunning)

	stateBytes, err := os.ReadFile(feature.Target.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"thread_id", "session_id", "provider", "gpt-test"} {
		if strings.Contains(strings.ToLower(string(stateBytes)), forbidden) {
			t.Fatalf("state.json contains runtime identity %q: %s", forbidden, stateBytes)
		}
	}

	// A new repository and registry model a new process: no provider handle is
	// available, so recovery must rely entirely on project-owned artifacts.
	repository = newTestFeatureRepository(t, root)
	recovered, err := repository.RecoverFeatureSession(SessionRecoveryRequest{FeatureID: featureID, At: repositoryTestTime.Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Interrupted {
		t.Fatal("running review was not recognized as interrupted")
	}
	stage, _ := recovered.Feature.State.Stage(StageSpec)
	if stage.ReviewStatus != ReviewEscalated || len(stage.Reviews) != 1 || stage.Reviews[0].Status != ReviewRunning {
		t.Fatalf("recovered review state = %#v", stage)
	}
	last := recovered.Feature.Journal[len(recovered.Feature.Journal)-1]
	if last.Kind != MemLogRecovery || !strings.Contains(last.Body, "explicit /review") {
		t.Fatalf("recovery event = %#v", last)
	}

	runner := &sessionRegistryRunner{}
	registry, err := NewSessionRegistry(runner, repository)
	if err != nil {
		t.Fatal(err)
	}
	author, err := NewStageEngine(root, registry, repository, NewEmbeddedPromptCatalog())
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewReviewEngine(root, registry, repository, NewEmbeddedPromptCatalog())
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewFeatureController(repository, author, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewResumeManager(repository, registry, controller)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := manager.Activate(featureID, "new process")
	if err != nil {
		t.Fatal(err)
	}
	if progress.CurrentStage != StageSpec || progress.ReviewStatus != ReviewEscalated || !hasControllerHint(progress.CommandHints, "/review") || hasControllerHint(progress.CommandHints, "/approve") {
		t.Fatalf("resume progress = %#v", progress)
	}
	if runner.turns != 0 || len(runner.configs) != 1 {
		t.Fatalf("resume continued interrupted turn: threads=%d turns=%d", len(runner.configs), runner.turns)
	}
	bootstrap := runner.configs[0].BootstrapInstructions
	for _, required := range []string{feature.Target.JournalPath, feature.Documents[StageSpec].Path, "reviews/spec-001.md"} {
		if !strings.Contains(bootstrap, required) {
			t.Fatalf("fresh role prompt does not contain %q:\n%s", required, bootstrap)
		}
	}
	if _, err := manager.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryStopsAutomaticReworkAtExplicitReviewBoundary(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-resume-automatic-rework"
	feature := preparePublishedSpec(t, root, repository, featureID)
	publishInterruptedReview(t, root, repository, feature, ReviewAutomaticRework)

	repository = newTestFeatureRepository(t, root)
	recovery, err := repository.RecoverFeatureSession(SessionRecoveryRequest{FeatureID: featureID, At: repositoryTestTime.Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	state, _ := recovery.Feature.State.Stage(StageSpec)
	if !recovery.Interrupted || state.ReviewStatus != ReviewEscalated {
		t.Fatalf("automatic rework recovery = %#v, state=%#v", recovery, state)
	}
}

func TestDiscoverResumableDoesNotActivateOrMutateFlows(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-discovery-only"
	created, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "List without activation", At: repositoryTestTime})
	if err != nil {
		t.Fatal(err)
	}
	beforeEntries := len(created.Journal)
	flows, err := repository.DiscoverResumable()
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || flows[0].FeatureID != featureID || flows[0].CurrentStage != StageIntent || flows[0].StageStatus != StageDrafting {
		t.Fatalf("resumable flows = %#v", flows)
	}
	after, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Journal) != beforeEntries {
		t.Fatalf("discovery mutated journal: before=%d after=%d", beforeEntries, len(after.Journal))
	}
}

func TestActivationBlocksAnotherFeatureWithUncommittedChanges(t *testing.T) {
	root := initializedCommitRepository(t)
	repository := newTestFeatureRepository(t, root)
	selected := "2026-08-30-resume-selected"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: selected, Brief: "First flow", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "--", filepath.ToSlash(filepath.Join(featuresDirectoryPath, selected)))
	gitRun(t, root, "commit", "-q", "-m", "save first flow")

	other := "2026-08-30-resume-dirty"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: other, Brief: "Dirty second flow", At: repositoryTestTime.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	blockers, err := repository.ActivationBlockers(selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) == 0 || blockers[0].Code != "other-feature-uncommitted" || !strings.Contains(blockers[0].Path, other) {
		t.Fatalf("activation blockers = %#v", blockers)
	}
	selectedBlockers, err := repository.ActivationBlockers(other)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectedBlockers) != 0 {
		t.Fatalf("feature must be able to resume its own dirty flow: %#v", selectedBlockers)
	}
}

func publishInterruptedReview(t *testing.T, root string, repository *FSFeatureRepository, feature FeatureSnapshot, status ReviewStatus) {
	t.Helper()
	policy, err := PolicyForStage(StageSpec)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := reviewFingerprint(feature, policy)
	if err != nil {
		t.Fatal(err)
	}
	artifactRoot := writeRepositoryArtifact(t, root, "review.md", contractSpecReview("SPEC-F-001"))
	publication, err := repository.PublishReview(ReviewArtifactRequest{
		FeatureID: feature.Target.ID, Stage: StageSpec, ArtifactRoot: artifactRoot,
		Status: status, Original: fingerprint, ActiveIDs: reviewTargetIDs(feature, StageSpec),
		Provider: "codex", Model: "gpt-test", CreatedAt: repositoryTestTime.Add(5 * time.Minute), UpdatedAt: repositoryTestTime.Add(6 * time.Minute),
	})
	if err != nil || !publication.Published {
		t.Fatalf("publish running review = %#v, %v", publication, err)
	}
	if _, err := os.Stat(filepath.Join(feature.Target.Directory, filepath.FromSlash(publication.Path))); err != nil {
		t.Fatal(err)
	}
}
