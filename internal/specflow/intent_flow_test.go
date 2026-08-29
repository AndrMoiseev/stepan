package specflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestIntentFlowPublishesThenReviewsAndApproves(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &intentRunner{steps: []intentStep{
		{output: `{"feature_id":"intent-flow"}`},
		{output: `{"kind":"message","message":"What outcome?","decisions":[]}`},
		{output: `{"kind":"draft","decisions":[{"author":"user","decision":"Keep it small","rationale":"brief","alternatives":[],"supersedes":[]}]}`, draft: "# Intent\n\nFirst\n"},
		{output: `{"kind":"draft","decisions":[]}`, draft: "# Intent\n\nSecond\n"},
	}}
	c := NewController(repo, runner)
	c.now = func() time.Time { return time.Date(2026, 8, 29, 0, 0, 0, 0, time.Local) }
	p, err := c.StartFeature("Build intent flow")
	if err != nil || p.State != StateDialoguing || p.Message != "What outcome?" {
		t.Fatalf("start = %#v, %v", p, err)
	}
	p, err = c.Submit("Small and safe")
	if err != nil || p.State != StateIntentPublished {
		t.Fatalf("first draft = %#v, %v", p, err)
	}
	intent := filepath.Join(repo, "docs", "changes", "features", "2026-08-29-intent-flow", "intent.md")
	if data, err := os.ReadFile(intent); err != nil || string(data) != "# Intent\n\nFirst\n" {
		t.Fatalf("published = %q, %v", data, err)
	}
	p, err = c.Submit("Please refine")
	if err != nil || p.State != StateAwaitingReview || !strings.Contains(p.Diff, "Second") {
		t.Fatalf("review = %#v, %v", p, err)
	}
	if _, err = c.Review(ReviewReject); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(intent); string(data) != "# Intent\n\nFirst\n" {
		t.Fatalf("reject changed intent: %q", data)
	}
	if _, err = c.Approve(); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(intent), "mem-log.md"))
	if err != nil || !strings.Contains(string(log), "intent approved") {
		t.Fatalf("log=%q,%v", log, err)
	}
	if runner.threads != 2 {
		t.Fatalf("threads=%d, want ID and main", runner.threads)
	}
}

func TestEnvelopeRejectsInvalidCombinations(t *testing.T) {
	for _, value := range []string{`{"kind":"draft","message":"no","decisions":[]}`, `{"kind":"message","decisions":[]}`, `{"kind":"message","message":"ok","decisions":[{"author":"bad","decision":"x","rationale":"y","alternatives":[],"supersedes":[]}]}`, `{"kind":"message","message":"ok","decisions":[],"extra":1}`} {
		if _, err := DecodeEnvelope([]byte(value)); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}

func TestEnvelopeAcceptsCodexEmptyDraftMessagePlaceholder(t *testing.T) {
	if _, err := DecodeEnvelope([]byte(`{"kind":"draft","message":"","decisions":[]}`)); err != nil {
		t.Fatalf("empty draft placeholder = %v", err)
	}
}

func TestReviewApplyRejectsDraftChangedAfterDiff(t *testing.T) {
	c, runner, intent := testController(t, []intentStep{
		{output: `{"feature_id":"safe-apply"}`},
		{output: `{"kind":"draft","decisions":[]}`, draft: "first\n"},
		{output: `{"kind":"draft","decisions":[]}`, draft: "reviewed\n"},
	})
	if _, err := c.StartFeature("Keep the reviewed bytes"); err != nil {
		t.Fatal(err)
	}
	if p, err := c.Submit("revise"); err != nil || p.State != StateAwaitingReview {
		t.Fatalf("review = %#v, %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(runner.artifact, "intent.md"), []byte("unreviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(ReviewApply); err == nil || !strings.Contains(err.Error(), "changed before apply") {
		t.Fatalf("apply error = %v", err)
	}
	if data, err := os.ReadFile(intent); err != nil || string(data) != "first\n" {
		t.Fatalf("published intent = %q, %v", data, err)
	}
}

func TestReviewReworkThenApplyClosesThreadAndCleansArtifact(t *testing.T) {
	c, runner, intent := testController(t, []intentStep{
		{output: `{"feature_id":"rework"}`},
		{output: `{"kind":"draft","decisions":[]}`, draft: "first\n"},
		{output: `{"kind":"draft","decisions":[]}`, draft: "second\n"},
		{output: `{"kind":"draft","decisions":[]}`, draft: "third\n"},
	})
	if _, err := c.StartFeature("Rework flow"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit("second"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Review(ReviewRework); err != nil {
		t.Fatal(err)
	}
	if p, err := c.Submit("make it clearer"); err != nil || p.State != StateAwaitingReview || !strings.Contains(p.Diff, "third") {
		t.Fatalf("reworked draft = %#v, %v", p, err)
	}
	if _, err := c.Review(ReviewApply); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(intent); err != nil || string(data) != "third\n" {
		t.Fatalf("applied intent = %q, %v", data, err)
	}
	if _, err := c.Approve(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(runner.artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact remains after apply: %v", err)
	}
	if len(runner.closed) != 2 || runner.closed[1] != 2 {
		t.Fatalf("closed threads = %v", runner.closed)
	}
}

func TestCancelLogsClosesThreadAndCleansArtifact(t *testing.T) {
	c, runner, intent := testController(t, []intentStep{
		{output: `{"feature_id":"cancel"}`},
		{output: `{"kind":"message","message":"Question","decisions":[]}`},
	})
	if _, err := c.StartFeature("Cancel me"); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(filepath.Dir(intent), "mem-log.md")
	c.Cancel()
	if _, err := os.Stat(runner.artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact remains after cancel: %v", err)
	}
	data, err := os.ReadFile(journal)
	if err != nil || !strings.Contains(string(data), "flow canceled") {
		t.Fatalf("journal = %q, %v", data, err)
	}
	if len(runner.closed) != 2 || runner.closed[1] != 2 {
		t.Fatalf("closed threads = %v", runner.closed)
	}
}

func TestJournalRejectsInvalidDecisionBatchWithoutPartialAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mem-log.md")
	journal, err := NewJournal(path, "2026-08-29-journal", "brief")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = journal.Decisions([]Decision{
		{Author: DecisionUser, Decision: "valid", Rationale: "because", Alternatives: []string{}, Supersedes: []int{}},
		{Author: DecisionAgent, Decision: "invalid", Rationale: "because", Alternatives: []string{}, Supersedes: []int{99}},
	})
	if err == nil {
		t.Fatal("invalid batch was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("journal changed: %q, %v", after, err)
	}
}

func TestUnifiedDiffUsesHunksAndContext(t *testing.T) {
	diff := unifiedDiff([]byte("one\ntwo\nthree\n"), []byte("one\nTWO\nthree\n"))
	for _, required := range []string{"@@ -1,3 +1,3 @@", " one", "-two", "+TWO", " three"} {
		if !strings.Contains(diff, required) {
			t.Fatalf("diff missing %q:\n%s", required, diff)
		}
	}
}

func TestFeatureIDPromptRequiresSemanticShortID(t *testing.T) {
	prompt := FeatureIDPrompt("Add Apple Silicon support")
	for _, required := range []string{"short", "semantic", "Apple Silicon support"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
	}
}

func TestFlowEnvelopeSchemaKeepsDecisionReferenceAtDocumentRoot(t *testing.T) {
	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(FlowEnvelopeSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Defs["decision"]) == 0 {
		t.Fatalf("FlowEnvelopeSchema has no root decision definition: %s", FlowEnvelopeSchema())
	}
}

func testController(t *testing.T, steps []intentStep) (*Controller, *intentRunner, string) {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &intentRunner{steps: steps}
	c := NewController(repo, runner)
	c.now = func() time.Time { return time.Date(2026, 8, 29, 0, 0, 0, 0, time.Local) }
	id, err := DecodeFeatureID([]byte(steps[0].output))
	if err != nil {
		t.Fatal(err)
	}
	return c, runner, filepath.Join(repo, "docs", "changes", "features", "2026-08-29-"+id.FeatureID, "intent.md")
}

type intentStep struct {
	output string
	draft  string
}
type intentRunner struct {
	steps          []intentStep
	turns, threads int
	artifact       string
	closed         []int
}

func (r *intentRunner) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.threads++
	if config.ArtifactRoot != "" {
		r.artifact = config.ArtifactRoot
	}
	return r.threads, nil
}
func (r *intentRunner) RunTurn(_ agentruntime.Thread, _ string) (json.RawMessage, error) {
	if r.turns >= len(r.steps) {
		return nil, fmt.Errorf("unexpected turn")
	}
	step := r.steps[r.turns]
	r.turns++
	if step.draft != "" {
		if err := os.WriteFile(filepath.Join(r.artifact, "intent.md"), []byte(step.draft), 0o600); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(step.output), nil
}
func (r *intentRunner) CloseThread(thread agentruntime.Thread) error {
	r.closed = append(r.closed, thread.(int))
	return nil
}
