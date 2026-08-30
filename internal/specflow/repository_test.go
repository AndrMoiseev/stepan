package specflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var repositoryTestTime = time.Date(2026, 8, 30, 12, 0, 0, 0, time.FixedZone("MSK", 3*60*60))

func TestFeatureRepositoryRoundTripRestoresAuthoritativeArtifacts(t *testing.T) {
	repoRoot := initRepository(t)
	repository := newTestFeatureRepository(t, repoRoot)
	featureID := "2026-08-30-round-trip"
	created, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Build planning flow", At: repositoryTestTime})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Documents) != 0 || len(created.Journal) != 1 {
		t.Fatalf("created feature = %#v", created)
	}

	intentRoot := writeRepositoryArtifact(t, repoRoot, "intent.md", validIntent("Initial intent"))
	intent, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageIntent, ArtifactRoot: intentRoot, Mode: ValidateDraft, At: repositoryTestTime.Add(time.Minute),
	})
	if err != nil || !intent.Published {
		t.Fatalf("publish intent: published=%t diagnostics=%#v err=%v", intent.Published, intent.Validation.Diagnostics, err)
	}

	specRoot := writeRepositoryArtifact(t, repoRoot, "spec.md", validRepositorySpec())
	spec, err := repository.PublishAuthorDraft(DraftArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: specRoot, Mode: ValidateDraft,
		UpstreamHashes: []UpstreamHash{{Stage: StageIntent, Hash: intent.Hash}}, At: repositoryTestTime.Add(2 * time.Minute),
	})
	if err != nil || !spec.Published {
		t.Fatalf("publish spec: published=%t diagnostics=%#v err=%v", spec.Published, spec.Validation.Diagnostics, err)
	}

	fingerprint, err := NewFingerprint(spec.Hash, []UpstreamHash{{Stage: StageIntent, Hash: intent.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	reviewRoot := writeRepositoryArtifact(t, repoRoot, "review.md", pendingSpecReview("SPEC-F-001"))
	review, err := repository.PublishReview(ReviewArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: reviewRoot, Status: ReviewAwaitingDecisions,
		Original: fingerprint, Attempts: 1, ActiveIDs: []StableID{mustStableID(t, "REQ-1")},
		Provider: "codex", Model: "gpt-test", CreatedAt: repositoryTestTime.Add(3 * time.Minute), UpdatedAt: repositoryTestTime.Add(3 * time.Minute),
	})
	if err != nil || !review.Published {
		t.Fatalf("publish review: published=%t diagnostics=%#v err=%v", review.Published, review.Validation.Diagnostics, err)
	}
	if _, err := repository.RecordDecision(featureID, StageSpec, RoleSpecReviewer, Decision{
		Author: DecisionUser, Decision: "fix finding", Rationale: "required", Alternatives: []string{}, Supersedes: []int{},
	}); err != nil {
		t.Fatal(err)
	}

	reopened := newTestFeatureRepository(t, repoRoot)
	loaded, err := reopened.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.State.Snapshot(), review.Feature.State.Snapshot()) {
		// RecordDecision changes only the journal, so the review publication state
		// remains the expected durable state.
		t.Fatalf("state changed across reopen\n got: %#v\nwant: %#v", loaded.State.Snapshot(), review.Feature.State.Snapshot())
	}
	if string(loaded.Documents[StageIntent].Content) != validIntent("Initial intent") || string(loaded.Documents[StageSpec].Content) != validRepositorySpec() {
		t.Fatalf("documents were not restored: %#v", loaded.Documents)
	}
	if len(loaded.Reviews) != 1 || !strings.Contains(string(loaded.Reviews[0].Content), "review_id: SPEC-REVIEW-001") ||
		!strings.Contains(string(loaded.Reviews[0].Content), "provider: \"codex\"") {
		t.Fatalf("review was not restored with front matter: %#v", loaded.Reviews)
	}
	if len(loaded.Journal) != 5 || loaded.Journal[len(loaded.Journal)-1].Kind != MemLogDecision {
		t.Fatalf("typed journal was not restored: %#v", loaded.Journal)
	}
}

func TestPendingDraftDiscardKeepsPublishedRevisionAndIssuedIDs(t *testing.T) {
	repoRoot := initRepository(t)
	repository := newTestFeatureRepository(t, repoRoot)
	featureID := "2026-08-30-pending"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Pending draft", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	initialRoot := writeRepositoryArtifact(t, repoRoot, "spec.md", validRepositorySpec())
	initial, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageSpec, ArtifactRoot: initialRoot, Mode: ValidateDraft})
	if err != nil || !initial.Published {
		t.Fatalf("publish initial spec: %#v, %v", initial.Validation.Diagnostics, err)
	}

	pendingMarkdown := strings.Replace(validRepositorySpec(), "REQ-001", "REQ-0777", 1)
	pendingRoot := writeRepositoryArtifact(t, repoRoot, "spec.md", pendingMarkdown)
	inspection, err := repository.InspectAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageSpec, ArtifactRoot: pendingRoot, Mode: ValidateDraft})
	if err != nil {
		t.Fatal(err)
	}
	if !containsStableID(inspection.State.IssuedIDs(), mustStableID(t, "REQ-777")) {
		t.Fatalf("observed ID not reserved: %v", inspection.State.IssuedIDs())
	}
	discarded, err := repository.DiscardPending(featureID, StageSpec, pendingRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pendingRoot); !os.IsNotExist(err) {
		t.Fatalf("pending artifact root still exists: %v", err)
	}
	if string(discarded.Documents[StageSpec].Content) != validRepositorySpec() || discarded.Documents[StageSpec].Hash != initial.Hash {
		t.Fatal("discard changed the published spec")
	}
	if !containsStableID(discarded.State.IssuedIDs(), mustStableID(t, "REQ-777")) {
		t.Fatal("discard released an issued ID")
	}
	loaded, err := newTestFeatureRepository(t, repoRoot).Load(featureID)
	if err != nil || !containsStableID(loaded.State.IssuedIDs(), mustStableID(t, "REQ-777")) {
		t.Fatalf("issued ID did not survive reopen: %v, %v", loaded.State.IssuedIDs(), err)
	}
}

func TestReviewRunUpdatesOneNumberedReportAndNewRunGetsNextNumber(t *testing.T) {
	repoRoot := initRepository(t)
	repository := newTestFeatureRepository(t, repoRoot)
	featureID := "2026-08-30-review-run"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Review lifecycle", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := NewFingerprint("spec-hash", []UpstreamHash{{Stage: StageIntent, Hash: "intent-hash"}})
	root := writeRepositoryArtifact(t, repoRoot, "review.md", pendingSpecReview("SPEC-F-001"))
	first, err := repository.PublishReview(repositoryReviewRequest(featureID, root, 0, fingerprint, nil))
	if err != nil || !first.Published {
		t.Fatalf("first review: %#v, %v", first.Validation.Diagnostics, err)
	}
	previous := findingSnapshots(first.Validation)
	if err := os.WriteFile(filepath.Join(root, "review.md"), []byte(dismissedSpecReview("SPEC-F-001")), 0o644); err != nil {
		t.Fatal(err)
	}
	updateRequest := repositoryReviewRequest(featureID, root, first.RunID, fingerprint, previous)
	updateRequest.Status = ReviewCompleted
	updateRequest.Accepted = &fingerprint
	updateRequest.UpdatedAt = repositoryTestTime.Add(time.Minute)
	updated, err := repository.PublishReview(updateRequest)
	if err != nil || !updated.Published {
		t.Fatalf("updated review: %#v, %v", updated.Validation.Diagnostics, err)
	}
	if len(updated.Feature.Reviews) != 1 || updated.Feature.Reviews[0].RunID != 1 ||
		!strings.Contains(string(updated.Feature.Reviews[0].Content), "Status: dismissed") ||
		!strings.Contains(string(updated.Feature.Reviews[0].Content), "accepted_for_revision: spec-hash") {
		t.Fatalf("same run did not update one report: %#v", updated.Feature.Reviews)
	}

	newRoot := writeRepositoryArtifact(t, repoRoot, "review.md", dismissedSpecReview("SPEC-F-001"))
	secondRequest := repositoryReviewRequest(featureID, newRoot, 0, fingerprint, findingSnapshots(updated.Validation))
	secondRequest.Status = ReviewCompleted
	second, err := repository.PublishReview(secondRequest)
	if err != nil || !second.Published || second.RunID != 2 {
		t.Fatalf("second review: id=%d diagnostics=%#v err=%v", second.RunID, second.Validation.Diagnostics, err)
	}
	entries, err := os.ReadDir(second.Feature.Target.ReviewsDirectory)
	if err != nil || len(entries) != 2 || entries[0].Name() != "spec-001.md" || entries[1].Name() != "spec-002.md" {
		t.Fatalf("review files = %#v, %v", entries, err)
	}
}

func TestRepositoryClassifiesExternalChangesAndProtectsServiceArtifacts(t *testing.T) {
	repoRoot := initRepository(t)
	repository := newTestFeatureRepository(t, repoRoot)
	featureID := "2026-08-30-protected"
	feature, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Protect files", At: repositoryTestTime})
	if err != nil {
		t.Fatal(err)
	}
	intentRoot := writeRepositoryArtifact(t, repoRoot, "intent.md", validIntent("Protected intent"))
	publication, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageIntent, ArtifactRoot: intentRoot, Mode: ValidateDraft})
	if err != nil || !publication.Published {
		t.Fatalf("publish intent: %#v, %v", publication.Validation.Diagnostics, err)
	}
	feature = publication.Feature
	fingerprint, _ := NewFingerprint("spec-hash", []UpstreamHash{{Stage: StageIntent, Hash: publication.Hash}})
	reviewRoot := writeRepositoryArtifact(t, repoRoot, "review.md", pendingSpecReview("SPEC-F-001"))
	review, err := repository.PublishReview(repositoryReviewRequest(featureID, reviewRoot, 0, fingerprint, nil))
	if err != nil || !review.Published {
		t.Fatalf("publish protected review: %#v, %v", review.Validation.Diagnostics, err)
	}
	feature = review.Feature
	specState, _ := feature.State.Stage(StageSpec)
	reviewPath := filepath.Join(feature.Target.Directory, filepath.FromSlash(specState.Reviews[0].Path))

	protected := []string{feature.Target.StatePath, feature.Target.JournalPath, reviewPath}
	for _, path := range protected {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(append([]byte(nil), original...), []byte("external\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			inspection, err := repository.InspectChanges(featureID)
			if err != nil || len(inspection.Blocking) != 1 || inspection.Blocking[0].Class != ChangeProtectedArtifact {
				t.Fatalf("inspection = %#v, %v", inspection, err)
			}
			entry, _ := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogSession, repositoryTestTime, "must block")
			if _, err := repository.RecordActivity(featureID, entry); !errors.Is(err, ErrRepositoryBlocked) {
				t.Fatalf("protected mutation error = %v", err)
			}
			if err := os.WriteFile(path, original, 0o644); err != nil {
				t.Fatal(err)
			}
		})
	}

	originalIntent := append([]byte(nil), feature.Documents[StageIntent].Content...)
	if err := os.WriteFile(feature.Target.IntentPath, []byte(validIntent("Manual revision")), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := repository.InspectChanges(featureID)
	if err != nil || len(inspection.DocumentRevisions) != 1 || len(inspection.Blocking) != 0 || inspection.DocumentRevisions[0].Stage != StageIntent {
		t.Fatalf("document inspection = %#v, %v", inspection, err)
	}
	reopened := newTestFeatureRepository(t, repoRoot)
	loadedRevision, err := reopened.Load(featureID)
	if err != nil || len(loadedRevision.Changes.DocumentRevisions) != 1 {
		t.Fatalf("reopened repository lost document revision: %#v, %v", loadedRevision.Changes, err)
	}
	reopenedInspection, err := reopened.InspectChanges(featureID)
	if err != nil || len(reopenedInspection.DocumentRevisions) != 1 {
		t.Fatalf("reopened inspection lost document revision: %#v, %v", reopenedInspection, err)
	}
	if _, err := repository.RecordDecision(featureID, StageIntent, RoleIntentAuthor, Decision{
		Author: DecisionUser, Decision: "accept", Rationale: "manual revision", Alternatives: []string{}, Supersedes: []int{},
	}); !errors.Is(err, ErrExternalChanges) {
		t.Fatalf("document revision error = %v", err)
	}
	if err := os.WriteFile(feature.Target.IntentPath, originalIntent, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRecoversUnambiguousPartialWriteAndBlocksAmbiguousWrite(t *testing.T) {
	t.Run("complete unambiguous create", func(t *testing.T) {
		repoRoot := initRepository(t)
		faults := &scriptedRepositoryFaults{step: "write", path: "state.json", remaining: 1}
		repository, err := newFSFeatureRepository(repoRoot, faults)
		if err != nil {
			t.Fatal(err)
		}
		featureID := "2026-08-30-recover"
		if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Recover me", At: repositoryTestTime}); err == nil {
			t.Fatal("fault did not interrupt create")
		}
		loaded, err := newTestFeatureRepository(t, repoRoot).Load(featureID)
		if err != nil || len(loaded.Journal) != 1 || loaded.State.CurrentStage() != StageIntent {
			t.Fatalf("recovered feature = %#v, %v", loaded, err)
		}
		if _, err := os.Stat(filepath.Join(loaded.Target.Directory, recoveryManifestName)); !os.IsNotExist(err) {
			t.Fatalf("recovery manifest remains: %v", err)
		}
	})

	t.Run("preserve ambiguous bytes", func(t *testing.T) {
		repoRoot := initRepository(t)
		faults := &scriptedRepositoryFaults{step: "write", path: "state.json", remaining: 1}
		repository, err := newFSFeatureRepository(repoRoot, faults)
		if err != nil {
			t.Fatal(err)
		}
		featureID := "2026-08-30-ambiguous"
		if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Do not overwrite", At: repositoryTestTime}); err == nil {
			t.Fatal("fault did not interrupt create")
		}
		target, _ := FeatureTargetForID(repoRoot, featureID)
		ambiguous := []byte("user bytes\n")
		if err := os.WriteFile(target.JournalPath, ambiguous, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = newTestFeatureRepository(t, repoRoot).Load(featureID)
		if !errors.Is(err, ErrRepositoryBlocked) {
			t.Fatalf("ambiguous load error = %v", err)
		}
		got, readErr := os.ReadFile(target.JournalPath)
		if readErr != nil || string(got) != string(ambiguous) {
			t.Fatalf("ambiguous bytes overwritten: %q, %v", got, readErr)
		}
	})
}

func TestRepositoryRejectsCanonicalStateTamperingAfterRestart(t *testing.T) {
	repoRoot := initRepository(t)
	repository := newTestFeatureRepository(t, repoRoot)
	featureID := "2026-08-30-state-tamper"
	feature, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Protect durable state", At: repositoryTestTime})
	if err != nil {
		t.Fatal(err)
	}
	originalJournal, err := os.ReadFile(feature.Target.JournalPath)
	if err != nil {
		t.Fatal(err)
	}

	snapshot := feature.State.Snapshot()
	intent := snapshot.Stages[StageIntent]
	intent.RetryCounters.ParserRepair = 1
	snapshot.Stages[StageIntent] = intent
	tamperedState, err := NewFlowStateFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tamperedBytes, err := encodeState(tamperedState)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feature.Target.StatePath, tamperedBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened := newTestFeatureRepository(t, repoRoot)
	if _, err := reopened.Load(featureID); !errors.Is(err, ErrRepositoryBlocked) || !strings.Contains(err.Error(), "state hash anchored by mem-log.md") {
		t.Fatalf("canonical state tampering load error = %v", err)
	}
	entry, _ := NewMemLogEntry(StageIntent, RoleIntentAuthor, MemLogSession, repositoryTestTime, "must remain blocked")
	if _, err := reopened.RecordActivity(featureID, entry); !errors.Is(err, ErrRepositoryBlocked) {
		t.Fatalf("canonical state tampering mutation error = %v", err)
	}
	stateAfter, stateErr := os.ReadFile(feature.Target.StatePath)
	journalAfter, journalErr := os.ReadFile(feature.Target.JournalPath)
	if stateErr != nil || journalErr != nil || !reflect.DeepEqual(stateAfter, tamperedBytes) || !reflect.DeepEqual(journalAfter, originalJournal) {
		t.Fatalf("blocked load overwrote bytes: state=%v journal=%v stateErr=%v journalErr=%v", reflect.DeepEqual(stateAfter, tamperedBytes), reflect.DeepEqual(journalAfter, originalJournal), stateErr, journalErr)
	}
}

type scriptedRepositoryFaults struct {
	step      string
	path      string
	remaining int
}

func (f *scriptedRepositoryFaults) Before(step, path string) error {
	if step == f.step && path == f.path && f.remaining > 0 {
		f.remaining--
		return fmt.Errorf("injected %s failure for %s", step, path)
	}
	return nil
}

func newTestFeatureRepository(t *testing.T, root string) *FSFeatureRepository {
	t.Helper()
	repository, err := NewFSFeatureRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	repository.now = func() time.Time { return repositoryTestTime }
	return repository
}

func writeRepositoryArtifact(t *testing.T, repoRoot, filename, content string) string {
	t.Helper()
	root, err := CreateArtifactRoot(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeArtifact(root) })
	if err := os.WriteFile(filepath.Join(root, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func validIntent(title string) string {
	return "# " + title + "\n\n## Scope\n\nImplement the agreed flow.\n\n## Open questions\n"
}

func validRepositorySpec() string {
	return `# Specification

## REQ-001 — Requirement

The flow persists its state.

## DEC-001 — Decision

Use a durable repository.

## AC-001 — Acceptance

Traces: REQ-001

The state survives restart.

## Open questions
`
}

func pendingSpecReview(id string) string {
	return fmt.Sprintf(`# Review

## %s — Material issue

Severity: major
Status: open
Problem: The flow leaves an important behavior unclear.
Location: whole document
Recommendation: Clarify the behavior.
Decision: pending
Decided-by: none
Rationale:
`, id)
}

func dismissedSpecReview(id string) string {
	return fmt.Sprintf(`# Review

## %s — Material issue

Severity: major
Status: dismissed
Problem: The flow leaves an important behavior unclear.
Location: whole document
Recommendation: Clarify the behavior.
Decision: dismiss
Decided-by: user
Rationale: The current scope deliberately excludes it.
`, id)
}

func repositoryReviewRequest(featureID, root string, runID uint64, fingerprint Fingerprint, previous []FindingSnapshot) ReviewArtifactRequest {
	return ReviewArtifactRequest{
		FeatureID: featureID, Stage: StageSpec, ArtifactRoot: root, RunID: runID,
		Status: ReviewAwaitingDecisions, Original: fingerprint, Attempts: 1,
		PreviousFindings: previous, Provider: "codex", Model: "gpt-test",
		CreatedAt: repositoryTestTime, UpdatedAt: repositoryTestTime,
	}
}

func findingSnapshots(result DocumentResult) []FindingSnapshot {
	values := make([]FindingSnapshot, 0, len(result.Document.Findings))
	for _, finding := range result.Document.Findings {
		values = append(values, finding.Finding.Snapshot())
	}
	return values
}

func containsStableID(values []StableID, want StableID) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
