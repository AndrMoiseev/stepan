package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func TestMain(main *testing.M) {
	if os.Getenv("STEPAN_MAIN_QWEN_FAKE") == "1" {
		os.Exit(runCompositionQwenFake())
	}
	os.Exit(main.Run())
}

type compositionACPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type compositionACPObservation struct {
	PID        int      `json:"pid"`
	SessionID  string   `json:"session_id"`
	Generation string   `json:"generation"`
	Role       string   `json:"role"`
	Prompts    []string `json:"prompts"`
}

func runCompositionQwenFake() int {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	read := func() (compositionACPRequest, bool) {
		if !scanner.Scan() {
			return compositionACPRequest{}, false
		}
		var request compositionACPRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return compositionACPRequest{}, false
		}
		return request, true
	}
	respond := func(id json.RawMessage, result any) bool {
		return encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result}) == nil
	}
	notify := func(method string, params any) bool {
		return encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": method, "params": params}) == nil
	}

	initialize, ok := read()
	if !ok || initialize.Method != "initialize" || !respond(initialize.ID, map[string]any{
		"protocolVersion": 1,
		"agentCapabilities": map[string]any{
			"promptCapabilities":  map[string]any{},
			"sessionCapabilities": map[string]any{},
		},
		"agentInfo":   map[string]string{"name": "task-seven-fake", "version": "test"},
		"authMethods": []any{},
	}) {
		return 81
	}
	sessionRequest, ok := read()
	if !ok || sessionRequest.Method != "session/new" {
		return 82
	}
	generation := os.Getenv("STEPAN_MAIN_QWEN_GENERATION")
	sessionID := fmt.Sprintf("%s-session-%d", generation, os.Getpid())
	if !respond(sessionRequest.ID, map[string]string{"sessionId": sessionID}) {
		return 83
	}
	observation := compositionACPObservation{PID: os.Getpid(), SessionID: sessionID, Generation: generation}
	artifactRoot := compositionArtifactRoot(os.Args[1:])
	turn := 0
	for {
		request, ok := read()
		if !ok {
			return 0
		}
		if request.Method != "session/prompt" {
			return 84
		}
		var params struct {
			SessionID string `json:"sessionId"`
			Prompt    []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if json.Unmarshal(request.Params, &params) != nil || params.SessionID != sessionID || len(params.Prompt) != 1 {
			return 85
		}
		prompt := params.Prompt[0].Text
		if observation.Role == "" {
			observation.Role = compositionRole(prompt)
		}
		observation.Prompts = append(observation.Prompts, prompt)
		if !writeCompositionObservation(observation) {
			return 86
		}
		turn++
		candidate, err := compositionResponse(observation.Role, turn, artifactRoot)
		if err != nil {
			return 87
		}
		if !notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"messageId":     fmt.Sprintf("message-%d", turn),
				"content":       map[string]string{"type": "text", "text": candidate},
			},
		}) || !respond(request.ID, map[string]string{"stopReason": "end_turn"}) {
			return 88
		}
	}
}

func compositionArtifactRoot(args []string) string {
	for index, arg := range args {
		if arg == "--include-directories" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func compositionRole(prompt string) string {
	if strings.Contains(prompt, `"feature_id"`) {
		return "feature-id"
	}
	for _, role := range []string{"intent-author", "spec-author", "spec-reviewer", "plan-author", "plan-reviewer"} {
		if strings.Contains(prompt, "[roles/"+role+"]") {
			return role
		}
	}
	return "unknown"
}

func compositionResponse(role string, turn int, artifactRoot string) (string, error) {
	artifact := func(name, body string) (string, error) {
		if artifactRoot == "" {
			return "", errors.New("missing artifact root")
		}
		if err := os.WriteFile(filepath.Join(artifactRoot, name), []byte(body), 0o600); err != nil {
			return "", err
		}
		return `{"kind":"artifact","message":"","decisions":[]}`, nil
	}
	message := func(text string) (string, error) {
		data, err := json.Marshal(map[string]any{"kind": "message", "message": text, "decisions": []any{}})
		return string(data), err
	}
	switch role {
	case "feature-id":
		return `{"feature_id":"qwen-provider-parity"}`, nil
	case "intent-author":
		if turn == 1 {
			return message("What durable outcome should the intent guarantee?")
		}
		return artifact("intent.md", qwenFlowIntent)
	case "spec-author":
		if turn == 1 {
			return message("Which durable requirements should the specification cover?")
		}
		body := qwenFlowSpec
		if turn > 2 {
			body = strings.Replace(body, "survives restart.", "survives restart and explicit review recheck.", 1)
		}
		return artifact("spec.md", body)
	case "spec-reviewer":
		if turn == 1 {
			return artifact("review.md", qwenPendingReview("SPEC-F-001"))
		}
		return artifact("review.md", qwenResolvedReview("SPEC-F-001"))
	case "plan-author":
		if turn == 1 {
			return message("Which tasks prove the accepted specification?")
		}
		body := qwenFlowPlan
		if turn > 2 {
			body = strings.Replace(body, "completes the flow.", "completes the reviewed flow.", 1)
		}
		return artifact("plan.md", body)
	case "plan-reviewer":
		if turn == 1 {
			return artifact("review.md", qwenPendingReview("PLAN-F-001"))
		}
		return artifact("review.md", qwenResolvedReview("PLAN-F-001"))
	default:
		return "", fmt.Errorf("unknown role %q", role)
	}
}

func writeCompositionObservation(observation compositionACPObservation) bool {
	directory := os.Getenv("STEPAN_MAIN_QWEN_OBSERVATIONS")
	if directory == "" {
		return false
	}
	data, err := json.Marshal(observation)
	if err != nil {
		return false
	}
	return os.WriteFile(filepath.Join(directory, fmt.Sprintf("%d.json", observation.PID)), data, 0o600) == nil
}

const qwenFlowIntent = `# Qwen provider parity

## Scope

Exercise the complete durable planning flow.

## Open questions
`

const qwenFlowSpec = `# Specification

## REQ-001 — Durable flow

The provider-neutral flow persists its state.

## DEC-001 — Durable context

Use documents, reviews, state, and mem-log.

## AC-001 — Resume

Traces: REQ-001

The state survives restart.

## Open questions
`

const qwenFlowPlan = `# Plan

## TASK-001 — Verify provider parity

Traces: REQ-001, DEC-001

### Test scenario — complete flow

Traces: AC-001

The automated suite completes the flow.

## Open questions
`

func qwenPendingReview(id string) string {
	return "# Review\n\n## " + id + " — Durable ambiguity\n\n" +
		"Severity: major\nStatus: open\n" +
		"Problem: The document leaves durable provider parity insufficiently explicit.\n" +
		"Location: whole document\nRecommendation: Clarify durable provider parity.\n" +
		"Decision: pending\nDecided-by: none\nRationale:\n"
}

func qwenResolvedReview(id string) string {
	return "# Review\n\n## " + id + " — Durable ambiguity\n\n" +
		"Severity: major\nStatus: resolved\n" +
		"Problem: The document leaves durable provider parity insufficiently explicit.\n" +
		"Location: whole document\nRecommendation: Clarify durable provider parity.\n" +
		"Resolution: The revised document now makes durable provider parity explicit.\n" +
		"Decision: fix\nDecided-by: user\nRationale: User accepted the pending recommendation with /apply.\n"
}

func TestQwenCompositionFullFlowGitPolicyAndDurableResume(t *testing.T) {
	root := initializeCompositionRepository(t)
	observations := t.TempDir()
	t.Setenv("STEPAN_MAIN_QWEN_FAKE", "1")
	t.Setenv("STEPAN_MAIN_QWEN_OBSERVATIONS", observations)
	t.Setenv("STEPAN_MAIN_QWEN_GENERATION", "before-resume")
	config := agentConfig{kind: agentQwen, executable: absoluteCompositionExecutable(t)}

	application, registry, session := newQwenCompositionApplication(t, root, config)
	t.Cleanup(func() {
		_, _ = application.Close()
		_ = registry.Close()
		_ = session.Close()
	})
	progress, err := application.StartFeature("Prove Qwen provider parity")
	if err != nil {
		t.Fatal(err)
	}
	featureID := progress.FeatureID
	progress = submitComposition(t, application, "write the intent")
	assertCompositionState(t, progress, specflow.StageIntent, specflow.StagePublished, specflow.ReviewNotStarted)

	outside := filepath.Join(root, "README.md")
	if err := os.WriteFile(outside, []byte("blocked change must survive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := submitComposition(t, application, "/approve")
	if blocked.Event != specflow.ControllerApprovalBlocked || len(blocked.Blocking) != 1 || blocked.Blocking[0].Path != "README.md" {
		t.Fatalf("Qwen approval blockers = %#v", blocked)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "blocked change must survive\n" {
		t.Fatalf("blocked file was rolled back: %q, %v", data, err)
	}
	if err := os.WriteFile(outside, []byte("composition fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	progress = submitComposition(t, application, "/approve")
	assertCompositionState(t, progress, specflow.StageSpec, specflow.StageDrafting, specflow.ReviewNotStarted)
	progress = submitComposition(t, application, "write the specification")
	assertCompositionState(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewNotStarted)
	progress = submitComposition(t, application, "/review")
	assertCompositionState(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewAwaitingDecisions)
	progress = submitComposition(t, application, "/apply")
	assertCompositionState(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewNotStarted)
	progress = submitComposition(t, application, "/review")
	assertCompositionState(t, progress, specflow.StageSpec, specflow.StagePublished, specflow.ReviewCompleted)
	progress = submitComposition(t, application, "/approve")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StageDrafting, specflow.ReviewNotStarted)

	before := readCompositionObservations(t, observations)
	if _, err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv("STEPAN_MAIN_QWEN_GENERATION", "after-resume")
	resumedApplication, resumedRegistry, resumedSession := newQwenCompositionApplication(t, root, config)
	t.Cleanup(func() {
		_, _ = resumedApplication.Close()
		_ = resumedRegistry.Close()
		_ = resumedSession.Close()
	})
	progress, err = resumedApplication.Resume(featureID)
	if err != nil {
		t.Fatal(err)
	}
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StageDrafting, specflow.ReviewNotStarted)
	progress = submitComposition(t, resumedApplication, "write the plan")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewNotStarted)
	progress = submitComposition(t, resumedApplication, "/review")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewAwaitingDecisions)
	progress = submitComposition(t, resumedApplication, "/apply")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewNotStarted)
	progress = submitComposition(t, resumedApplication, "/review")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StagePublished, specflow.ReviewCompleted)
	progress = submitComposition(t, resumedApplication, "/approve")
	assertCompositionState(t, progress, specflow.StagePlan, specflow.StageCommitted, specflow.ReviewCompleted)

	after := readCompositionObservations(t, observations)
	freshPlanAuthor := false
	for _, observation := range after {
		if observation.Generation == "after-resume" && observation.Role == "plan-author" {
			freshPlanAuthor = true
			if _, reused := before[observation.SessionID]; reused {
				t.Fatalf("resume reused Qwen session %q", observation.SessionID)
			}
			if len(observation.Prompts) == 0 {
				t.Fatalf("resumed plan author has no durable bootstrap prompt: %#v", observation)
			}
			for _, required := range []string{"intent.md", "spec.md", "mem-log.md"} {
				if !strings.Contains(observation.Prompts[0], required) {
					t.Fatalf("resumed plan-author prompt lacks durable %q:\n%s", required, observation.Prompts[0])
				}
			}
		}
	}
	if !freshPlanAuthor {
		t.Fatalf("resume did not start a fresh Qwen plan-author process: %#v", after)
	}
	statePath := filepath.Join(root, "docs", "changes", "features", featureID, "state.json")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderNeutralStateKeys(t, state)
	for sessionID := range before {
		if strings.Contains(string(state), sessionID) {
			t.Fatalf("state retained ephemeral Qwen session %q", sessionID)
		}
	}
	reviews, err := filepath.Glob(filepath.Join(root, "docs", "changes", "features", featureID, "reviews", "*.md"))
	if err != nil || len(reviews) != 4 {
		t.Fatalf("review reports = %#v, %v", reviews, err)
	}
	for _, review := range reviews {
		data, readErr := os.ReadFile(review)
		if readErr != nil || !strings.Contains(string(data), `provider: "qwen"`) || !strings.Contains(string(data), `model: "default"`) {
			t.Fatalf("review runtime front matter %s: %v\n%s", review, readErr, data)
		}
	}
}

func TestQwenCompositionCleanCreateGateDoesNotRollbackDirtyFiles(t *testing.T) {
	root := initializeCompositionRepository(t)
	observations := t.TempDir()
	t.Setenv("STEPAN_MAIN_QWEN_FAKE", "1")
	t.Setenv("STEPAN_MAIN_QWEN_OBSERVATIONS", observations)
	t.Setenv("STEPAN_MAIN_QWEN_GENERATION", "dirty-create")
	outside := filepath.Join(root, "README.md")
	if err := os.WriteFile(outside, []byte("dirty create marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := agentConfig{kind: agentQwen, executable: absoluteCompositionExecutable(t)}
	application, registry, session := newQwenCompositionApplication(t, root, config)
	t.Cleanup(func() { _ = registry.Close(); _ = session.Close() })
	progress, err := application.StartFeature("must honor clean create")
	if !errors.Is(err, specflow.ErrRepositoryBlocked) {
		t.Fatalf("dirty Qwen create = %#v, %v", progress, err)
	}
	if data, readErr := os.ReadFile(outside); readErr != nil || string(data) != "dirty create marker\n" {
		t.Fatalf("dirty create file was rolled back: %q, %v", data, readErr)
	}
	featureRoot := filepath.Join(root, "docs", "changes", "features")
	if entries, readErr := os.ReadDir(featureRoot); readErr == nil && len(entries) != 0 {
		t.Fatalf("blocked create persisted a feature: %#v", entries)
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
}

func newQwenCompositionApplication(t *testing.T, root string, config agentConfig) (*specflow.ApplicationController, *specflow.SessionRegistry, *specflow.Session) {
	t.Helper()
	session := specflow.NewSession(runtimeFactory(config, root))
	application, registry, err := composePlanningFlow(root, session, config)
	if err != nil {
		t.Fatal(err)
	}
	return application, registry, session
}

func submitComposition(t *testing.T, application *specflow.ApplicationController, input string) specflow.Progress {
	t.Helper()
	progress, err := application.Submit(input)
	if err != nil {
		t.Fatalf("submit %q: %v", input, err)
	}
	return progress
}

func assertCompositionState(t *testing.T, progress specflow.Progress, stage specflow.Stage, status specflow.StageStatus, review specflow.ReviewStatus) {
	t.Helper()
	if progress.CurrentStage != stage || progress.StageStatus != status || progress.ReviewStatus != review {
		t.Fatalf("progress = %#v, want %s/%s/%s", progress, stage, status, review)
	}
}

func absoluteCompositionExecutable(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func readCompositionObservations(t *testing.T, directory string) map[string]compositionACPObservation {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]compositionACPObservation, len(entries))
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var observation compositionACPObservation
		if json.Unmarshal(data, &observation) != nil || observation.SessionID == "" {
			t.Fatalf("invalid Qwen observation %s: %s", entry.Name(), data)
		}
		result[observation.SessionID] = observation
	}
	return result
}

func assertProviderNeutralStateKeys(t *testing.T, state []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(state, &value); err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"provider": true, "model": true, "process": true, "process_id": true, "session": true, "session_id": true, "prompt": true, "capabilities": true}
	var inspect func(any)
	inspect = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if forbidden[strings.ToLower(key)] {
					t.Fatalf("state contains ephemeral key %q: %s", key, state)
				}
				inspect(child)
			}
		case []any:
			for _, child := range typed {
				inspect(child)
			}
		}
	}
	inspect(value)
}
