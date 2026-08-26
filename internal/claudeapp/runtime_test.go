package claudeapp

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	claudecode "github.com/severity1/claude-agent-sdk-go"
)

func TestClaudeRuntimeRoutesTurnsToFreshSessionsAndClosesOnce(t *testing.T) {
	config := testConfig(t)
	fake := &fakeClient{messages: []claudecode.Message{
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "READY_TO_WRITE", "spec_id": "one"}},
		&claudecode.ResultMessage{StructuredOutput: map[string]any{"status": "WRITTEN"}},
	}}
	var options *claudecode.Options
	runtime, err := startRuntime(config, func(items ...claudecode.Option) client {
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
	assertLockedOptions(t, options, config)
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
	runtime, err := startRuntime(config, func(...claudecode.Option) client { return fake })
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

func TestClaudeRuntimeRejectsWriteRootOutsideWorkspace(t *testing.T) {
	config := testConfig(t)
	runtime, err := startRuntime(config, func(...claudecode.Option) client { return &fakeClient{} })
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
	runtime, err := startRuntime(config, func(...claudecode.Option) client { return fake })
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

func TestPermissionEvaluatorFailsClosed(t *testing.T) {
	workspace := t.TempDir()
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
	if err := permitTool("Read", map[string]any{"file_path": inside}, agentruntime.ReadOnlyTurnPolicy(), workspace); err != nil {
		t.Fatalf("read inside: %v", err)
	}
	if err := permitTool("Read", map[string]any{"file_path": outside}, agentruntime.ReadOnlyTurnPolicy(), workspace); err == nil {
		t.Fatal("read outside was allowed")
	}
	if err := permitTool("Write", map[string]any{"file_path": filepath.Join(writeRoot, "new.md")}, agentruntime.ReadOnlyTurnPolicy(), workspace); err == nil {
		t.Fatal("read-only write was allowed")
	}
	if err := permitTool("Write", map[string]any{"file_path": filepath.Join(writeRoot, "new.md")}, writePolicy, workspace); err != nil {
		t.Fatalf("write inside root: %v", err)
	}
	if err := permitTool("Bash", map[string]any{"command": "whoami"}, writePolicy, workspace); err == nil {
		t.Fatal("Bash was allowed")
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
	messages    []claudecode.Message
	sessions    []string
	disconnects int
	interrupts  int
}

func (*fakeClient) Connect(context.Context, ...claudecode.StreamMessage) error { return nil }
func (client *fakeClient) Disconnect() error                                   { client.disconnects++; return nil }
func (client *fakeClient) QueryWithSession(_ context.Context, _ string, session string) error {
	client.sessions = append(client.sessions, session)
	return nil
}
func (client *fakeClient) ReceiveResponse(context.Context) claudecode.MessageIterator {
	message := client.messages[0]
	client.messages = client.messages[1:]
	return &fakeIterator{messages: []claudecode.Message{message}}
}
func (client *fakeClient) Interrupt(context.Context) error { client.interrupts++; return nil }

type fakeIterator struct{ messages []claudecode.Message }

func (iterator *fakeIterator) Next(context.Context) (claudecode.Message, error) {
	if len(iterator.messages) == 0 {
		return nil, io.EOF
	}
	message := iterator.messages[0]
	iterator.messages = iterator.messages[1:]
	return message, nil
}
func (*fakeIterator) Close() error { return nil }
