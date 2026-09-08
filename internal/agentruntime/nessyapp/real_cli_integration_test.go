//go:build nessy_real_cli && (windows || darwin)

package nessyapp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/usersettings"
)

const (
	realCLIMarkerEnv = "STEPAN_NESSY_REAL_CLI"
	realCLIResultEnv = "STEPAN_NESSY_RESULT"
	realCLITimeout   = 90 * time.Second
)

type realCLIAssertion struct {
	Status string `json:"status"`
}

type realCLIReadObservation struct {
	Outcome               string `json:"outcome"`
	OSIsolationGuaranteed bool   `json:"os_isolation_guaranteed"`
}

type realCLIInventoryObservation struct {
	RequestedExact    []string `json:"requested_exact"`
	BehaviorObserved  []string `json:"behavior_observed"`
	PreflightPresent  bool     `json:"preflight_present"`
	PreflightReported []string `json:"preflight_reported,omitempty"`
}

type realCLIForbiddenObservation struct {
	Outcomes      map[string]string `json:"outcomes"`
	ACPToolEvents int               `json:"acp_tool_events"`
	CanariesFound int               `json:"canaries_found"`
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
	ToolInventory  realCLIInventoryObservation `json:"tool_inventory"`
	Forbidden      realCLIForbiddenObservation `json:"forbidden_capabilities"`
	DurationMS     int64                       `json:"duration_ms"`
	FailureClass   string                      `json:"failure_class,omitempty"`
}

type realCLIFixture struct {
	authToken      string
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

func TestNessyRealCLIConformance(t *testing.T) {
	started := time.Now()
	result := &realCLIResult{
		SchemaVersion: 1,
		Provider:      "nessy",
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

	name := defaultExecutable
	if os.Getenv(realCLIMarkerEnv) != "1" {
		result.FailureClass = "not_selected"
		t.Skip("real Nessy CLI integration was not selected by its opt-in runner")
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
	token, err := usersettings.NessyAuthToken()
	if err != nil {
		result.FailureClass = "configuration"
		t.Fatal("configure nessy.auth_token in ~/.stepan/settings.json")
	}
	t.Setenv("NESSY_CLI_DP_AUTH_TOKEN", "acceptance-conflicting-environment-value")
	fixture := makeRealCLIFixture(t, name, resolved)
	fixture.authToken = token

	runRealCLIAssertion(t, result, "startup_acp_and_five_tool_surface", func(t *testing.T) {
		result.ToolInventory = testRealCLIStartupAndTools(t, fixture)
	})
	runRealCLIAssertion(t, result, "write_policy_and_live_allow_once_correlation", func(t *testing.T) {
		testRealCLIWritePolicy(t, fixture)
	})
	runRealCLIAssertion(t, result, "permission_replay_and_ambiguity_policy", func(t *testing.T) {
		testPermissionAdversarialMatrix(t, fixture)
	})
	runRealCLIAssertion(t, result, "forbidden_capability_surface", func(t *testing.T) {
		result.Forbidden = testRealCLIForbiddenSurface(t, fixture)
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
	runRealCLIAssertion(t, result, "localized_live_protocol_failure", func(t *testing.T) {
		testRealCLIProtocolFailureLocalization(t, fixture)
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
	git(t, fixture.workspace, "config", "user.name", "Stepan Nessy Acceptance")
	git(t, fixture.workspace, "config", "user.email", "nessy-acceptance@example.invalid")
	git(t, fixture.workspace, "config", "commit.gpgsign", "false")
	git(t, fixture.workspace, "add", seedName)
	git(t, fixture.workspace, "commit", "--quiet", "-m", "acceptance baseline")
	return fixture
}

func startRealCLIRuntime(t *testing.T, fixture realCLIFixture, envelopeSchema json.RawMessage) *Runtime {
	t.Helper()
	runtime, err := StartRuntime(Config{
		AuthToken: fixture.authToken, Workspace: fixture.workspace,
		JSONContract: JSONContract, EnvelopeSchema: envelopeSchema,
	})
	if err != nil {
		t.Fatal("configure real Nessy runtime")
	}
	t.Cleanup(func() {
		if err := closeRealCLIRuntimeBounded(runtime); err != nil {
			t.Error("bounded real Nessy runtime cleanup failed")
		}
	})
	return runtime
}

func startRealCLIThread(t *testing.T, runtime *Runtime, workspace, artifact string, schema json.RawMessage, role string) agentruntime.Thread {
	t.Helper()
	thread, err := startRealCLIThreadResult(runtime, agentruntime.ThreadConfig{
		BootstrapInstructions: role, OutputSchema: schema, Workspace: workspace, ArtifactRoot: artifact,
	}, realCLITimeout)
	if err != nil {
		t.Fatal("start real Nessy thread")
	}
	return thread
}

func startRealCLIThreadResult(runtime *Runtime, config agentruntime.ThreadConfig, timeout time.Duration) (agentruntime.Thread, error) {
	type startResult struct {
		thread agentruntime.Thread
		err    error
	}
	done := make(chan startResult, 1)
	go func() {
		thread, err := runtime.StartThread(config)
		done <- startResult{thread: thread, err: err}
	}()
	select {
	case result := <-done:
		return result.thread, result.err
	case <-time.After(timeout):
		return nil, errors.Join(agentruntime.ErrRuntimeStartup, stopRealCLIRuntimeBounded(runtime))
	}
}

func runRealCLITurn(t *testing.T, runtime *Runtime, thread agentruntime.Thread, prompt string) json.RawMessage {
	t.Helper()
	raw, err := runRealCLITurnResult(runtime, thread, prompt, realCLITimeout)
	if err != nil {
		t.Fatal("real Nessy turn failed")
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
		return nil, errors.Join(agentruntime.ErrTurnInterrupted, stopRealCLIRuntimeBounded(runtime))
	}
}

func stopRealCLIRuntimeBounded(runtime *Runtime) error {
	if runtime == nil {
		return nil
	}
	return realCLIRuntimeActionBounded(runtime.Interrupt)
}

func closeRealCLIRuntimeBounded(runtime *Runtime) error {
	if runtime == nil {
		return nil
	}
	return realCLIRuntimeActionBounded(runtime.Close)
}

func realCLIRuntimeActionBounded(action func() error) error {
	if action == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- action() }()
	select {
	case err := <-done:
		return err
	case <-time.After(interruptGracePeriod + 5*time.Second):
		return errors.New("bounded runtime cleanup timed out")
	}
}

func realCLIBackend(t *testing.T, thread agentruntime.Thread) *nessyThread {
	t.Helper()
	handle, ok := thread.(*threadHandle)
	if !ok || handle == nil || handle.state == nil {
		t.Fatal("real Nessy thread handle has unexpected type")
	}
	backend, ok := handle.state.backend.(*nessyThread)
	if !ok || backend == nil || backend.process == nil || backend.connection == nil {
		t.Fatal("real Nessy thread backend is incomplete")
	}
	return backend
}

func assertRealCLIStartup(t *testing.T, backend *nessyThread, fixture realCLIFixture, root string) {
	t.Helper()
	process := backend.process
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.config.AuthToken != fixture.authToken || filepath.Clean(process.command.Path) != fixture.executablePath {
		t.Fatal("launcher did not use the exact explicit PATH name")
	}
	count := 0
	for _, entry := range process.command.Env {
		key, value, _ := strings.Cut(entry, "=")
		if environmentKey(key) == environmentKey("NESSY_CLI_DP_AUTH_TOKEN") {
			count++
			if value != fixture.authToken {
				t.Fatal("child auth differs from settings")
			}
		}
	}
	if count != 1 {
		t.Fatal("child auth is missing or duplicated")
	}
	if process.command.Dir != fixture.workspace || process.artifactRoot != root || process.job == nil || process.command.Process == nil {
		t.Fatal("launcher roots or containment are incomplete")
	}
	want := nessyArgs(JSONContract, root)
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
		t.Fatal("real Nessy probe returned an unexpected structured result")
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatal("real Nessy probe returned an unexpected structured value")
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

func gitHead(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	command.Env = withoutGitContext(os.Environ())
	output, err := command.Output()
	if err != nil {
		t.Fatal("read disposable Git HEAD")
	}
	return strings.TrimSpace(string(output))
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
