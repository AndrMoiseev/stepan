package claudeapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/conformance"
	"github.com/AndrMoiseev/stepan/internal/specflow"
	claudecode "github.com/severity1/claude-agent-sdk-go"
)

func TestClaudeProviderParity(t *testing.T) {
	conformance.ProviderParity(t, func(t *testing.T, script conformance.Script) conformance.Fixture {
		t.Helper()
		config := testConfig(t)
		config.EnvelopeSchema = specflow.FlowEnvelopeSchema()
		outputs := script.Outputs()
		messages := make([]claudecode.Message, len(outputs))
		for index, output := range outputs {
			var structured map[string]any
			if err := json.Unmarshal(output, &structured); err != nil {
				t.Fatal(err)
			}
			messages[index] = &claudecode.ResultMessage{StructuredOutput: structured}
		}
		runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client {
			return &fakeClient{messages: messages}
		})
		if err != nil {
			t.Fatal(err)
		}
		return conformance.Fixture{
			Runtime: runtime, Workspace: config.Workspace, OutputSchema: specflow.DialogueSchema(),
			Decode: func(raw json.RawMessage) (conformance.DomainEnvelope, error) {
				envelope, err := specflow.DecodeEnvelope(raw)
				return conformance.DomainEnvelope{Kind: string(envelope.Kind), Message: envelope.Message, DecisionCount: len(envelope.Decisions)}, err
			},
			WriteAllowed: func(threadConfig agentruntime.ThreadConfig, target string) bool {
				policy := activePolicy{workspace: threadConfig.Workspace, artifactRoot: threadConfig.ArtifactRoot, writableRoot: threadConfig.ArtifactRoot}
				return permitTool("Write", map[string]any{"file_path": target}, policy, threadConfig.Workspace) == nil
			},
		}
	})
}

func TestClaudeApplicationParity(t *testing.T) {
	conformance.ApplicationParity(t, specflow.RuntimeIdentity{Provider: "claude", Model: "default"}, func(t *testing.T, workspace string, script conformance.Script) conformance.ApplicationFixture {
		t.Helper()
		executable := installClaudeExecutable(t, "claude-compatible")
		config := Config{Executable: executable, Workspace: workspace, EnvelopeSchema: specflow.FlowEnvelopeSchema()}
		outputs := script.Outputs()
		messages := make([]claudecode.Message, len(outputs))
		for index, output := range outputs {
			var structured map[string]any
			if err := json.Unmarshal(output, &structured); err != nil {
				t.Fatal(err)
			}
			messages[index] = &claudecode.ResultMessage{StructuredOutput: structured}
		}
		runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client {
			return &fakeClient{messages: messages}
		})
		if err != nil {
			t.Fatal(err)
		}
		return conformance.ApplicationFixture{Runtime: conformance.ScriptedArtifacts(runtime, script)}
	})
}

func TestClaudeExecutableResolutionUsesOnlyAuthoritativePATHNames(t *testing.T) {
	config := testConfig(t)
	validated, _, err := validateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(validated.Executable) || filepath.Base(validated.Executable) != config.Executable {
		t.Fatalf("resolved Claude executable = %q from %q", validated.Executable, config.Executable)
	}

	official := installClaudeExecutable(t, "claude")
	defaultConfig := config
	defaultConfig.Executable = ""
	validated, _, err = validateConfig(defaultConfig)
	if err != nil || filepath.Base(validated.Executable) != official {
		t.Fatalf("default Claude executable = %q, %v; want %q", validated.Executable, err, official)
	}
	for _, name := range []string{filepath.Join(t.TempDir(), "claude"), filepath.Join("directory", "claude"), `directory\claude`} {
		invalid := config
		invalid.Executable = name
		if _, _, err := validateConfig(invalid); err == nil || !strings.Contains(err.Error(), "simple PATH name") {
			t.Fatalf("invalid Claude executable %q error = %v", name, err)
		}
	}

	missing := config
	missing.Executable = "missing-claude-compatible-cli"
	created := false
	instance, err := startRuntime(context.Background(), missing, func(context.Context, ...claudecode.Option) client {
		created = true
		return &fakeClient{}
	})
	if instance != nil || created || !errors.Is(err, agentruntime.ErrRuntimeConfiguration) || !strings.Contains(err.Error(), "configure Claude runtime") {
		t.Fatalf("missing Claude executable = %#v, %v; client_created=%v", instance, err, created)
	}
}

func TestClaudeTransportSchemaOmitsDialectDeclarations(t *testing.T) {
	config := testConfig(t)
	config.EnvelopeSchema = specflow.FlowEnvelopeSchema()
	validated, schema, err := validateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	options := claudecode.NewOptions(claudeOptions(validated, schema, nil, func(string) {})...)
	if options.OutputFormat == nil {
		t.Fatal("Claude transport schema is missing")
	}

	encoded, err := json.Marshal(options.OutputFormat.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"$schema"`)) {
		t.Fatalf("Claude transport schema contains unsupported dialect declaration: %s", encoded)
	}
	if string(validated.EnvelopeSchema) != string(config.EnvelopeSchema) {
		t.Fatal("transport normalization changed the domain schema")
	}
}

func TestClaudeRuntimeRoutesTurnsToFreshSessionsAndClosesOnce(t *testing.T) {
	config := testConfig(t)
	validated, _, err := validateConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{messages: []claudecode.Message{
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "READY_TO_WRITE", "spec_id": "one"}},
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "WRITTEN"}},
	}}
	var options *claudecode.Options
	runtime, err := startRuntime(context.Background(), config, func(_ context.Context, items ...claudecode.Option) client {
		options = claudecode.NewOptions(items...)
		return fake
	})
	if err != nil {
		t.Fatal(err)
	}
	firstConfig := testThreadConfig(config)
	firstConfig.BootstrapInstructions = "effective intent-author prompt"
	first, err := runtime.StartThread(firstConfig)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.StartThread(testThreadConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(first, "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(first, "write"); err != nil {
		t.Fatal(err)
	}
	if len(fake.sessions) != 2 || fake.sessions[0] != fake.sessions[1] || fake.sessions[0] == "" {
		t.Fatalf("sessions = %#v", fake.sessions)
	}
	if len(fake.prompts) != 2 || !strings.Contains(fake.prompts[0], "effective intent-author prompt") || strings.Contains(fake.prompts[1], "effective intent-author prompt") {
		t.Fatalf("bootstrap prompt lifecycle = %#v", fake.prompts)
	}
	if first == second {
		t.Fatal("two ideas received the same thread handle")
	}
	assertLockedOptions(t, options, validated)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.disconnects != 1 {
		t.Fatalf("disconnects = %d", fake.disconnects)
	}
}

func TestClaudeRuntimeRejectsForeignThreadAndBadTerminalOutput(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{messages: []claudecode.Message{&claudecode.ResultMessage{StructuredOutput: nil}}}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.RunTurn(&thread{sessionID: "foreign"}, "turn"); err == nil {
		t.Fatal("foreign thread was accepted")
	}
	handle, err := runtime.StartThread(testThreadConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn"); !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("invalid result error = %v", err)
	}
}

func TestClaudeRuntimeAcceptsProviderAssignedTerminalSession(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{batches: [][]claudecode.Message{{
		&claudecode.ResultMessage{SessionID: "provider-generated-uuid", StructuredOutput: map[string]any{"status": "READY_TO_WRITE"}},
	}}}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handle, err := runtime.StartThread(testThreadConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn"); err != nil {
		t.Fatalf("provider-assigned result session was rejected: %v", err)
	}
	if sent := fake.session(0); sent == "" || sent == "provider-generated-uuid" {
		t.Fatalf("local query session = %q", sent)
	}
}

func TestClaudeRuntimeRejectsEmptyProviderSession(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{
		keepEmptySession: true,
		messages:         []claudecode.Message{&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "READY_TO_WRITE"}}},
	}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handle, err := runtime.StartThread(testThreadConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn"); !errors.Is(err, agentruntime.ErrRuntimeExited) || !strings.Contains(err.Error(), "has no provider session") {
		t.Fatalf("empty provider session error = %v", err)
	}
}

func TestClaudeRuntimeDoesNotReuseDelayedTerminalResult(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{messages: []claudecode.Message{
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "READY_TO_WRITE", "spec_id": "one"}},
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "WRITTEN"}},
	}}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handle, err := runtime.StartThread(testThreadConfig(config))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "first"); err != nil {
		t.Fatal(err)
	}
	fake.drainReceived()
	fake.send(&claudecode.ResultMessage{SessionID: fake.session(0), StructuredOutput: map[string]any{"status": "WRITTEN"}})
	fake.waitReceived()
	if _, err := runtime.RunTurn(handle, "second"); !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("second turn after duplicate = %v", err)
	}
	if got := fake.sessionCount(); got != 1 {
		t.Fatalf("queries after duplicate = %d", got)
	}
}

func TestClaudeRuntimeAcceptsExactExternalArtifactRoot(t *testing.T) {
	config := testConfig(t)
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return &fakeClient{} })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	artifact := t.TempDir()
	handle, err := runtime.StartThread(agentruntime.ThreadConfig{Workspace: config.Workspace, OutputSchema: config.EnvelopeSchema, ArtifactRoot: artifact})
	if err != nil {
		t.Fatal(err)
	}
	item := handle.(*thread)
	if item.config.ArtifactRoot == "" {
		t.Fatal("external artifact root was not retained by thread")
	}
}

func TestClaudeCloseThreadInvalidatesItsHandle(t *testing.T) {
	conformance.ClosedThread(t, func(t *testing.T) (agentruntime.Runtime, agentruntime.ThreadConfig) {
		t.Helper()
		config := testConfig(t)
		runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return &fakeClient{} })
		if err != nil {
			t.Fatal(err)
		}
		return runtime, testThreadConfig(config)
	})
}

func TestClaudeRuntimeInterruptThenClosesOnce(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if fake.interrupts != 1 || fake.disconnects != 1 {
		t.Fatalf("interrupts = %d, disconnects = %d", fake.interrupts, fake.disconnects)
	}
	if _, err := runtime.StartThread(testThreadConfig(config)); !errors.Is(err, agentruntime.ErrRuntimeClosed) {
		t.Fatalf("start after interrupt = %v", err)
	}
}

func TestStartRuntimeDoesNotCreateClientForCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	created := false
	if _, err := startRuntime(ctx, testConfig(t), func(context.Context, ...claudecode.Option) client {
		created = true
		return &fakeClient{}
	}); !errors.Is(err, agentruntime.ErrRuntimeClosed) {
		t.Fatalf("start error = %v", err)
	}
	if created {
		t.Fatal("client factory was called for canceled startup")
	}
}

func TestStartRuntimeCleansUpPartialConnect(t *testing.T) {
	connectErr := errors.New("failed to initialize control protocol: initialize failed: control request timeout: context deadline exceeded")
	cleanupErr := errors.New("cleanup failed")
	fake := &fakeClient{connectErr: connectErr, disconnectErr: cleanupErr}
	_, err := startRuntime(context.Background(), testConfig(t), func(_ context.Context, items ...claudecode.Option) client {
		options := claudecode.NewOptions(items...)
		fake.connectHook = func() {
			if options.StderrCallback != nil {
				options.StderrCallback("tclaude: unknown option --permission-prompt-tool")
			}
		}
		return fake
	})
	if !errors.Is(err, connectErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("start error does not retain both causes: %v", err)
	}
	for _, detail := range []string{
		"CLI did not answer the Claude Agent SDK initialize request",
		"tclaude: unknown option --permission-prompt-tool",
	} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("start error %q does not contain diagnostic %q", err, detail)
		}
	}
	if fake.disconnects != 1 {
		t.Fatalf("disconnects = %d", fake.disconnects)
	}
}

func TestPermissionEvaluatorFailsClosed(t *testing.T) {
	workspace, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(workspace, "inside.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRoot := filepath.Join(workspace, "docs")
	if err := os.Mkdir(writeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	readOnly := activePolicy{}
	write := activePolicy{writableRoot: writeRoot, artifactRoot: writeRoot}
	if err := permitTool("Read", map[string]any{"file_path": inside}, readOnly, workspace); err != nil {
		t.Fatalf("read inside: %v", err)
	}
	if err := permitTool("Read", map[string]any{"file_path": outside}, readOnly, workspace); err == nil {
		t.Fatal("read outside was allowed")
	}
	if err := permitTool("Write", map[string]any{"file_path": filepath.Join(writeRoot, "new.md")}, readOnly, workspace); err == nil {
		t.Fatal("read-only write was allowed")
	}
	if err := permitTool("Write", map[string]any{"file_path": filepath.Join(writeRoot, "new.md")}, write, workspace); err != nil {
		t.Fatalf("write inside root: %v", err)
	}
	if err := permitTool("Bash", map[string]any{"command": "whoami"}, write, workspace); err == nil {
		t.Fatal("Bash was allowed")
	}
}

func TestPermissionEvaluatorResolvesEveryPathFromWorkspace(t *testing.T) {
	workspace, err := canonicalDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeRoot := filepath.Join(workspace, "docs", "changes", "features", "example")
	if err := os.MkdirAll(writeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	active := activePolicy{writableRoot: writeRoot, artifactRoot: writeRoot}
	readOnly := activePolicy{}

	if err := permitTool("Write", map[string]any{"file_path": "specification.md"}, active, workspace); err == nil {
		t.Fatal("relative write was resolved from writable root")
	}
	for _, path := range []string{
		"docs/changes/features/example/specification.md",
		filepath.Join(writeRoot, "specification.md"),
	} {
		if err := permitTool("Write", map[string]any{"file_path": path}, active, workspace); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	for _, path := range []string{
		"../outside.md",
		filepath.Join(t.TempDir(), "outside.md"),
		filepath.Join(workspace, "other.md"),
	} {
		if err := permitTool("Edit", map[string]any{"file_path": path}, active, workspace); err == nil {
			t.Fatalf("write escape %q was allowed", path)
		}
	}
	for _, tool := range []string{"Read", "Glob", "Grep"} {
		field := "file_path"
		if tool != "Read" {
			field = "path"
		}
		if err := permitTool(tool, map[string]any{field: "docs/changes/features/example/specification.md"}, readOnly, workspace); err != nil {
			t.Fatalf("%s inside workspace: %v", tool, err)
		}
		if err := permitTool(tool, map[string]any{field: "../outside.md"}, readOnly, workspace); err == nil {
			t.Fatalf("%s outside workspace was allowed", tool)
		}
	}
	if err := permitTool("Edit", map[string]any{"file_path": "docs/changes/features/example/specification.md"}, readOnly, workspace); err == nil {
		t.Fatal("read-only edit was allowed")
	}
}

func TestPermissionEvaluatorRejectsMalformedToolPaths(t *testing.T) {
	workspace := t.TempDir()
	writeRoot := filepath.Join(workspace, "docs")
	if err := os.Mkdir(writeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	active := activePolicy{writableRoot: writeRoot, artifactRoot: writeRoot}
	for _, tool := range []string{"Read", "Write", "Edit", "Glob", "Grep"} {
		field := "file_path"
		other := "path"
		if tool == "Glob" || tool == "Grep" {
			field, other = other, field
		}
		for _, input := range []map[string]any{
			nil,
			{},
			{field: nil},
			{field: 1},
			{field: "docs/file.md", other: "docs/other.md"},
		} {
			if err := permitTool(tool, input, active, workspace); err == nil {
				t.Fatalf("%s accepted malformed input %#v", tool, input)
			}
		}
	}
	if err := permitTool("Bash", map[string]any{"file_path": "docs/file.md"}, active, workspace); err == nil {
		t.Fatal("unknown tool was allowed")
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: nowhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := installClaudeExecutable(t, "corporate-claude")
	return Config{Executable: executable, Workspace: root, EnvelopeSchema: []byte(`{"type":"object"}`)}
}

func installClaudeExecutable(t *testing.T, base string) string {
	t.Helper()
	name := base
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, name), []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return name
}

func testThreadConfig(config Config) agentruntime.ThreadConfig {
	return agentruntime.ThreadConfig{Workspace: config.Workspace, OutputSchema: config.EnvelopeSchema}
}

func assertLockedOptions(t *testing.T, options *claudecode.Options, config Config) {
	t.Helper()
	if options == nil || options.CLIPath == nil || *options.CLIPath != config.Executable || options.Cwd == nil || *options.Cwd != config.Workspace {
		t.Fatalf("CLI options = %#v", options)
	}
	if !reflect.DeepEqual(options.Tools, []string{"Read", "Write", "Edit", "Glob", "Grep"}) || options.PermissionMode == nil || *options.PermissionMode != claudecode.PermissionModeDefault {
		t.Fatalf("tools or permission mode = %#v", options)
	}
	if options.SettingSources == nil || len(options.SettingSources) != 0 || !reflect.DeepEqual(options.Skills, []string{}) || len(options.AddDirs) != 0 || len(options.McpServers) != 0 || len(options.Plugins) != 0 || len(options.Agents) != 0 {
		t.Fatalf("unsafe SDK options = %#v", options)
	}
	if options.ExtraEnv[backgroundTasksEnv] != "1" || options.ExtraEnv[agentViewEnv] != "1" || options.OutputFormat == nil || options.CanUseTool == nil || options.StderrCallback == nil {
		t.Fatalf("required SDK options missing = %#v", options)
	}
}

type fakeClient struct {
	mu               sync.Mutex
	messages         []claudecode.Message
	sessions         []string
	prompts          []string
	disconnects      int
	interrupts       int
	connectErr       error
	connectHook      func()
	disconnectErr    error
	responses        chan claudecode.Message
	received         chan struct{}
	batches          [][]claudecode.Message
	queries          int
	keepEmptySession bool
}

func (client *fakeClient) Connect(context.Context, ...claudecode.StreamMessage) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.responses == nil {
		client.responses = make(chan claudecode.Message, 32)
		client.received = make(chan struct{}, 32)
	}
	if client.connectHook != nil {
		client.connectHook()
	}
	return client.connectErr
}
func (client *fakeClient) Disconnect() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.disconnects++
	return client.disconnectErr
}
func (client *fakeClient) QueryWithSession(_ context.Context, prompt string, session string) error {
	client.mu.Lock()
	client.sessions = append(client.sessions, session)
	client.prompts = append(client.prompts, prompt)
	var messages []claudecode.Message
	if client.queries < len(client.batches) {
		messages = client.batches[client.queries]
	} else if len(client.messages) != 0 {
		messages = []claudecode.Message{client.messages[0]}
		client.messages = client.messages[1:]
	}
	client.queries++
	for index, message := range messages {
		if result, ok := message.(*claudecode.ResultMessage); ok && result.SessionID == "" && !client.keepEmptySession {
			copy := *result
			copy.SessionID = session
			messages[index] = &copy
		}
	}
	responses := client.responses
	client.mu.Unlock()
	for _, message := range messages {
		responses <- message
	}
	return nil
}

func (client *fakeClient) send(message claudecode.Message) { client.responses <- message }

func (client *fakeClient) drainReceived() {
	for {
		select {
		case <-client.received:
		default:
			return
		}
	}
}

func (client *fakeClient) waitReceived() { <-client.received }

func (client *fakeClient) session(index int) string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.sessions[index]
}

func (client *fakeClient) sessionCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.sessions)
}
func (client *fakeClient) ReceiveResponse(context.Context) claudecode.MessageIterator {
	client.mu.Lock()
	defer client.mu.Unlock()
	return &fakeIterator{responses: client.responses, received: client.received}
}
func (client *fakeClient) Interrupt(context.Context) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.interrupts++
	return nil
}

type fakeIterator struct {
	responses <-chan claudecode.Message
	received  chan<- struct{}
}

func (iterator *fakeIterator) Next(ctx context.Context) (claudecode.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case message := <-iterator.responses:
		iterator.received <- struct{}{}
		return message, nil
	}
}
func (*fakeIterator) Close() error { return nil }
