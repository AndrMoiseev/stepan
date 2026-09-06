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

type clientFactory func(context.Context, ...claudecode.Option) client

type thread struct {
	runtime      *Runtime
	sessionID    string
	config       agentruntime.ThreadConfig
	bootstrapped bool
	closed       bool
}

// Runtime has no process-tree containment in v1. It deliberately uses the
// SDK's standard subprocess transport; direct CLI close is manually tested.
type Runtime struct {
	client              client
	workspace           string
	transportProperties map[string]struct{}
	ctx                 context.Context
	cancel              context.CancelFunc
	responses           claudecode.MessageIterator
	receiverDone        chan struct{}
	receiverClose       sync.Once

	turnMu       sync.Mutex
	stateMu      sync.Mutex
	activePolicy *activePolicy
	activeQuery  *responseQuery
	unhealthy    bool
	closed       bool
	closeOnce    sync.Once
	closeErr     error
	nextSession  atomic.Uint64
}

type responseQuery struct {
	done     chan struct{}
	output   json.RawMessage
	err      error
	terminal bool
}

type activePolicy struct {
	writableRoot string
	workspace    string
	artifactRoot string
}

var _ agentruntime.Runtime = (*Runtime)(nil)

func StartRuntime(ctx context.Context, config Config) (*Runtime, error) {
	return startRuntime(ctx, config, func(_ context.Context, options ...claudecode.Option) client { return claudecode.NewClient(options...) })
}

func startRuntime(ctx context.Context, config Config, factory clientFactory) (*Runtime, error) {
	validated, schema, err := validateConfig(config)
	if err != nil {
		return nil, fmt.Errorf("configure Claude runtime: %w", errors.Join(agentruntime.ErrRuntimeConfiguration, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, agentruntime.ErrRuntimeClosed
	}
	lifecycle, cancel := context.WithCancel(ctx)
	runtime := &Runtime{
		workspace:           validated.Workspace,
		transportProperties: schemaPropertyNames(schema),
		ctx:                 lifecycle,
		cancel:              cancel,
	}
	stderr := &stderrCapture{}
	runtime.client = factory(lifecycle, claudeOptions(validated, schema, runtime.canUseTool, stderr.Add)...)
	if runtime.client == nil {
		cancel()
		return nil, errors.New("Claude client factory returned nil")
	}
	connectStarted := time.Now()
	if err := runtime.client.Connect(lifecycle); err != nil {
		cancel()
		cleanupErr := runtime.client.Disconnect()
		diagnostic := describeConnectFailure(err, time.Since(connectStarted), stderr.String())
		return nil, fmt.Errorf("connect Claude CLI %q: %s: %w", validated.Executable, diagnostic, errors.Join(err, cleanupErr))
	}
	if err := lifecycle.Err(); err != nil {
		_ = runtime.client.Disconnect()
		return nil, agentruntime.ErrRuntimeClosed
	}
	runtime.responses = runtime.client.ReceiveResponse(lifecycle)
	if runtime.responses == nil {
		cancel()
		_ = runtime.client.Disconnect()
		return nil, errors.New("Claude response iterator is nil")
	}
	runtime.receiverDone = make(chan struct{})
	go runtime.receiveResponses()
	return runtime, nil
}

func (runtime *Runtime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.stateMu.Lock()
	defer runtime.stateMu.Unlock()
	if runtime.closed {
		return nil, agentruntime.ErrRuntimeClosed
	}
	config = config.Clone()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	workspace, err := canonicalDirectory(config.Workspace)
	if err != nil {
		return nil, fmt.Errorf("thread workspace: %w", err)
	}
	if workspace != runtime.workspace {
		return nil, errors.New("thread workspace does not match runtime workspace")
	}
	config.Workspace = workspace
	if config.ArtifactRoot != "" {
		artifact, err := canonicalDirectory(config.ArtifactRoot)
		if err != nil {
			return nil, fmt.Errorf("artifact root: %w", err)
		}
		if pathWithin(runtime.workspace, artifact) {
			return nil, errors.New("artifact root must be outside workspace")
		}
		config.ArtifactRoot = artifact
	}
	id := runtime.nextSession.Add(1)
	return &thread{runtime: runtime, sessionID: fmt.Sprintf("stepan-%d", id), config: config}, nil
}

func (runtime *Runtime) RunTurn(handle agentruntime.Thread, prompt string) (json.RawMessage, error) {
	item, ok := handle.(*thread)
	if !ok || item == nil || item.runtime != runtime || item.sessionID == "" || item.closed {
		return nil, errors.New("invalid Claude thread handle")
	}
	if prompt == "" {
		return nil, errors.New("turn prompt is required")
	}
	if _, err := decodeJSONObject(item.config.OutputSchema); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	policy := activePolicy{workspace: runtime.workspace, artifactRoot: item.config.ArtifactRoot, writableRoot: item.config.ArtifactRoot}
	if !runtime.turnMu.TryLock() {
		return nil, agentruntime.ErrTurnInProgress
	}
	defer runtime.turnMu.Unlock()
	runtime.stateMu.Lock()
	if runtime.closed {
		runtime.stateMu.Unlock()
		return nil, agentruntime.ErrRuntimeClosed
	}
	if runtime.unhealthy {
		runtime.stateMu.Unlock()
		return nil, agentruntime.ErrRuntimeExited
	}
	runtime.activePolicy = &policy
	query := &responseQuery{done: make(chan struct{})}
	runtime.activeQuery = query
	runtime.stateMu.Unlock()
	defer func() {
		runtime.stateMu.Lock()
		if runtime.activeQuery == query {
			runtime.activeQuery = nil
		}
		runtime.activePolicy = nil
		runtime.stateMu.Unlock()
	}()

	if !item.bootstrapped {
		prompt = item.config.BootstrapInstructions + "\n\n" + prompt
		item.bootstrapped = true
	}
	ctx := runtime.ctx
	if err := runtime.client.QueryWithSession(ctx, prompt, item.sessionID); err != nil {
		return nil, runtime.runtimeError("send turn", err)
	}
	select {
	case <-query.done:
	case <-ctx.Done():
		return nil, runtime.runtimeError("receive turn", ctx.Err())
	}
	runtime.stateMu.Lock()
	output, err := append(json.RawMessage(nil), query.output...), query.err
	if runtime.unhealthy && err == nil {
		err = agentruntime.ErrRuntimeExited
	}
	runtime.stateMu.Unlock()
	if err != nil {
		return nil, runtime.runtimeError("receive turn", err)
	}
	output, err = projectTransportOutput(output, runtime.transportProperties, item.config.OutputSchema)
	if err != nil {
		return nil, fmt.Errorf("Claude structured output: %w", err)
	}
	if err := agentruntime.ValidateOutput(item.config.OutputSchema, output); err != nil {
		return nil, fmt.Errorf("Claude structured output: %w", err)
	}
	return output, nil
}

// projectTransportOutput removes only properties that belong to another
// branch of the connection-level transport envelope. Unknown properties stay
// in the object so the narrow thread schema can still reject protocol drift.
func projectTransportOutput(output json.RawMessage, transportProperties map[string]struct{}, targetSchema json.RawMessage) (json.RawMessage, error) {
	var target map[string]json.RawMessage
	if err := json.Unmarshal(targetSchema, &target); err != nil || target == nil {
		return nil, errors.New("thread output schema must be a JSON object")
	}
	var targetProperties map[string]json.RawMessage
	if raw, present := target["properties"]; present {
		if err := json.Unmarshal(raw, &targetProperties); err != nil || targetProperties == nil {
			return nil, errors.New("thread output schema properties must be a JSON object")
		}
	}
	if targetProperties == nil {
		return output, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(output, &object); err != nil || object == nil {
		return nil, errors.New("structured output must be a JSON object")
	}
	changed := false
	for name := range object {
		if _, allowed := targetProperties[name]; allowed {
			continue
		}
		if _, knownTransportProperty := transportProperties[name]; knownTransportProperty {
			delete(object, name)
			changed = true
		}
	}
	if !changed {
		return output, nil
	}
	projected, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("marshal projected structured output: %w", err)
	}
	return projected, nil
}

func schemaPropertyNames(schema map[string]any) map[string]struct{} {
	properties, _ := schema["properties"].(map[string]any)
	names := make(map[string]struct{}, len(properties))
	for name := range properties {
		names[name] = struct{}{}
	}
	return names
}

// CloseThread invalidates the logical session handle. The Claude SDK has no
// per-session close operation, so the provider process remains available for
// other threads while this handle becomes unusable.
func (runtime *Runtime) CloseThread(handle agentruntime.Thread) error {
	item, ok := handle.(*thread)
	if !ok || item == nil || item.runtime != runtime || item.sessionID == "" {
		return errors.New("invalid Claude thread handle")
	}
	runtime.stateMu.Lock()
	defer runtime.stateMu.Unlock()
	if runtime.closed {
		return agentruntime.ErrRuntimeClosed
	}
	item.closed = true
	return nil
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
		runtime.cancel()
		runtime.closeResponseIterator()
		if runtime.receiverDone != nil {
			<-runtime.receiverDone
		}
		runtime.closeErr = runtime.client.Disconnect()
	})
	return runtime.closeErr
}

func (runtime *Runtime) closeResponseIterator() {
	runtime.receiverClose.Do(func() { _ = runtime.responses.Close() })
}

func (runtime *Runtime) receiveResponses() {
	defer close(runtime.receiverDone)
	defer runtime.closeResponseIterator()
	for {
		message, err := runtime.responses.Next(runtime.ctx)
		if err != nil {
			runtime.failResponse(err)
			return
		}
		if message == nil {
			runtime.failResponse(errors.New("Claude response stream returned nil message"))
			return
		}
		result, ok := message.(*claudecode.ResultMessage)
		if !ok {
			continue
		}
		runtime.receiveResult(result)
	}
}

func (runtime *Runtime) receiveResult(result *claudecode.ResultMessage) {
	runtime.stateMu.Lock()
	defer runtime.stateMu.Unlock()
	query := runtime.activeQuery
	if query == nil || query.terminal || result.SessionID == "" {
		runtime.unhealthy = true
		if query != nil && !query.terminal {
			query.err = errors.New("Claude terminal result has no provider session")
			close(query.done)
		}
		return
	}
	if result.IsError {
		query.err = describeTerminalResultError(result)
	} else if result.StructuredOutput == nil {
		query.err = errors.New("Claude terminal result has no structured output")
	} else {
		data, err := json.Marshal(result.StructuredOutput)
		if err != nil {
			query.err = fmt.Errorf("marshal Claude structured output: %w", err)
		} else {
			query.output, query.err = decodeJSONObject(data)
		}
	}
	query.terminal = true
	close(query.done)
}

func (runtime *Runtime) failResponse(err error) {
	runtime.stateMu.Lock()
	defer runtime.stateMu.Unlock()
	if runtime.closed || errors.Is(err, context.Canceled) {
		if query := runtime.activeQuery; query != nil && !query.terminal {
			query.err = agentruntime.ErrRuntimeClosed
			close(query.done)
		}
		return
	}
	runtime.unhealthy = true
	if query := runtime.activeQuery; query != nil && !query.terminal {
		query.err = err
		close(query.done)
	}
}

func (runtime *Runtime) runtimeError(action string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", action, agentruntime.ErrTurnInterrupted)
	}
	return fmt.Errorf("Claude CLI %s: %w: %v", action, agentruntime.ErrRuntimeExited, err)
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

func permitTool(name string, input map[string]any, policy activePolicy, workspace string) error {
	path, err := toolPath(name, input)
	if err != nil {
		return err
	}
	workspace, err = canonicalDirectory(workspace)
	if err != nil {
		return err
	}
	if policy.artifactRoot != "" {
		policy.artifactRoot, err = canonicalDirectory(policy.artifactRoot)
		if err != nil {
			return err
		}
	}
	if policy.writableRoot != "" {
		policy.writableRoot, err = canonicalDirectory(policy.writableRoot)
		if err != nil {
			return err
		}
	}
	candidate, err := resolveAllowedPath(workspace, policy.artifactRoot, path)
	if err != nil {
		return err
	}
	if name == "Write" || name == "Edit" {
		if policy.writableRoot == "" {
			return errors.New("write denied in read-only turn")
		}
		return pathWithinRoot(policy.writableRoot, candidate)
	}
	if name != "Read" && name != "Glob" && name != "Grep" {
		return fmt.Errorf("tool %q is not allowed", name)
	}
	if !pathWithin(workspace, candidate) && (policy.artifactRoot == "" || !pathWithin(policy.artifactRoot, candidate)) {
		return errors.New("read outside allowed roots")
	}
	return nil
}

func toolPath(name string, input map[string]any) (string, error) {
	if input == nil {
		return "", errors.New("tool input is required")
	}
	field := "file_path"
	if name == "Glob" || name == "Grep" {
		field = "path"
	}
	otherField := "path"
	if field == "path" {
		otherField = "file_path"
	}
	if _, ambiguous := input[otherField]; ambiguous {
		return "", fmt.Errorf("tool %s has ambiguous path fields", name)
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
			return nil, describeTerminalResultError(result)
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
