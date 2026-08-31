package specflow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestSessionRegistryReusesThreadsByFeatureAndRole(t *testing.T) {
	root := initRepository(t)
	repository := newTestFeatureRepository(t, root)
	featureID := "2026-08-30-session-registry"
	if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Keep role sessions alive", At: repositoryTestTime}); err != nil {
		t.Fatal(err)
	}
	runner := &sessionRegistryRunner{}
	registry, err := NewSessionRegistry(runner, repository)
	if err != nil {
		t.Fatal(err)
	}

	authorRoot := mustSessionArtifactRoot(t, root)
	author, err := registry.Acquire(testSessionRequest(featureID, RoleSpecAuthor, root, authorRoot))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Release(featureID, RoleSpecAuthor); err != nil {
		t.Fatal(err)
	}
	replacementRoot := mustSessionArtifactRoot(t, root)
	reused, err := registry.Acquire(testSessionRequest(featureID, RoleSpecAuthor, root, replacementRoot))
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.Thread != author.Thread || reused.ArtifactRoot != authorRoot {
		t.Fatalf("reused author = %#v, first = %#v", reused, author)
	}
	if _, err := os.Stat(replacementRoot); !os.IsNotExist(err) {
		t.Fatalf("unused replacement artifact root remains: %v", err)
	}
	if err := registry.Release(featureID, RoleSpecAuthor); err != nil {
		t.Fatal(err)
	}

	reviewerRoot := mustSessionArtifactRoot(t, root)
	reviewer, err := registry.Acquire(testSessionRequest(featureID, RoleSpecReviewer, root, reviewerRoot))
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.Thread == author.Thread || len(runner.configs) != 2 {
		t.Fatalf("author/reviewer shared a thread: author=%v reviewer=%v configs=%d", author.Thread, reviewer.Thread, len(runner.configs))
	}
	if err := registry.Release(featureID, RoleSpecReviewer); err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleIntentAuthor, RolePlanAuthor} {
		rootForRole := mustSessionArtifactRoot(t, root)
		session, acquireErr := registry.Acquire(testSessionRequest(featureID, role, root, rootForRole))
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		if session.Thread == author.Thread || session.Thread == reviewer.Thread {
			t.Fatalf("role %s shared another role's thread", role)
		}
		if releaseErr := registry.Release(featureID, role); releaseErr != nil {
			t.Fatal(releaseErr)
		}
	}
	if len(runner.configs) != 4 {
		t.Fatalf("one thread per exercised role = %d, want 4", len(runner.configs))
	}

	if err := registry.CloseFeature(featureID); err != nil {
		t.Fatal(err)
	}
	feature, err := repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if feature.State.Status() != FlowActive {
		t.Fatalf("session close changed flow status: %s", feature.State.Status())
	}
	if got := feature.Journal[len(feature.Journal)-1]; got.Kind != MemLogSession || !strings.Contains(got.Body, "sessions closed") {
		t.Fatalf("session close journal entry = %#v", got)
	}
	journalEntries := len(feature.Journal)
	if err := registry.CloseFeature(featureID); err != nil {
		t.Fatal(err)
	}
	feature, err = repository.Load(featureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(feature.Journal) != journalEntries {
		t.Fatalf("idempotent close appended another transition: before=%d after=%d", journalEntries, len(feature.Journal))
	}
	for _, artifactRoot := range []string{authorRoot, reviewerRoot} {
		if _, err := os.Stat(artifactRoot); !os.IsNotExist(err) {
			t.Fatalf("artifact root remains after close: %s: %v", artifactRoot, err)
		}
	}
}

func TestFeatureSessionExitDiscardsPendingRevisionWithoutCancelingFlow(t *testing.T) {
	for _, exit := range []string{"exit", "eof", "ctrl-c"} {
		t.Run(exit, func(t *testing.T) {
			root := initRepository(t)
			repository := newTestFeatureRepository(t, root)
			featureID := "2026-08-30-session-" + exit
			if _, err := repository.Create(CreateFeatureRequest{FeatureID: featureID, Brief: "Preserve published intent", At: repositoryTestTime}); err != nil {
				t.Fatal(err)
			}
			runner := &sessionRegistryRunner{}
			registry, err := NewSessionRegistry(runner, repository)
			if err != nil {
				t.Fatal(err)
			}
			artifactRoot := mustSessionArtifactRoot(t, root)
			if _, err := registry.Acquire(testSessionRequest(featureID, RoleIntentAuthor, root, artifactRoot)); err != nil {
				t.Fatal(err)
			}
			published := validIntent("Published intent")
			if err := os.WriteFile(filepath.Join(artifactRoot, "intent.md"), []byte(published), 0o600); err != nil {
				t.Fatal(err)
			}
			publication, err := repository.PublishAuthorDraft(DraftArtifactRequest{FeatureID: featureID, Stage: StageIntent, ArtifactRoot: artifactRoot, Mode: ValidateDraft})
			if err != nil || !publication.Published {
				t.Fatalf("publish = %#v, %v", publication, err)
			}
			pending := validIntent("Pending intent revision")
			if err := os.WriteFile(filepath.Join(artifactRoot, "intent.md"), []byte(pending), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := repository.InspectAuthorDraft(DraftArtifactRequest{
				FeatureID: featureID, Stage: StageIntent, ArtifactRoot: artifactRoot, Mode: ValidateDraft,
			}); err != nil {
				t.Fatal(err)
			}

			if err := registry.CloseFeature(featureID); err != nil {
				t.Fatal(err)
			}
			resumed, err := repository.Load(featureID)
			if err != nil {
				t.Fatal(err)
			}
			if resumed.State.Status() != FlowActive || resumed.Documents[StageIntent].Hash != publication.Feature.Documents[StageIntent].Hash || string(resumed.Documents[StageIntent].Content) != published {
				t.Fatalf("exit did not preserve stable publication: %#v", resumed)
			}
			if _, err := os.Stat(artifactRoot); !os.IsNotExist(err) {
				t.Fatalf("pending artifact root remains: %v", err)
			}
			for _, entry := range resumed.Journal {
				if strings.Contains(strings.ToLower(entry.Body), "flow canceled") {
					t.Fatalf("exit recorded cancellation: %#v", entry)
				}
			}
			foundPending := false
			for _, entry := range resumed.Journal {
				foundPending = foundPending || (entry.Kind == MemLogAttempt && strings.Contains(entry.Body, hash([]byte(pending))))
			}
			if !foundPending {
				t.Fatal("pending revision trace was not retained in mem-log")
			}
		})
	}
}

type sessionRegistryRunner struct {
	configs []agentruntime.ThreadConfig
	closed  []agentruntime.Thread
	turns   int
}

func (r *sessionRegistryRunner) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.configs = append(r.configs, config.Clone())
	return len(r.configs), nil
}

func (r *sessionRegistryRunner) RunTurn(agentruntime.Thread, string) (json.RawMessage, error) {
	r.turns++
	return json.RawMessage(`{"kind":"message","message":"ok","decisions":[]}`), nil
}

func (r *sessionRegistryRunner) CloseThread(thread agentruntime.Thread) error {
	r.closed = append(r.closed, thread)
	return nil
}

func testSessionRequest(featureID string, role Role, workspace, artifactRoot string) SessionRequest {
	return SessionRequest{FeatureID: featureID, Role: role, Config: agentruntime.ThreadConfig{
		BootstrapInstructions: "role prompt", OutputSchema: DialogueSchema(), Workspace: workspace, ArtifactRoot: artifactRoot,
	}}
}

func mustSessionArtifactRoot(t *testing.T, workspace string) string {
	t.Helper()
	root, err := CreateArtifactRoot(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeArtifact(root) })
	return root
}
