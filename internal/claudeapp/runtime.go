package claudeapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	claudecode "github.com/severity1/claude-agent-sdk-go"
)

type client interface {
	Connect(context.Context, ...claudecode.StreamMessage) error
	Disconnect() error
	QueryWithSession(context.Context, string, string) error
	ReceiveResponse(context.Context) claudecode.MessageIterator
	Interrupt(context.Context) error
}

type clientFactory func(...claudecode.Option) client

type thread struct {
	runtime   *Runtime
	sessionID string
}

// Runtime has no process-tree containment in v1. It deliberately uses the
// SDK's standard subprocess transport; direct CLI close is manually tested.
type Runtime struct {
	client    client
	workspace string

	turnMu       sync.Mutex
	stateMu      sync.Mutex
	activePolicy *agentruntime.TurnPolicy
	closed       bool
	closeOnce    sync.Once
	closeErr     error
	nextSession  atomic.Uint64
}

var _ agentruntime.Runtime = (*Runtime)(nil)

func StartRuntime(config Config) (*Runtime, error) {
	return startRuntime(config, func(options ...claudecode.Option) client { return claudecode.NewClient(options...) })
}

func startRuntime(config Config, factory clientFactory) (*Runtime, error) {
	validated, schema, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{workspace: validated.Workspace}
	runtime.client = factory(claudeOptions(validated, schema, runtime.canUseTool)...)
	if runtime.client == nil {
		return nil, errors.New("Claude client factory returned nil")
	}
	if err := runtime.client.Connect(context.Background()); err != nil {
		return nil, fmt.Errorf("connect Claude CLI %q: %w", validated.Executable, err)
	}
	return runtime, nil
}

func (runtime *Runtime) StartThread() (agentruntime.Thread, error) {
	runtime.stateMu.Lock()
	defer runtime.stateMu.Unlock()
	if runtime.closed {
		return nil, agentruntime.ErrRuntimeClosed
	}
	id := runtime.nextSession.Add(1)
	return &thread{runtime: runtime, sessionID: fmt.Sprintf("stepan-%d", id)}, nil
}

func (runtime *Runtime) RunTurn(handle agentruntime.Thread, prompt string, options agentruntime.TurnOptions) (json.RawMessage, error) {
	options = options.Clone()
	item, ok := handle.(*thread)
	if !ok || item == nil || item.runtime != runtime || item.sessionID == "" {
		return nil, errors.New("invalid Claude thread handle")
	}
	if prompt == "" {
		return nil, errors.New("turn prompt is required")
	}
	if _, err := decodeJSONObject(options.OutputSchema); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	if err := runtime.validatePolicy(options.Policy); err != nil {
		return nil, fmt.Errorf("turn policy: %w", err)
	}
	if !runtime.turnMu.TryLock() {
		return nil, agentruntime.ErrTurnInProgress
	}
	defer runtime.turnMu.Unlock()
	runtime.stateMu.Lock()
	if runtime.closed {
		runtime.stateMu.Unlock()
		return nil, agentruntime.ErrRuntimeClosed
	}
	policy := options.Policy
	runtime.activePolicy = &policy
	runtime.stateMu.Unlock()
	defer func() { runtime.stateMu.Lock(); runtime.activePolicy = nil; runtime.stateMu.Unlock() }()

	ctx := context.Background()
	if err := runtime.client.QueryWithSession(ctx, prompt, item.sessionID); err != nil {
		return nil, runtime.runtimeError("send turn", err)
	}
	output, err := collectStructuredOutput(ctx, runtime.client.ReceiveResponse(ctx))
	if err != nil {
		return nil, runtime.runtimeError("receive turn", err)
	}
	return output, nil
}

func (runtime *Runtime) Interrupt() error {
	runtime.stateMu.Lock()
	closed := runtime.closed
	runtime.stateMu.Unlock()
	if closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := runtime.client.Interrupt(ctx)
	cancel()
	closeErr := runtime.Close()
	if err != nil {
		return fmt.Errorf("interrupt Claude turn: %w", err)
	}
	return closeErr
}

func (runtime *Runtime) Close() error {
	runtime.closeOnce.Do(func() {
		runtime.stateMu.Lock()
		runtime.closed = true
		runtime.stateMu.Unlock()
		runtime.closeErr = runtime.client.Disconnect()
	})
	return runtime.closeErr
}

func (runtime *Runtime) runtimeError(action string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", action, agentruntime.ErrTurnInterrupted)
	}
	return fmt.Errorf("Claude CLI %s: %w: %v", action, agentruntime.ErrRuntimeExited, err)
}

func (runtime *Runtime) validatePolicy(policy agentruntime.TurnPolicy) error {
	root, writable := policy.WritableRoot()
	if !writable {
		return nil
	}
	_, err := resolvePath(runtime.workspace, root)
	return err
}

func (runtime *Runtime) canUseTool(_ context.Context, name string, input map[string]any, _ claudecode.ToolPermissionContext) (claudecode.PermissionResult, error) {
	runtime.stateMu.Lock()
	policy := runtime.activePolicy
	runtime.stateMu.Unlock()
	if policy == nil {
		return claudecode.NewPermissionResultDeny("no active Stepan turn"), nil
	}
	if err := permitTool(name, input, *policy, runtime.workspace); err != nil {
		return claudecode.NewPermissionResultDeny("Stepan policy denied tool request"), nil
	}
	return claudecode.NewPermissionResultAllow(), nil
}

func permitTool(name string, input map[string]any, policy agentruntime.TurnPolicy, workspace string) error {
	path, err := toolPath(name, input)
	if err != nil {
		return err
	}
	root, writable := policy.WritableRoot()
	if name == "Write" || name == "Edit" {
		if !writable {
			return errors.New("write denied in read-only turn")
		}
		_, err = resolvePath(root, path)
		return err
	}
	if name != "Read" && name != "Glob" && name != "Grep" {
		return fmt.Errorf("tool %q is not allowed", name)
	}
	_, err = resolvePath(workspace, path)
	return err
}

func toolPath(name string, input map[string]any) (string, error) {
	if input == nil {
		return "", errors.New("tool input is required")
	}
	field := "file_path"
	if name == "Glob" || name == "Grep" {
		field = "path"
		if input[field] == nil {
			return ".", nil
		}
	}
	value, ok := input[field].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("tool %s requires string %s", name, field)
	}
	return value, nil
}

func collectStructuredOutput(ctx context.Context, iterator claudecode.MessageIterator) (json.RawMessage, error) {
	if iterator == nil {
		return nil, errors.New("Claude response iterator is nil")
	}
	defer iterator.Close()
	for {
		message, err := iterator.Next(ctx)
		if err != nil {
			return nil, err
		}
		result, ok := message.(*claudecode.ResultMessage)
		if !ok {
			continue
		}
		if result.IsError {
			return nil, errors.New("Claude terminal result reports an error")
		}
		if result.StructuredOutput == nil {
			return nil, errors.New("Claude terminal result has no structured output")
		}
		data, err := json.Marshal(result.StructuredOutput)
		if err != nil {
			return nil, fmt.Errorf("marshal Claude structured output: %w", err)
		}
		return decodeJSONObject(data)
	}
}

func decodeJSONObject(data []byte) (json.RawMessage, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil, errors.New("structured output must be a JSON object")
	}
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("structured output must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("structured output has trailing JSON")
	}
	return append(json.RawMessage(nil), data...), nil
}
