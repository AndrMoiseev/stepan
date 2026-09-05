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

	"github.com/AndrMoiseev/stepan/internal/agentruntime/conformance"
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
	PID               int      `json:"pid"`
	SessionID         string   `json:"session_id"`
	Generation        string   `json:"generation"`
	Role              string   `json:"role"`
	Prompts           []string `json:"prompts"`
	CapabilityPayload string   `json:"capability_payload"`
	StatusPayload     string   `json:"status_payload"`
	ReportedModel     string   `json:"reported_model"`
}

func runCompositionQwenFake() int {
	generation := os.Getenv("STEPAN_MAIN_QWEN_GENERATION")
	capabilityPayload := generation + "-ephemeral-capability-payload"
	statusPayload := generation + "-ephemeral-status-payload"
	reportedModel := generation + "-non-default-reported-model"
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
			"_meta":               map[string]string{"fixtureCapability": capabilityPayload},
		},
		"agentInfo":   map[string]string{"name": "task-seven-fake", "version": "test"},
		"authMethods": []any{},
		"_meta": map[string]string{
			"fixtureStatus": statusPayload,
			"activeModel":   reportedModel,
		},
	}) {
		return 81
	}
	sessionRequest, ok := read()
	if !ok || sessionRequest.Method != "session/new" {
		return 82
	}
	sessionID := fmt.Sprintf("%s-session-%d", generation, os.Getpid())
	if !respond(sessionRequest.ID, map[string]any{
		"sessionId": sessionID,
		"_meta": map[string]string{
			"fixtureStatus": statusPayload,
			"activeModel":   reportedModel,
		},
	}) {
		return 83
	}
	observation := compositionACPObservation{
		PID: os.Getpid(), SessionID: sessionID, Generation: generation,
		CapabilityPayload: capabilityPayload, StatusPayload: statusPayload, ReportedModel: reportedModel,
	}
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
		return artifact("intent.md", conformance.ApplicationIntent())
	case "spec-author":
		if turn == 1 {
			return message("Which durable requirements should the specification cover?")
		}
		body := conformance.ApplicationSpec(false)
		if turn > 2 {
			body = conformance.ApplicationSpec(true)
		}
		return artifact("spec.md", body)
	case "spec-reviewer":
		if turn == 1 {
			return artifact("review.md", conformance.ReviewArtifact("SPEC-F-001", false))
		}
		return artifact("review.md", conformance.ReviewArtifact("SPEC-F-001", true))
	case "plan-author":
		if turn == 1 {
			return message("Which tasks prove the accepted specification?")
		}
		body := conformance.ApplicationPlan(false)
		if turn > 2 {
			body = conformance.ApplicationPlan(true)
		}
		return artifact("plan.md", body)
	case "plan-reviewer":
		if turn == 1 {
			return artifact("review.md", conformance.ReviewArtifact("PLAN-F-001", false))
		}
		return artifact("review.md", conformance.ReviewArtifact("PLAN-F-001", true))
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
	beforeModels := make(map[string]bool)
	for _, observation := range before {
		if observation.PID == 0 || observation.SessionID == "" || len(observation.Prompts) == 0 || observation.CapabilityPayload == "" || observation.StatusPayload == "" || observation.ReportedModel == "" || observation.ReportedModel == "default" {
			t.Fatalf("incomplete pre-resume ephemeral observation: %#v", observation)
		}
		beforeModels[observation.ReportedModel] = true
	}
	featureRoot := filepath.Join(root, "docs", "changes", "features", featureID)
	statePath := filepath.Join(featureRoot, "state.json")
	beforeState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprints := durableFingerprints(t, beforeState)
	for _, relative := range []string{"intent.md", "spec.md", "state.json", "mem-log.md", "reviews/spec-001.md", "reviews/spec-002.md"} {
		if _, err := os.Stat(filepath.Join(featureRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("durable resume input %s: %v", relative, err)
		}
	}
	assertNoEphemeralRuntimeData(t, beforeState, before)
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
			if observation.PID == 0 || observation.CapabilityPayload == "" || observation.StatusPayload == "" || observation.ReportedModel == "" || observation.ReportedModel == "default" || beforeModels[observation.ReportedModel] {
				t.Fatalf("resume did not use fresh ephemeral capability/status/model identity: %#v", observation)
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
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderNeutralStateKeys(t, state)
	assertNoEphemeralRuntimeData(t, state, before)
	assertNoEphemeralRuntimeData(t, state, after)
	afterFingerprints := durableFingerprints(t, state)
	for path, fingerprint := range beforeFingerprints {
		if got, ok := afterFingerprints[path]; !ok || got != fingerprint {
			t.Fatalf("durable fingerprint %s changed across runtime identity: got %q want %q", path, got, fingerprint)
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
		assertNoEphemeralRuntimeData(t, data, before)
		assertNoEphemeralRuntimeData(t, data, after)
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
		case string:
			if typed == "qwen" || typed == "default" {
				t.Fatalf("state contains runtime identity value %q: %s", typed, state)
			}
		}
	}
	inspect(value)
}

func assertNoEphemeralRuntimeData(t *testing.T, data []byte, observations map[string]compositionACPObservation) {
	t.Helper()
	pids := make(map[int]bool)
	for _, observation := range observations {
		pids[observation.PID] = true
		for _, forbidden := range append([]string{
			observation.SessionID,
			observation.CapabilityPayload,
			observation.StatusPayload,
			observation.ReportedModel,
		}, observation.Prompts...) {
			if forbidden != "" && strings.Contains(string(data), forbidden) {
				t.Fatalf("durable data retained ephemeral runtime value %q", forbidden)
			}
		}
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return
	}
	var inspect func(any)
	inspect = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for _, child := range typed {
				inspect(child)
			}
		case []any:
			for _, child := range typed {
				inspect(child)
			}
		case float64:
			if typed == float64(int(typed)) && pids[int(typed)] {
				t.Fatalf("durable JSON retained ephemeral process ID %d", int(typed))
			}
		}
	}
	inspect(value)
}

func durableFingerprints(t *testing.T, state []byte) map[string]string {
	t.Helper()
	var value any
	if err := json.Unmarshal(state, &value); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string)
	var inspect func(string, any)
	inspect = func(path string, current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				childPath := path + "/" + key
				if strings.Contains(strings.ToLower(key), "fingerprint") {
					encoded, err := json.Marshal(child)
					if err != nil {
						t.Fatal(err)
					}
					result[childPath] = string(encoded)
				}
				inspect(childPath, child)
			}
		case []any:
			for index, child := range typed {
				inspect(fmt.Sprintf("%s/%d", path, index), child)
			}
		}
	}
	inspect("", value)
	if len(result) == 0 {
		t.Fatal("state contains no durable review fingerprints")
	}
	return result
}
