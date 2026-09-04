package qwenapp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/platformsupport"
	"github.com/AndrMoiseev/stepan/internal/processjob"
)

const allowedToolsCSV = "read_file,write_file,edit,glob,grep_search"

// excludedToolsCSV is deliberately explicit as defense in depth: current Qwen
// releases ignore --core-tools in safe mode, while --exclude-tools remains in
// force. The launch contract therefore carries both the allowlist and the
// exhaustive known denylist; protocol preflight can verify the effective set.
const excludedToolsCSV = "zoom_image,run_shell_command,todo_write,save_memory,agent,skill," +
	"exit_plan_mode,enter_plan_mode,web_fetch,web_search,image_gen,list_directory,lsp,ask_user_question," +
	"cron_create,cron_list,cron_delete,loop_wakeup,create_sub_session,list_agents,task_stop,task_create," +
	"task_update,task_list,team_create,team_delete,team_plan_approval,request_shutdown,send_message," +
	"structured_output,monitor,notebook_edit,tool_search,read_mcp_resource,enter_worktree,exit_worktree," +
	"workflow,artifact,record_artifact,report_findings,get_goal,update_goal,propose_goal,display_image"

var diagnosticSecretPattern = regexp.MustCompile(`(?i)(bearer\s+|(?:token|secret|password|credential|api[_-]?key)\s*[=:]\s*)[^\s,;]+`)

// Process owns one contained Qwen ACP child and the one transport artifact
// root mounted for it. The root is runtime-owned when the configured artifact
// root is empty.
type Process struct {
	config        Config
	artifactRoot  string
	workspaceRoot string
	deps          processDependencies

	mu               sync.Mutex
	started          bool
	closed           bool
	startErr         error
	command          *exec.Cmd
	stdin            io.WriteCloser
	stdout           io.ReadCloser
	stderr           io.ReadCloser
	job              processJob
	stderrDone       chan error
	runtimeOwnedRoot string
	diagnostic       limitedDiagnostic

	waitOnce  sync.Once
	waitDone  chan struct{}
	waitErr   error
	exitCode  *int
	closeOnce sync.Once
	closeErr  error
}

// NewProcess constructs a process launcher. artifactRoot may be empty for a
// read-only thread; Start then creates a separate empty runtime-owned root.
func NewProcess(config Config, artifactRoot string) *Process {
	return newProcess(config, artifactRoot, defaultDependencies())
}

func newProcess(config Config, artifactRoot string, deps processDependencies) *Process {
	config.JSONContract = strings.Clone(config.JSONContract)
	return &Process{
		config:       config,
		artifactRoot: artifactRoot,
		deps:         deps,
		waitDone:     make(chan struct{}),
	}
}

func defaultDependencies() processDependencies {
	return processDependencies{
		command: exec.Command,
		newJob: func() (processJob, error) {
			return processjob.New()
		},
		makeRoot: func() (string, error) {
			return os.MkdirTemp("", "stepan-qwen-")
		},
		remove:  os.RemoveAll,
		environ: os.Environ,
	}
}

// Start validates all immutable input, starts the selected executable directly,
// and assigns it to its process-tree supervisor before returning stdio.
func (process *Process) Start() error {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.closed {
		return errors.New("Qwen process is closed")
	}
	if process.started || process.startErr != nil {
		if process.startErr != nil {
			return process.startErr
		}
		return errors.New("Qwen process is already started")
	}

	executable, workspace, artifact, owned, err := process.preflight()
	if err != nil {
		process.startErr = fmt.Errorf("%w: %v", ErrConfiguration, err)
		return process.startErr
	}
	if owned {
		process.runtimeOwnedRoot = artifact
	}
	process.artifactRoot = artifact
	process.workspaceRoot = workspace
	baseEnvironment := process.deps.environ()
	sensitiveValues := environmentValuesForRedaction(baseEnvironment)
	sensitiveValues = append(sensitiveValues, process.config.JSONContract)
	process.diagnostic.setSecrets(sensitiveValues)

	command := process.deps.command(executable, qwenArgs(process.config.JSONContract, artifact)...)
	if command == nil {
		process.startErr = fmt.Errorf("%w: command factory returned nil", ErrStartup)
		process.closePartial()
		return process.startErr
	}
	command.Dir = workspace
	command.Env = isolatedEnv(baseEnvironment)
	stdin, err := command.StdinPipe()
	if err != nil {
		process.startErr = fmt.Errorf("%w: create stdin: %v", ErrStartup, err)
		process.closePartial()
		return process.startErr
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		process.startErr = fmt.Errorf("%w: create stdout: %v", ErrStartup, err)
		process.closePartial()
		return process.startErr
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		process.startErr = fmt.Errorf("%w: create stderr: %v", ErrStartup, err)
		process.closePartial()
		return process.startErr
	}
	job, err := process.deps.newJob()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		process.startErr = fmt.Errorf("%w: create supervisor: %v", ErrContainment, err)
		process.closePartial()
		return process.startErr
	}

	process.command, process.stdin, process.stdout, process.stderr, process.job = command, stdin, stdout, stderr, job
	if err := job.Prepare(command); err != nil {
		process.startErr = fmt.Errorf("%w: prepare supervisor: %v", ErrContainment, err)
		process.closePartial()
		return process.startErr
	}
	if err := command.Start(); err != nil {
		process.startErr = fmt.Errorf("%w: execute %q: %v", ErrStartup, executable, err)
		process.closePartial()
		return process.startErr
	}
	process.stderrDone = make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&process.diagnostic, stderr)
		process.stderrDone <- copyErr
	}()
	if err := job.Assign(command.Process); err != nil {
		process.closePartial()
		process.startErr = fmt.Errorf("%w: assign child: %v%s", ErrContainment, err, process.diagnosticSuffix())
		return process.startErr
	}
	process.started = true
	return nil
}

func (process *Process) preflight() (executable, workspace, artifact string, owned bool, err error) {
	if err = platformsupport.Validate(runtime.GOOS, runtime.GOARCH); err != nil {
		return "", "", "", false, err
	}
	if strings.TrimSpace(process.config.JSONContract) == "" {
		return "", "", "", false, errors.New("Qwen JSON contract is required")
	}
	if executable, err = ResolveExecutable(process.config.Executable); err != nil {
		return "", "", "", false, err
	}
	if workspace, err = canonicalGitRoot(process.config.Workspace); err != nil {
		return "", "", "", false, err
	}
	if process.artifactRoot != "" {
		artifact, err = canonicalDirectory(process.artifactRoot, "Qwen artifact root")
		if err != nil {
			return "", "", "", false, err
		}
		if err = validateDistinctRoots(workspace, artifact); err != nil {
			return "", "", "", false, err
		}
		return executable, workspace, artifact, false, nil
	}
	artifact, err = process.deps.makeRoot()
	if err != nil {
		return "", "", "", false, fmt.Errorf("create read-only transport root: %w", err)
	}
	owned = true
	cleanup := func(cause error) (string, string, string, bool, error) {
		return "", "", "", false, errors.Join(cause, process.deps.remove(artifact))
	}
	artifact, err = canonicalDirectory(artifact, "Qwen read-only transport root")
	if err != nil {
		return cleanup(err)
	}
	entries, err := os.ReadDir(artifact)
	if err != nil {
		return cleanup(fmt.Errorf("inspect read-only transport root: %w", err))
	}
	if len(entries) != 0 {
		return cleanup(errors.New("Qwen read-only transport root must be empty"))
	}
	if err = validateDistinctRoots(workspace, artifact); err != nil {
		return cleanup(err)
	}
	return executable, workspace, artifact, true, nil
}

func qwenArgs(contract, artifactRoot string) []string {
	return []string{
		"--safe-mode",
		"--approval-mode", "default",
		"--core-tools", allowedToolsCSV,
		"--exclude-tools", excludedToolsCSV,
		"--extensions", "none",
		"--append-system-prompt", contract,
		"--include-directories", artifactRoot,
		"--acp",
	}
}

func isolatedEnv(base []string) []string {
	overrides := map[string]string{
		"QWEN_CODE_SAFE_MODE":               "true",
		"QWEN_CODE_DISABLE_CRON":            "1",
		"QWEN_CODE_ENABLE_AGENT_TEAM":       "0",
		"QWEN_CODE_DISABLE_ARTIFACT":        "1",
		"QWEN_CODE_EMIT_TOOL_USE_SUMMARIES": "0",
	}
	values := make(map[string]string, len(base)+len(overrides))
	keys := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		folded := environmentKey(name)
		keys[folded] = name
		values[folded] = value
	}
	for name, value := range overrides {
		folded := environmentKey(name)
		keys[folded] = name
		values[folded] = value
	}
	ordered := make([]string, 0, len(values))
	for folded := range values {
		ordered = append(ordered, folded)
	}
	sort.Strings(ordered)
	environment := make([]string, 0, len(ordered))
	for _, folded := range ordered {
		environment = append(environment, keys[folded]+"="+values[folded])
	}
	return environment
}

func environmentKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// Stdin returns the ACP request stream after successful containment.
func (process *Process) Stdin() io.WriteCloser {
	process.mu.Lock()
	defer process.mu.Unlock()
	if !process.started {
		return nil
	}
	return process.stdin
}

// Stdout returns the ACP response stream after successful containment.
func (process *Process) Stdout() io.ReadCloser {
	process.mu.Lock()
	defer process.mu.Unlock()
	if !process.started {
		return nil
	}
	return process.stdout
}

// TransportRoot returns the canonical process-wide include root after Start.
// It does not imply that a read-only thread has a writable artifact root.
func (process *Process) TransportRoot() string {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.artifactRoot
}

// WorkspaceRoot returns the canonical Git root used as the child working
// directory. ACP session setup must use this exact value as its cwd.
func (process *Process) WorkspaceRoot() string {
	process.mu.Lock()
	defer process.mu.Unlock()
	return process.workspaceRoot
}

// Diagnostic returns bounded, redacted stderr collected from the child.
func (process *Process) Diagnostic() string { return process.diagnostic.String() }

func (process *Process) ExitCode() *int {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.exitCode == nil {
		return nil
	}
	code := *process.exitCode
	return &code
}

// Wait waits once for natural child termination and is safe to call repeatedly.
func (process *Process) Wait() error {
	process.mu.Lock()
	started, command := process.started, process.command
	process.mu.Unlock()
	if !started {
		return errors.New("Qwen process is not started")
	}
	process.waitOnce.Do(func() {
		commandErr := command.Wait()
		containmentErr := closeUnlessClosed(process.job)
		var stderrErr error
		if process.stderrDone != nil {
			stderrErr = <-process.stderrDone
		}
		if command.ProcessState != nil {
			code := command.ProcessState.ExitCode()
			process.mu.Lock()
			process.exitCode = &code
			process.mu.Unlock()
		}
		if commandErr != nil {
			process.waitErr = fmt.Errorf("Qwen process exited: %w%s", commandErr, process.diagnosticSuffix())
		} else if stderrErr != nil {
			process.waitErr = fmt.Errorf("read Qwen diagnostic: %w", stderrErr)
		}
		process.waitErr = errors.Join(process.waitErr, containmentErr)
		close(process.waitDone)
	})
	<-process.waitDone
	return process.waitErr
}

// Close idempotently kills the whole contained tree, closes stdio, waits for
// the child, and removes a runtime-owned read-only transport root.
func (process *Process) Close() error {
	process.closeOnce.Do(func() {
		process.mu.Lock()
		started := process.started
		process.closed = true
		process.mu.Unlock()
		if started {
			process.closeErr = errors.Join(closeUnlessClosed(process.job), closeUnlessClosed(process.stdin), closeUnlessClosed(process.stdout))
			_ = process.Wait()
			process.closeErr = errors.Join(process.closeErr, closeUnlessClosed(process.stderr))
		}
		process.closeErr = errors.Join(process.closeErr, process.removeRuntimeRoot())
	})
	return process.closeErr
}

func (process *Process) closePartial() {
	_ = closeUnlessClosed(process.job)
	_ = closeUnlessClosed(process.stdin)
	_ = closeUnlessClosed(process.stdout)
	if process.command != nil && process.command.Process != nil {
		_ = process.command.Process.Kill()
		_ = process.command.Wait()
	}
	if process.stderrDone != nil {
		<-process.stderrDone
	}
	_ = closeUnlessClosed(process.stderr)
	_ = process.removeRuntimeRoot()
	process.stdin, process.stdout, process.stderr = nil, nil, nil
	process.command, process.job = nil, nil
}

func (process *Process) removeRuntimeRoot() error {
	if process.runtimeOwnedRoot == "" {
		return nil
	}
	root := process.runtimeOwnedRoot
	process.runtimeOwnedRoot = ""
	return process.deps.remove(root)
}

func (process *Process) diagnosticSuffix() string {
	if diagnostic := strings.TrimSpace(process.Diagnostic()); diagnostic != "" {
		return ": stderr: " + diagnostic
	}
	return ""
}

type closer interface{ Close() error }

func closeUnlessClosed(value closer) error {
	if value == nil {
		return nil
	}
	err := value.Close()
	if errors.Is(err, os.ErrClosed) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

type limitedDiagnostic struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
	redactor  *strings.Replacer
	capture   int
}

func (diagnostic *limitedDiagnostic) setSecrets(secrets []string) {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	secrets = append([]string(nil), secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	pairs := make([]string, 0, len(secrets)*2)
	seen := make(map[string]struct{}, len(secrets))
	diagnostic.capture = maxDiagnosticBytes
	longest := 0
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if len(secret) > longest {
			longest = len(secret)
		}
		if _, duplicate := seen[secret]; duplicate {
			continue
		}
		seen[secret] = struct{}{}
		pairs = append(pairs, secret, "[REDACTED]")
	}
	if len(pairs) > 0 {
		diagnostic.redactor = strings.NewReplacer(pairs...)
	}
	diagnostic.capture += longest
}

func (diagnostic *limitedDiagnostic) Write(data []byte) (int, error) {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	limit := diagnostic.capture
	if limit == 0 {
		limit = maxDiagnosticBytes
	}
	remaining := limit - diagnostic.buffer.Len()
	if remaining < len(data) {
		diagnostic.truncated = true
	}
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = diagnostic.buffer.Write(data[:remaining])
	}
	return len(data), nil
}

func (diagnostic *limitedDiagnostic) String() string {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	value := diagnostic.buffer.String()
	if diagnostic.redactor != nil {
		value = diagnostic.redactor.Replace(value)
	}
	value = diagnosticSecretPattern.ReplaceAllStringFunc(value, func(match string) string {
		prefix := match
		if index := strings.IndexAny(match, "=:"); index >= 0 {
			prefix = match[:index+1]
		} else if index := strings.IndexAny(match, " \t"); index >= 0 {
			prefix = match[:index+1]
		}
		return prefix + "[REDACTED]"
	})
	if diagnostic.truncated || len(value) > maxDiagnosticBytes {
		const marker = "\n[stderr truncated]"
		limit := maxDiagnosticBytes - len(marker)
		if len(value) > limit {
			value = value[:limit]
			for !utf8.ValidString(value) && len(value) > 0 {
				value = value[:len(value)-1]
			}
		}
		value += marker
	}
	return value
}

// environmentValuesForRedaction deliberately does not classify values by
// variable name. Authentication providers and CI systems use open-ended names
// (for example GITHUB_PAT), so a name allowlist would fail open as providers
// evolve. Replacing every non-empty value preserves the classified cause while
// preventing inherited environment material from appearing in stderr.
func environmentValuesForRedaction(environment []string) []string {
	var values []string
	for _, entry := range environment {
		_, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		values = append(values, value)
	}
	return values
}
