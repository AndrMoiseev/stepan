package claudeapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	claudecode "github.com/severity1/claude-agent-sdk-go"
)

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
	first, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(first, "initial", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(first, "write", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); err != nil {
		t.Fatal(err)
	}
	if len(fake.sessions) != 2 || fake.sessions[0] != fake.sessions[1] || fake.sessions[0] == "" {
		t.Fatalf("sessions = %#v", fake.sessions)
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
	if _, err := runtime.RunTurn(&thread{sessionID: "foreign"}, "turn", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); err == nil {
		t.Fatal("foreign thread was accepted")
	}
	handle, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("invalid result error = %v", err)
	}
}

func TestClaudeRuntimeRejectsTerminalResultForAnotherSession(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{batches: [][]claudecode.Message{{
		&claudecode.ResultMessage{SessionID: "foreign", StructuredOutput: map[string]any{"status": "READY_TO_WRITE"}},
	}}}
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return fake })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handle, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("foreign result error = %v", err)
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
	handle, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "first", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); err != nil {
		t.Fatal(err)
	}
	fake.drainReceived()
	fake.send(&claudecode.ResultMessage{SessionID: fake.session(0), StructuredOutput: map[string]any{"status": "WRITTEN"}})
	fake.waitReceived()
	if _, err := runtime.RunTurn(handle, "second", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema}); !errors.Is(err, agentruntime.ErrRuntimeExited) {
		t.Fatalf("second turn after duplicate = %v", err)
	}
	if got := fake.sessionCount(); got != 1 {
		t.Fatalf("queries after duplicate = %d", got)
	}
}

func TestClaudeRuntimeRejectsWriteRootOutsideWorkspace(t *testing.T) {
	config := testConfig(t)
	runtime, err := startRuntime(context.Background(), config, func(context.Context, ...claudecode.Option) client { return &fakeClient{} })
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	handle, err := runtime.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.SingleWriteRootTurnPolicy(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RunTurn(handle, "turn", agentruntime.TurnOptions{OutputSchema: config.EnvelopeSchema, Policy: policy}); err == nil {
		t.Fatal("outside writable root was accepted")
	}
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
	if _, err := runtime.StartThread(); !errors.Is(err, agentruntime.ErrRuntimeClosed) {
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
	connectErr := errors.New("connect failed")
	cleanupErr := errors.New("cleanup failed")
	fake := &fakeClient{connectErr: connectErr, disconnectErr: cleanupErr}
	_, err := startRuntime(context.Background(), testConfig(t), func(context.Context, ...claudecode.Option) client { return fake })
	if !errors.Is(err, connectErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("start error does not retain both causes: %v", err)
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
	writePolicy, err := agentruntime.SingleWriteRootTurnPolicy(writeRoot)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := activePolicy{policy: agentruntime.ReadOnlyTurnPolicy()}
	write := activePolicy{policy: writePolicy, writableRoot: writeRoot}
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
	writeRoot := filepath.Join(workspace, "docs", "specs", "example")
	if err := os.MkdirAll(writeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.SingleWriteRootTurnPolicy(writeRoot)
	if err != nil {
		t.Fatal(err)
	}
	active := activePolicy{policy: policy, writableRoot: writeRoot}
	readOnly := activePolicy{policy: agentruntime.ReadOnlyTurnPolicy()}

	if err := permitTool("Write", map[string]any{"file_path": "specification.md"}, active, workspace); err == nil {
		t.Fatal("relative write was resolved from writable root")
	}
	for _, path := range []string{
		"docs/specs/example/specification.md",
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
		if err := permitTool(tool, map[string]any{field: "docs/specs/example/specification.md"}, readOnly, workspace); err != nil {
			t.Fatalf("%s inside workspace: %v", tool, err)
		}
		if err := permitTool(tool, map[string]any{field: "../outside.md"}, readOnly, workspace); err == nil {
			t.Fatalf("%s outside workspace was allowed", tool)
		}
	}
	if err := permitTool("Edit", map[string]any{"file_path": "docs/specs/example/specification.md"}, readOnly, workspace); err == nil {
		t.Fatal("read-only edit was allowed")
	}
}

func TestPermissionEvaluatorRejectsMalformedToolPaths(t *testing.T) {
	workspace := t.TempDir()
	writeRoot := filepath.Join(workspace, "docs")
	if err := os.Mkdir(writeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	policy, err := agentruntime.SingleWriteRootTurnPolicy(writeRoot)
	if err != nil {
		t.Fatal(err)
	}
	active := activePolicy{policy: policy, writableRoot: writeRoot}
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
	executable := filepath.Join(root, "corporate claude")
	if err := os.WriteFile(executable, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{Executable: executable, Workspace: root, EnvelopeSchema: []byte(`{"type":"object"}`)}
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
	if options.ExtraEnv[backgroundTasksEnv] != "1" || options.ExtraEnv[agentViewEnv] != "1" || options.OutputFormat == nil || options.CanUseTool == nil {
		t.Fatalf("required SDK options missing = %#v", options)
	}
}

type fakeClient struct {
	mu            sync.Mutex
	messages      []claudecode.Message
	sessions      []string
	disconnects   int
	interrupts    int
	connectErr    error
	disconnectErr error
	responses     chan claudecode.Message
	received      chan struct{}
	batches       [][]claudecode.Message
	queries       int
}

func (client *fakeClient) Connect(context.Context, ...claudecode.StreamMessage) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.responses == nil {
		client.responses = make(chan claudecode.Message, 32)
		client.received = make(chan struct{}, 32)
	}
	return client.connectErr
}
func (client *fakeClient) Disconnect() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.disconnects++
	return client.disconnectErr
}
func (client *fakeClient) QueryWithSession(_ context.Context, _ string, session string) error {
	client.mu.Lock()
	client.sessions = append(client.sessions, session)
	var messages []claudecode.Message
	if client.queries < len(client.batches) {
		messages = client.batches[client.queries]
	} else if len(client.messages) != 0 {
		messages = []claudecode.Message{client.messages[0]}
		client.messages = client.messages[1:]
	}
	client.queries++
	for index, message := range messages {
		if result, ok := message.(*claudecode.ResultMessage); ok && result.SessionID == "" {
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
