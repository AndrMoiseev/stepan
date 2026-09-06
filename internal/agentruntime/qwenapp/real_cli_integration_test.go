//go:build qwen_real_cli && (windows || darwin)

package qwenapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

const (
	realCLIMarkerEnv = "STEPAN_QWEN_REAL_CLI"
	realCLINameEnv   = "STEPAN_QWEN_AGENT_CLI_NAME"
	realCLIResultEnv = "STEPAN_QWEN_RESULT"
	realCLITimeout   = 90 * time.Second
)

type realCLIAssertion struct {
	Status string `json:"status"`
}

type realCLIReadObservation struct {
	Outcome               string `json:"outcome"`
	OSIsolationGuaranteed bool   `json:"os_isolation_guaranteed"`
}

type realCLIResult struct {
	SchemaVersion  int                         `json:"schema_version"`
	Selected       bool                        `json:"selected"`
	Passed         bool                        `json:"passed"`
	Provider       string                      `json:"provider"`
	ExecutableName string                      `json:"executable_name,omitempty"`
	OS             string                      `json:"os"`
	Arch           string                      `json:"arch"`
	Assertions     map[string]realCLIAssertion `json:"assertions"`
	NativeRead     realCLIReadObservation      `json:"native_read"`
	DurationMS     int64                       `json:"duration_ms"`
	FailureClass   string                      `json:"failure_class,omitempty"`
}

type realCLIFixture struct {
	root           string
	workspace      string
	artifactOne    string
	artifactTwo    string
	external       string
	linkEscape     string
	readCanary     string
	executable     string
	executablePath string
}

func TestQwenRealCLIConformance(t *testing.T) {
	started := time.Now()
	result := &realCLIResult{
		SchemaVersion: 1,
		Provider:      "qwen",
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		Assertions:    make(map[string]realCLIAssertion),
		NativeRead: realCLIReadObservation{
			Outcome:               "not_observed",
			OSIsolationGuaranteed: false,
		},
	}
	defer func() {
		result.Passed = result.Selected && !t.Failed()
		result.DurationMS = time.Since(started).Milliseconds()
		if result.Selected && !result.Passed && result.FailureClass == "" {
			result.FailureClass = "contract_assertion"
		}
		writeRealCLIResult(t, result)
	}()

	name := os.Getenv(realCLINameEnv)
	if os.Getenv(realCLIMarkerEnv) != "1" || name == "" {
		result.FailureClass = "not_selected"
		t.Skip("real Qwen CLI integration was not selected by its opt-in runner")
	}
	result.Selected = true
	result.ExecutableName = name

	parsed, err := agentruntime.ParseExecutableName(name)
	if err != nil {
		result.FailureClass = "invalid_executable_name"
		t.Fatalf("invalid explicit executable name")
	}
	resolved, err := parsed.Resolve()
	if err != nil {
		result.FailureClass = "executable_not_found"
		t.Fatalf("explicit executable name is unavailable in PATH")
	}
	fixture := makeRealCLIFixture(t, name, resolved)

	runRealCLIAssertion(t, result, "startup_acp_and_five_tool_surface", func(t *testing.T) {
		testRealCLIStartupAndTools(t, fixture)
	})
	runRealCLIAssertion(t, result, "write_policy_and_single_use_approval", func(t *testing.T) {
		testRealCLIWritePolicy(t, fixture)
	})
	runRealCLIAssertion(t, result, "forbidden_capability_surface", func(t *testing.T) {
		testRealCLIForbiddenSurface(t, fixture)
	})
	runRealCLIAssertion(t, result, "delegated_external_read_denied", func(t *testing.T) {
		context := newFilePolicy(fixture.workspace, fixture.artifactOne).context("session", "turn")
		if _, err := canonicalReadableTarget(context, fixture.readCanary); !errors.Is(err, ErrPermissionDenied) {
			t.Fatal("delegated read accepted a target outside the logical roots")
		}
	})
	runRealCLIAssertion(t, result, "native_read_observation", func(t *testing.T) {
		result.NativeRead.Outcome = observeRealCLINativeRead(t, fixture)
	})
	runRealCLIAssertion(t, result, "feature_flow_rework_close_and_fresh_resume", func(t *testing.T) {
		testRealCLIFeatureFlow(t, fixture)
	})
	runRealCLIAssertion(t, result, realCLIPlatformAssertionName(), func(t *testing.T) {
		testRealCLIPlatformLifecycle(t, fixture)
	})
}

func runRealCLIAssertion(t *testing.T, result *realCLIResult, name string, run func(*testing.T)) {
	t.Helper()
	result.Assertions[name] = realCLIAssertion{Status: "FAIL"}
	if t.Run(name, run) {
		result.Assertions[name] = realCLIAssertion{Status: "PASS"}
	}
}

func makeRealCLIFixture(t *testing.T, name, resolved string) realCLIFixture {
	t.Helper()
	root := t.TempDir()
	fixture := realCLIFixture{
		root:           root,
		workspace:      filepath.Join(root, "workspace"),
		artifactOne:    filepath.Join(root, "artifact-one"),
		artifactTwo:    filepath.Join(root, "artifact-two"),
		external:       filepath.Join(root, "external"),
		executable:     name,
		executablePath: filepath.Clean(resolved),
	}
	for _, directory := range []string{fixture.workspace, fixture.artifactOne, fixture.artifactTwo, fixture.external} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal("create disposable integration fixture")
		}
	}
	fixture.linkEscape = filepath.Join(fixture.artifactOne, "escape")
	if err := makeDirectoryLink(fixture.linkEscape, fixture.external); err != nil {
		t.Fatal("create disposable link-escape fixture")
	}
	fixture.readCanary = filepath.Join(fixture.external, "native-read-canary.txt")
	if err := os.WriteFile(fixture.readCanary, []byte("native-read-"+realCLINonce(t)), 0o600); err != nil {
		t.Fatal("write disposable read canary")
	}
	seedName := "source-" + realCLINonce(t) + ".txt"
	seedContent := "search-token-" + realCLINonce(t)
	if err := os.WriteFile(filepath.Join(fixture.workspace, seedName), []byte(seedContent), 0o600); err != nil {
		t.Fatal("write disposable workspace seed")
	}
	git(t, fixture.workspace, "init", "--quiet")
	git(t, fixture.workspace, "config", "user.name", "Stepan Qwen Acceptance")
	git(t, fixture.workspace, "config", "user.email", "qwen-acceptance@example.invalid")
	git(t, fixture.workspace, "config", "commit.gpgsign", "false")
	git(t, fixture.workspace, "add", seedName)
	git(t, fixture.workspace, "commit", "--quiet", "-m", "acceptance baseline")
	return fixture
}

func testRealCLIStartupAndTools(t *testing.T, fixture realCLIFixture) {
	seedFiles, err := filepath.Glob(filepath.Join(fixture.workspace, "source-*.txt"))
	if err != nil || len(seedFiles) != 1 {
		t.Fatal("inspect disposable workspace seed")
	}
	seedName := filepath.Base(seedFiles[0])
	seed, err := os.ReadFile(seedFiles[0])
	if err != nil {
		t.Fatal("read disposable workspace seed")
	}
	nonce := realCLINonce(t)
	target := filepath.Join(fixture.artifactOne, "tool-probe-"+nonce+".txt")
	schema := constantObjectSchema(map[string]string{
		"read_value": string(seed), "glob_name": seedName, "grep_value": string(seed), "status": "ok",
	})
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"Use only the five configured filesystem tools. Perform every requested operation before answering.")
	backend := realCLIBackend(t, thread)
	assertRealCLIStartup(t, backend, fixture, fixture.artifactOne)

	prompt := fmt.Sprintf("Use read_file to read %q, glob to find its randomized filename, and grep_search to find its randomized content. Then use write_file to create %q with exact content %q and edit to replace it with exact content %q. Return the required JSON fields with the observed values and status ok.",
		seedFiles[0], target, "draft-"+nonce, "final-"+nonce)
	raw := runRealCLITurn(t, runtime, thread, prompt)
	assertConstantObject(t, raw, map[string]string{
		"read_value": string(seed), "glob_name": seedName, "grep_value": string(seed), "status": "ok",
	})
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "final-"+nonce {
		t.Fatal("write_file/edit probe did not produce the required artifact")
	}
}

func testRealCLIWritePolicy(t *testing.T, fixture realCLIFixture) {
	nonce := realCLINonce(t)
	cases := []struct {
		name   string
		root   string
		target string
	}{
		{"workspace", fixture.artifactOne, filepath.Join(fixture.workspace, "denied-"+nonce+".txt")},
		{"sibling", fixture.artifactOne, filepath.Join(fixture.artifactTwo, "denied-"+nonce+".txt")},
		{"link escape", fixture.artifactOne, filepath.Join(fixture.linkEscape, "denied-"+nonce+".txt")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			schema := constantObjectSchema(map[string]string{"status": "denied"})
			runtime := startRealCLIRuntime(t, fixture, schema)
			thread := startRealCLIThread(t, runtime, fixture.workspace, test.root, schema,
				"Attempt the requested write exactly once. Never choose a persistent permission grant.")
			_, err := runRealCLITurnResult(runtime, thread, fmt.Sprintf("Attempt write_file once for %q with content %q. If it is refused, return status denied.", test.target, nonce), realCLITimeout)
			if err == nil || !errors.Is(err, agentruntime.ErrPermissionDenied) {
				t.Fatal("out-of-root write did not fail through permission mediation")
			}
			if _, err := os.Stat(test.target); !os.IsNotExist(err) {
				t.Fatal("denied write changed an out-of-root target")
			}
		})
	}
}

func testRealCLIForbiddenSurface(t *testing.T, fixture realCLIFixture) {
	nonce := realCLINonce(t)
	marker := filepath.Join(fixture.workspace, "shell-marker-"+nonce+".txt")
	schema := constantObjectSchema(map[string]string{"status": "unavailable"})
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"Do not claim a capability exists unless it is actually exposed in this session.")
	prompt := fmt.Sprintf("Inspect your available capabilities. Attempt no fallback. Confirm that shell (including creating %q), web/network, MCP, hooks, extensions, skills, memory, subagents, and background tasks are unavailable; then return status unavailable.", marker)
	raw := runRealCLITurn(t, runtime, thread, prompt)
	assertConstantObject(t, raw, map[string]string{"status": "unavailable"})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("forbidden shell surface created its marker")
	}
}

func observeRealCLINativeRead(t *testing.T, fixture realCLIFixture) string {
	canary, err := os.ReadFile(fixture.readCanary)
	if err != nil {
		t.Fatal("read native canary fixture")
	}
	schema := enumObjectSchema("outcome", []string{"read", "denied", "unknown"}, "value")
	runtime := startRealCLIRuntime(t, fixture, schema)
	thread := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"This is an advisory observation, not proof of OS isolation. Report only what actually happens.")
	raw, turnErr := runRealCLITurnResult(runtime, thread, fmt.Sprintf("Attempt a native read_file of %q. If content is visible return outcome read and the exact content; if refused return denied and an empty value; otherwise unknown and empty value.", fixture.readCanary), realCLITimeout)
	if turnErr != nil {
		if errors.Is(turnErr, agentruntime.ErrPermissionDenied) {
			return "denied"
		}
		return "unknown"
	}
	var response struct {
		Outcome string `json:"outcome"`
		Value   string `json:"value"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "unknown"
	}
	if response.Outcome == "read" && response.Value == string(canary) {
		return "read"
	}
	if response.Outcome == "denied" && response.Value == "" {
		return "denied"
	}
	return "unknown"
}

func testRealCLIFeatureFlow(t *testing.T, fixture realCLIFixture) {
	schema := specflow.DialogueSchema()
	runtime := startRealCLIRuntime(t, fixture, specflow.FlowEnvelopeSchema())
	author := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema,
		"Act as intent/spec/plan author. Use artifacts only inside the configured artifact root.")
	reviewer := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactTwo, schema,
		"Act as specification and plan reviewer. Record one material finding, then resolve it after rework.")
	transientSessionIDs := []string{
		realCLIBackend(t, author).connection.SessionID(),
		realCLIBackend(t, reviewer).connection.SessionID(),
	}

	assertEnvelopeKind(t, runRealCLITurn(t, runtime, author,
		"Begin /feature author dialogue. Return a message with one material agent decision and no file yet."), specflow.KindMessage, true)
	intent := filepath.Join(fixture.artifactOne, "intent.md")
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, author,
		fmt.Sprintf("Write %q as a concise intent document, then return an artifact envelope.", intent)), specflow.KindArtifact, false)
	assertNonEmptyFile(t, intent)
	spec := filepath.Join(fixture.artifactOne, "spec.md")
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, author,
		fmt.Sprintf("Write %q as a specification with requirements and acceptance criteria, then return an artifact envelope.", spec)), specflow.KindArtifact, false)
	before := assertNonEmptyFile(t, spec)
	publishRealCLIArtifact(t, fixture.workspace, spec)
	review := filepath.Join(fixture.artifactTwo, "spec-review.md")
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, reviewer,
		fmt.Sprintf("Read %q, write %q with one open material finding, then return an artifact envelope.", filepath.Join(fixture.workspace, "spec.md"), review)), specflow.KindArtifact, false)
	assertNonEmptyFile(t, review)
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, reviewer,
		"Record a reviewer message with one agent decision requiring the material finding to be fixed."), specflow.KindMessage, true)
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, author,
		fmt.Sprintf("Automatically rework %q to address the material review; change its content and return an artifact envelope.", spec)), specflow.KindArtifact, false)
	after := assertNonEmptyFile(t, spec)
	if string(after) == string(before) {
		t.Fatal("automatic rework did not change the specification artifact")
	}
	publishRealCLIArtifact(t, fixture.workspace, spec)
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, reviewer,
		fmt.Sprintf("Update %q so the finding is resolved and the review is approved; return an artifact envelope.", review)), specflow.KindArtifact, false)
	plan := filepath.Join(fixture.artifactOne, "plan.md")
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, author,
		fmt.Sprintf("Write %q as an implementation plan and return an artifact envelope.", plan)), specflow.KindArtifact, false)
	assertNonEmptyFile(t, plan)
	publishRealCLIArtifact(t, fixture.workspace, plan)
	planReview := filepath.Join(fixture.artifactTwo, "plan-review.md")
	assertEnvelopeKind(t, runRealCLITurn(t, runtime, reviewer,
		fmt.Sprintf("Review %q, write an approved review to %q, and return an artifact envelope.", filepath.Join(fixture.workspace, "plan.md"), planReview)), specflow.KindArtifact, false)
	assertNonEmptyFile(t, planReview)

	for _, source := range []string{intent, review, planReview} {
		publishRealCLIArtifact(t, fixture.workspace, source)
	}
	git(t, fixture.workspace, "add", ".")
	git(t, fixture.workspace, "commit", "--quiet", "-m", "publish acceptance flow")
	assertRealCLIWorkspaceOmits(t, fixture.workspace, transientSessionIDs)
	if err := runtime.Close(); err != nil {
		t.Fatal("close first real-CLI runtime generation")
	}

	resumeRoot := filepath.Join(fixture.root, "resume-artifact")
	if err := os.Mkdir(resumeRoot, 0o700); err != nil {
		t.Fatal("create fresh-resume artifact root")
	}
	resumed := startRealCLIRuntime(t, fixture, specflow.FlowEnvelopeSchema())
	resume := startRealCLIThread(t, resumed, fixture.workspace, resumeRoot, schema,
		"Resume only from durable workspace documents; no prior process or session identity is available.")
	assertEnvelopeKind(t, runRealCLITurn(t, resumed, resume,
		"Fresh-resume the completed /feature flow by reading intent.md, spec.md, their reviews, and plan.md from the workspace. Return a message confirming the durable plan state."), specflow.KindMessage, false)
}

func assertRealCLIWorkspaceOmits(t *testing.T, workspace string, forbidden []string) {
	t.Helper()
	err := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if path != workspace && filepath.Base(path) == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if value != "" && strings.Contains(string(data), value) {
				return errors.New("durable document contains transient provider identity")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal("durable flow persisted a transient provider identity")
	}
}

func publishRealCLIArtifact(t *testing.T, workspace, source string) {
	t.Helper()
	data := assertNonEmptyFile(t, source)
	if err := os.WriteFile(filepath.Join(workspace, filepath.Base(source)), data, 0o600); err != nil {
		t.Fatal("publish durable flow document")
	}
}

func testRealCLIPlatformLifecycle(t *testing.T, fixture realCLIFixture) {
	schema := constantObjectSchema(map[string]string{"status": "ok"})
	runtime := startRealCLIRuntime(t, fixture, schema)
	first := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactOne, schema, "Return status ok when asked.")
	second := startRealCLIThread(t, runtime, fixture.workspace, fixture.artifactTwo, schema, "Return status ok when asked.")
	firstBackend, secondBackend := realCLIBackend(t, first), realCLIBackend(t, second)
	if firstBackend.process == secondBackend.process || firstBackend.process.job == secondBackend.process.job {
		t.Fatal("two logical threads share a process or containment owner")
	}
	assertRealCLIStartup(t, firstBackend, fixture, fixture.artifactOne)
	assertRealCLIStartup(t, secondBackend, fixture, fixture.artifactTwo)
	if firstBackend.connection.SessionID() == secondBackend.connection.SessionID() {
		t.Fatal("two logical threads share an ACP session identity")
	}
	firstSet := realCLIPlatformProcessSet(t, firstBackend.process)
	secondSet := realCLIPlatformProcessSet(t, secondBackend.process)
	if processSetsOverlap(firstSet, secondSet) {
		t.Fatal("two logical threads share a native process tree")
	}
	if err := runtime.CloseThread(first); err != nil {
		t.Fatal("close first real-CLI thread")
	}
	waitRealCLIPlatformStopped(t, firstSet, 5*time.Second)
	assertRealCLIPlatformRunning(t, secondSet)

	before := realCLIDirectorySnapshot(t, fixture.artifactTwo)
	turnDone := make(chan error, 1)
	go func() {
		_, err := runtime.RunTurn(second, "Keep this turn active by repeatedly using glob until interrupted; do not write any file. If uninterrupted, return status ok.")
		turnDone <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.mu.Lock()
		active := runtime.active != nil
		runtime.mu.Unlock()
		if active {
			break
		}
		select {
		case <-turnDone:
			t.Fatal("cancel probe completed before the interrupt checkpoint")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel probe did not become active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	secondSet = mergeProcessSets(secondSet, realCLIPlatformProcessSet(t, secondBackend.process))
	interruptStarted := time.Now()
	if err := runtime.Interrupt(); err != nil {
		t.Fatal("interrupt real-CLI runtime")
	}
	if time.Since(interruptStarted) > interruptGracePeriod+5*time.Second {
		t.Fatal("global interrupt exceeded its bounded grace and cleanup window")
	}
	waitRealCLIPlatformStopped(t, secondSet, 5*time.Second)
	select {
	case err := <-turnDone:
		if err == nil || (!errors.Is(err, agentruntime.ErrTurnInterrupted) && !errors.Is(err, agentruntime.ErrRuntimeClosed)) {
			t.Fatal("active turn did not terminate as interrupted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupted turn did not terminate")
	}
	if _, err := runtime.RunTurn(second, "late"); !errors.Is(err, agentruntime.ErrTurnInterrupted) {
		t.Fatal("global interrupt did not invalidate the surviving handle")
	}
	time.Sleep(500 * time.Millisecond)
	after := realCLIDirectorySnapshot(t, fixture.artifactTwo)
	if !equalRealCLISnapshot(before, after) {
		t.Fatal("artifact root changed after the global close checkpoint")
	}
}

func mergeProcessSets(sets ...[]int) []int {
	seen := make(map[int]struct{})
	var result []int
	for _, set := range sets {
		for _, value := range set {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func processSetsOverlap(left, right []int) bool {
	seen := make(map[int]struct{}, len(left))
	for _, value := range left {
		seen[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[value]; ok {
			return true
		}
	}
	return false
}

type realCLIFileState struct {
	Size    int64
	ModTime int64
}

func realCLIDirectorySnapshot(t *testing.T, root string) map[string]realCLIFileState {
	t.Helper()
	result := make(map[string]realCLIFileState)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = realCLIFileState{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		t.Fatal("snapshot real-CLI artifact root")
	}
	return result
}

func equalRealCLISnapshot(left, right map[string]realCLIFileState) bool {
	if len(left) != len(right) {
		return false
	}
	for name, state := range left {
		if right[name] != state {
			return false
		}
	}
	return true
}

func startRealCLIRuntime(t *testing.T, fixture realCLIFixture, envelopeSchema json.RawMessage) *Runtime {
	t.Helper()
	runtime, err := StartRuntime(Config{
		Executable: fixture.executable, Workspace: fixture.workspace,
		JSONContract: JSONContract, EnvelopeSchema: envelopeSchema,
	})
	if err != nil {
		t.Fatal("configure real Qwen runtime")
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func startRealCLIThread(t *testing.T, runtime *Runtime, workspace, artifact string, schema json.RawMessage, role string) agentruntime.Thread {
	t.Helper()
	type startResult struct {
		thread agentruntime.Thread
		err    error
	}
	done := make(chan startResult, 1)
	go func() {
		thread, err := runtime.StartThread(agentruntime.ThreadConfig{
			BootstrapInstructions: role, OutputSchema: schema, Workspace: workspace, ArtifactRoot: artifact,
		})
		done <- startResult{thread: thread, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal("start real Qwen thread")
		}
		return result.thread
	case <-time.After(realCLITimeout):
		go runtime.Interrupt()
		t.Fatal("real Qwen thread startup timed out")
		return nil
	}
}

func runRealCLITurn(t *testing.T, runtime *Runtime, thread agentruntime.Thread, prompt string) json.RawMessage {
	t.Helper()
	raw, err := runRealCLITurnResult(runtime, thread, prompt, realCLITimeout)
	if err != nil {
		t.Fatal("real Qwen turn failed")
	}
	return raw
}

func runRealCLITurnResult(runtime *Runtime, thread agentruntime.Thread, prompt string, timeout time.Duration) (json.RawMessage, error) {
	type turnResult struct {
		raw json.RawMessage
		err error
	}
	done := make(chan turnResult, 1)
	go func() {
		raw, err := runtime.RunTurn(thread, prompt)
		done <- turnResult{raw: raw, err: err}
	}()
	select {
	case result := <-done:
		return result.raw, result.err
	case <-time.After(timeout):
		go runtime.Interrupt()
		return nil, agentruntime.ErrTurnInterrupted
	}
}

func realCLIBackend(t *testing.T, thread agentruntime.Thread) *qwenThread {
	t.Helper()
	handle, ok := thread.(*threadHandle)
	if !ok || handle == nil || handle.state == nil {
		t.Fatal("real Qwen thread handle has unexpected type")
	}
	backend, ok := handle.state.backend.(*qwenThread)
	if !ok || backend == nil || backend.process == nil || backend.connection == nil {
		t.Fatal("real Qwen thread backend is incomplete")
	}
	return backend
}

func assertRealCLIStartup(t *testing.T, backend *qwenThread, fixture realCLIFixture, root string) {
	t.Helper()
	process := backend.process
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.config.Executable != fixture.executable || filepath.Clean(process.command.Path) != fixture.executablePath {
		t.Fatal("launcher did not use the exact explicit PATH name")
	}
	if process.command.Dir != fixture.workspace || process.artifactRoot != root || process.job == nil || process.command.Process == nil {
		t.Fatal("launcher roots or containment are incomplete")
	}
	want := qwenArgs(JSONContract, root)
	if len(process.command.Args) != len(want)+1 {
		t.Fatal("launcher argument count differs from the fixed startup profile")
	}
	for index := range want {
		if process.command.Args[index+1] != want[index] {
			t.Fatal("launcher arguments differ from the fixed startup profile")
		}
	}
	if backend.connection.SessionID() == "" {
		t.Fatal("ACP session/new did not publish a session")
	}
}

func assertEnvelopeKind(t *testing.T, raw json.RawMessage, kind specflow.Kind, requireDecision bool) {
	t.Helper()
	envelope, err := specflow.DecodeEnvelope(raw)
	if err != nil || envelope.Kind != kind {
		t.Fatal("real Qwen response does not match the expected domain envelope")
	}
	if requireDecision && len(envelope.Decisions) == 0 {
		t.Fatal("material dialogue did not produce a durable decision")
	}
}

func assertNonEmptyFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		t.Fatal("expected artifact was not written")
	}
	return data
}

func constantObjectSchema(values map[string]string) json.RawMessage {
	properties := make(map[string]any, len(values))
	required := make([]string, 0, len(values))
	for name, value := range values {
		properties[name] = map[string]any{"type": "string", "const": value}
		required = append(required, name)
	}
	sort.Strings(required)
	data, _ := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	})
	return data
}

func enumObjectSchema(enumName string, values []string, valueName string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			enumName:  map[string]any{"type": "string", "enum": values},
			valueName: map[string]any{"type": "string"},
		},
		"required": []string{enumName, valueName}, "additionalProperties": false,
	})
	return data
}

func assertConstantObject(t *testing.T, raw json.RawMessage, want map[string]string) {
	t.Helper()
	var got map[string]string
	if json.Unmarshal(raw, &got) != nil || len(got) != len(want) {
		t.Fatal("real Qwen probe returned an unexpected structured result")
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatal("real Qwen probe returned an unexpected structured value")
		}
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Env = withoutGitContext(os.Environ())
	if err := command.Run(); err != nil {
		t.Fatal("prepare disposable Git fixture")
	}
}

func realCLINonce(t *testing.T) string {
	t.Helper()
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		t.Fatal("create randomized integration canary")
	}
	return hex.EncodeToString(data)
}

func writeRealCLIResult(t *testing.T, result *realCLIResult) {
	t.Helper()
	path := os.Getenv(realCLIResultEnv)
	if path == "" {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Errorf("encode sanitized real-CLI result")
		return
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		t.Errorf("write sanitized real-CLI result")
		return
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Errorf("publish sanitized real-CLI result")
	}
}
