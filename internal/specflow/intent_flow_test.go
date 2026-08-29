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

type intentStep struct {
	output string
	draft  string
}
type intentRunner struct {
	steps          []intentStep
	turns, threads int
	artifact       string
}

func (r *intentRunner) StartThread(configs ...agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	r.threads++
	if len(configs) == 1 && configs[0].ArtifactRoot != "" {
		r.artifact = configs[0].ArtifactRoot
	}
	return r.threads, nil
}
func (r *intentRunner) RunTurn(_ agentruntime.Thread, _ string, _ ...agentruntime.TurnOptions) (json.RawMessage, error) {
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
