package qwenapp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testJSONContract = "Return exactly one JSON object and no other text."

type fakeMetadata struct {
	Args        []string `json:"args"`
	Environment []string `json:"environment"`
	WorkingDir  string   `json:"workingDir"`
	PID         int      `json:"pid"`
}

func TestMain(main *testing.M) {
	if scenario := os.Getenv("GO_WANT_QWENAPP_FAKE"); scenario != "" {
		os.Exit(runQwenFake(scenario))
	}
	os.Exit(main.Run())
}

func runQwenFake(scenario string) int {
	switch scenario {
	case "contract":
		workingDir, err := os.Getwd()
		if err != nil {
			return 11
		}
		metadata := fakeMetadata{Args: os.Args[1:], Environment: os.Environ(), WorkingDir: workingDir, PID: os.Getpid()}
		data, err := json.Marshal(metadata)
		if err != nil || os.WriteFile(os.Getenv("STEPAN_QWENAPP_METADATA"), data, 0o600) != nil {
			return 12
		}
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return 13
		}
		_, _ = fmt.Fprint(os.Stdout, line)
		return 0
	case "wait":
		fmt.Fprintln(os.Stderr, "natural exit", diagnosticSecretValues())
		return 17
	case "assignment-failure":
		fmt.Fprintln(os.Stderr, diagnosticSecretValues())
		fmt.Fprintln(os.Stderr, testJSONContract)
		fmt.Fprintln(os.Stderr, strings.Repeat("x", maxDiagnosticBytes+4096))
		if err := os.WriteFile(os.Getenv("STEPAN_QWENAPP_READY"), []byte("ready"), 0o600); err != nil {
			return 14
		}
		time.Sleep(30 * time.Second)
		return 0
	case "tree":
		return runQwenTreeFake()
	case "acp":
		return runQwenACPFake()
	default:
		return 10
	}
}

func diagnosticSecretValues() []string {
	return []string{
		os.Getenv("QWEN_API_KEY"),
		os.Getenv("GITHUB_PAT"),
		os.Getenv("CI_JOB_JWT"),
		os.Getenv("SIGNING_MATERIAL"),
	}
}

func TestLauncherPassesExactIsolatedContract(t *testing.T) {
	workspace := makeGitRoot(t)
	artifact := t.TempDir()
	metadataPath := filepath.Join(t.TempDir(), "metadata.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "contract")
	t.Setenv("STEPAN_QWENAPP_METADATA", metadataPath)
	t.Setenv("QWEN_API_KEY", "test-auth-value")
	t.Setenv("QWEN_CODE_ENABLE_AGENT_TEAM", "1")

	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: workspace, JSONContract: testJSONContract}, artifact)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	if process.Stdin() == nil || process.Stdout() == nil {
		t.Fatal("contained stdio was not published")
	}
	if _, err := process.Stdin().Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(process.Stdout()).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("stdout = %q, %v", line, err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}

	metadata := readFakeMetadata(t, metadataPath)
	wantArgs := []string{
		"--safe-mode",
		"--approval-mode", "default",
		"--core-tools", "read_file,write_file,edit,glob,grep_search",
		"--exclude-tools", "zoom_image,run_shell_command,todo_write,save_memory,agent,skill," +
			"exit_plan_mode,enter_plan_mode,web_fetch,web_search,image_gen,list_directory,lsp,ask_user_question," +
			"cron_create,cron_list,cron_delete,loop_wakeup,create_sub_session,list_agents,task_stop,task_create," +
			"task_update,task_list,team_create,team_delete,team_plan_approval,request_shutdown,send_message," +
			"structured_output,monitor,notebook_edit,tool_search,read_mcp_resource,enter_worktree,exit_worktree," +
			"workflow,artifact,record_artifact,report_findings,get_goal,update_goal,propose_goal,display_image",
		"--extensions", "none",
		"--append-system-prompt", testJSONContract,
		"--include-directories", canonicalForTest(t, artifact),
		"--acp",
	}
	if !slices.Equal(metadata.Args, wantArgs) {
		t.Fatalf("argv:\n got %#v\nwant %#v", metadata.Args, wantArgs)
	}
	if strings.Contains(strings.Join(metadata.Args, "\x00"), "--version") {
		t.Fatal("launcher performed a version probe")
	}
	if metadata.WorkingDir != canonicalForTest(t, workspace) {
		t.Fatalf("cwd = %q, want %q", metadata.WorkingDir, canonicalForTest(t, workspace))
	}
	if metadata.PID == os.Getpid() {
		t.Fatal("fake was not launched as a direct child process")
	}
	environment := envMap(metadata.Environment)
	if environment["QWEN_API_KEY"] != "test-auth-value" {
		t.Fatal("authentication environment was not preserved")
	}
	for name, value := range map[string]string{
		"QWEN_CODE_SAFE_MODE":               "true",
		"QWEN_CODE_DISABLE_CRON":            "1",
		"QWEN_CODE_ENABLE_AGENT_TEAM":       "0",
		"QWEN_CODE_DISABLE_ARTIFACT":        "1",
		"QWEN_CODE_EMIT_TOOL_USE_SUMMARIES": "0",
	} {
		if environment[name] != value {
			t.Errorf("%s = %q, want %q", name, environment[name], value)
		}
	}
	if process.TransportRoot() != canonicalForTest(t, artifact) {
		t.Fatalf("transport root = %q", process.TransportRoot())
	}
	if process.WorkspaceRoot() != canonicalForTest(t, workspace) {
		t.Fatalf("workspace root = %q", process.WorkspaceRoot())
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("caller-owned artifact root was removed: %v", err)
	}
}

func TestReadOnlyProcessGetsEmptyOwnedRootAndRemovesIt(t *testing.T) {
	metadataPath := filepath.Join(t.TempDir(), "metadata.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "contract")
	t.Setenv("STEPAN_QWENAPP_METADATA", metadataPath)
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, "")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	root := process.TransportRoot()
	if root == "" {
		t.Fatal("read-only process has no transport root")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("runtime root is not empty: %v, %v", entries, err)
	}
	if _, err := process.Stdin().Write([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(process.Stdout()).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime root remains after close: %v", err)
	}
	if err := process.Close(); err != nil {
		t.Fatal("second close:", err)
	}
}

func TestSeparateProcessesReceiveNoSiblingArtifactRoot(t *testing.T) {
	workspace := makeGitRoot(t)
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	firstMetadata := filepath.Join(t.TempDir(), "first.json")
	secondMetadata := filepath.Join(t.TempDir(), "second.json")
	t.Setenv("GO_WANT_QWENAPP_FAKE", "contract")
	t.Setenv("STEPAN_QWENAPP_METADATA", firstMetadata)
	first := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: workspace, JSONContract: testJSONContract}, firstRoot)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPAN_QWENAPP_METADATA", secondMetadata)
	second := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: workspace, JSONContract: testJSONContract}, secondRoot)
	if err := second.Start(); err != nil {
		_ = first.Close()
		t.Fatal(err)
	}

	firstArgs := readFakeMetadata(t, firstMetadata).Args
	secondArgs := readFakeMetadata(t, secondMetadata).Args
	assertOnlyIncludeRoot(t, firstArgs, canonicalForTest(t, firstRoot), canonicalForTest(t, secondRoot))
	assertOnlyIncludeRoot(t, secondArgs, canonicalForTest(t, secondRoot), canonicalForTest(t, firstRoot))
	for _, process := range []*Process{first, second} {
		if _, err := process.Stdin().Write([]byte("done\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(process.Stdout()).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		if err := process.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutableResolutionAllowsPATHAndAuthoritativeNonstandardBasename(t *testing.T) {
	executable := absoluteTestExecutable(t)
	if got, err := ResolveExecutable(executable); err != nil || got != filepath.Clean(executable) {
		t.Fatalf("explicit resolution = %q, %v", got, err)
	}

	pathDir := t.TempDir()
	pathName := "qwen"
	if runtime.GOOS == "windows" {
		pathName += ".exe"
	}
	copyExecutable(t, executable, filepath.Join(pathDir, pathName))
	t.Setenv("PATH", pathDir)
	resolved, err := ResolveExecutable("qwen")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(resolved) != pathDir {
		t.Fatalf("PATH executable = %q", resolved)
	}
	copyExecutable(t, executable, filepath.Join(pathDir, "other-cli.exe"))
	if _, err := ResolveExecutable("other-cli"); err == nil || !strings.Contains(err.Error(), `PATH name "qwen"`) {
		t.Fatalf("other PATH name error = %v", err)
	}
}

func TestInvalidRootsAndConfigAreRejectedBeforeLaunch(t *testing.T) {
	executable := absoluteTestExecutable(t)
	workspace := makeGitRoot(t)
	parentArtifact := filepath.Dir(workspace)
	fakeGitRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(fakeGitRoot, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		config   Config
		artifact string
	}{
		{name: "relative workspace", config: Config{Executable: executable, Workspace: ".", JSONContract: testJSONContract}, artifact: t.TempDir()},
		{name: "not git root", config: Config{Executable: executable, Workspace: t.TempDir(), JSONContract: testJSONContract}, artifact: t.TempDir()},
		{name: "fake git metadata", config: Config{Executable: executable, Workspace: fakeGitRoot, JSONContract: testJSONContract}, artifact: t.TempDir()},
		{name: "missing contract", config: Config{Executable: executable, Workspace: workspace}, artifact: t.TempDir()},
		{name: "relative executable", config: Config{Executable: filepath.Join("dir", "qwen"), Workspace: workspace, JSONContract: testJSONContract}, artifact: t.TempDir()},
		{name: "other PATH executable", config: Config{Executable: "other-cli", Workspace: workspace, JSONContract: testJSONContract}, artifact: t.TempDir()},
		{name: "missing artifact", config: Config{Executable: executable, Workspace: workspace, JSONContract: testJSONContract}, artifact: filepath.Join(t.TempDir(), "missing")},
		{name: "artifact in workspace", config: Config{Executable: executable, Workspace: workspace, JSONContract: testJSONContract}, artifact: filepath.Join(workspace, "artifact")},
		{name: "artifact contains workspace", config: Config{Executable: executable, Workspace: workspace, JSONContract: testJSONContract}, artifact: parentArtifact},
	}
	if err := os.Mkdir(filepath.Join(workspace, "artifact"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			started := false
			deps := defaultDependencies()
			deps.command = func(string, ...string) *exec.Cmd {
				started = true
				return exec.Command(executable)
			}
			process := newProcess(test.config, test.artifact, deps)
			err := process.Start()
			if err == nil || !errors.Is(err, ErrConfiguration) {
				t.Fatalf("Start error = %v", err)
			}
			if started {
				t.Fatal("command factory called for invalid roots")
			}
		})
	}
}

func TestCanonicalGitRootAcceptsLinkedWorktree(t *testing.T) {
	mainRoot := makeGitRoot(t)
	if err := os.WriteFile(filepath.Join(mainRoot, "tracked.txt"), []byte("tracked"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRoot, "add", "tracked.txt")
	runGit(t, mainRoot, "-c", "user.name=Stepan Test", "-c", "user.email=stepan@example.invalid", "commit", "--quiet", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "linked-worktree")
	runGit(t, mainRoot, "worktree", "add", "--quiet", "--detach", worktree)
	got, err := canonicalGitRoot(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if got != canonicalForTest(t, worktree) {
		t.Fatalf("worktree root = %q, want %q", got, canonicalForTest(t, worktree))
	}
}

func TestCanonicalRootsRejectLinkIntoWorkspace(t *testing.T) {
	workspace := makeGitRoot(t)
	link := filepath.Join(t.TempDir(), "artifact-link")
	if err := makeDirectoryLink(link, workspace); err != nil {
		t.Fatal(err)
	}
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: workspace, JSONContract: testJSONContract}, link)
	if err := process.Start(); err == nil || !errors.Is(err, ErrConfiguration) {
		t.Fatalf("Start error = %v", err)
	}
}

func TestStartupFailuresAreClassifiedAndCleanOwnedRoot(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "assignment-failure")
	setDiagnosticSecrets(t)
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv("STEPAN_QWENAPP_READY", readyPath)

	tests := []struct {
		name      string
		configure func(*processDependencies, *recordingJob)
		kind      error
	}{
		{name: "job creation", kind: ErrContainment, configure: func(deps *processDependencies, _ *recordingJob) {
			deps.newJob = func() (processJob, error) { return nil, errors.New("job unavailable") }
		}},
		{name: "job preparation", kind: ErrContainment, configure: func(_ *processDependencies, job *recordingJob) {
			job.prepareErr = errors.New("prepare refused")
		}},
		{name: "child startup", kind: ErrStartup, configure: func(deps *processDependencies, _ *recordingJob) {
			deps.command = func(_ string, args ...string) *exec.Cmd {
				return exec.Command(filepath.Join(t.TempDir(), "missing"), args...)
			}
		}},
		{name: "job assignment", kind: ErrContainment, configure: func(_ *processDependencies, job *recordingJob) {
			job.assignErr = errors.New("assign refused")
			job.beforeAssign = func() { waitForFile(t, readyPath) }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "owned")
			job := &recordingJob{}
			deps := defaultDependencies()
			var command *exec.Cmd
			defaultCommand := deps.command
			deps.command = func(name string, args ...string) *exec.Cmd {
				command = defaultCommand(name, args...)
				return command
			}
			deps.makeRoot = func() (string, error) {
				if err := os.Mkdir(root, 0o700); err != nil {
					return "", err
				}
				return root, nil
			}
			deps.newJob = func() (processJob, error) { return job, nil }
			test.configure(&deps, job)
			process := newProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, "", deps)
			err := process.Start()
			if err == nil || !errors.Is(err, test.kind) {
				t.Fatalf("Start error = %v", err)
			}
			for _, secret := range diagnosticSecretValues() {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("environment value leaked in error: %v", err)
				}
			}
			if process.Stdin() != nil || process.Stdout() != nil {
				t.Fatal("stdio published after failed containment")
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("runtime root remains: %v", statErr)
			}
			if test.name != "job creation" && !job.closed {
				t.Fatal("partial supervisor was not closed")
			}
			if test.name == "job assignment" {
				if command == nil || command.ProcessState == nil || !command.ProcessState.Exited() {
					t.Fatal("partially started child was not reaped")
				}
				diagnostic := process.Diagnostic()
				if strings.Contains(diagnostic, testJSONContract) || !strings.Contains(diagnostic, "[REDACTED]") {
					t.Fatalf("unsafe diagnostic: %q", diagnostic)
				}
				for _, secret := range diagnosticSecretValues() {
					if strings.Contains(diagnostic, secret) {
						t.Fatalf("environment value leaked in diagnostic: %q", diagnostic)
					}
				}
				if len(diagnostic) > maxDiagnosticBytes || !strings.Contains(diagnostic, "[stderr truncated]") {
					t.Fatalf("diagnostic is not bounded: len=%d", len(diagnostic))
				}
			}
			if err := process.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWaitAndCloseAreIdempotent(t *testing.T) {
	t.Setenv("GO_WANT_QWENAPP_FAKE", "wait")
	setDiagnosticSecrets(t)
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	var waitGroup sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsSeen <- process.Wait()
		}()
	}
	waitGroup.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err == nil || !strings.Contains(err.Error(), "natural exit") {
			t.Fatalf("Wait error = %v", err)
		}
		for _, secret := range diagnosticSecretValues() {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("environment value leaked in Wait error: %v", err)
			}
		}
	}
	if code := process.ExitCode(); code == nil || *code != 17 {
		t.Fatalf("exit code = %v", code)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCannotRestartOrStartAfterClose(t *testing.T) {
	process := NewProcess(Config{Executable: absoluteTestExecutable(t), Workspace: makeGitRoot(t), JSONContract: testJSONContract}, t.TempDir())
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if err := process.Start(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Start error = %v", err)
	}
}

type recordingJob struct {
	prepareErr   error
	assignErr    error
	beforeAssign func()
	closed       bool
}

func (job *recordingJob) Prepare(*exec.Cmd) error { return job.prepareErr }

func (job *recordingJob) Assign(*os.Process) error {
	if job.beforeAssign != nil {
		job.beforeAssign()
	}
	return job.assignErr
}

func (job *recordingJob) Close() error {
	job.closed = true
	return nil
}

func makeGitRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "--quiet")
	return root
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	arguments := append([]string{"-C", root}, args...)
	command := exec.Command("git", arguments...)
	command.Env = withoutGitContext(os.Environ())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func setDiagnosticSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("QWEN_API_KEY", "credential-marker-that-must-not-leak")
	t.Setenv("GITHUB_PAT", "github-pat-marker-that-must-not-leak")
	t.Setenv("CI_JOB_JWT", "ci-jwt-marker-that-must-not-leak")
	t.Setenv("SIGNING_MATERIAL", "opaque-signing-marker-that-must-not-leak")
}

func runQwenTreeFake() int {
	switch os.Getenv("STEPAN_QWENAPP_TREE_LEVEL") {
	case "":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_QWENAPP_TREE_LEVEL=child")
		if err := command.Start(); err != nil {
			return 51
		}
		time.Sleep(30 * time.Second)
	case "child":
		command := exec.Command(os.Args[0])
		command.Env = append(os.Environ(), "STEPAN_QWENAPP_TREE_LEVEL=grandchild")
		if err := command.Start(); err != nil {
			return 52
		}
		data, err := json.Marshal([]int{os.Getppid(), os.Getpid(), command.Process.Pid})
		if err != nil || os.WriteFile(os.Getenv("STEPAN_QWENAPP_PID_FILE"), data, 0o600) != nil {
			return 53
		}
		time.Sleep(30 * time.Second)
	case "grandchild":
		time.Sleep(30 * time.Second)
	default:
		return 54
	}
	return 0
}

func waitForQwenPIDs(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			var pids []int
			if json.Unmarshal(data, &pids) == nil && len(pids) == 3 {
				return pids
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake Qwen process tree did not report PIDs")
	return nil
}

func absoluteTestExecutable(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalForTest(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(canonical)
}

func readFakeMetadata(t *testing.T, path string) fakeMetadata {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	var metadata fakeMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func envMap(environment []string) map[string]string {
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return values
}

func assertOnlyIncludeRoot(t *testing.T, args []string, own, sibling string) {
	t.Helper()
	count := 0
	for index, arg := range args {
		if arg != "--include-directories" {
			continue
		}
		count++
		if index+1 >= len(args) || args[index+1] != own {
			t.Fatalf("include root in %#v, want %q", args, own)
		}
	}
	if count != 1 {
		t.Fatalf("include root count = %d in %#v", count, args)
	}
	if slices.Contains(args, sibling) {
		t.Fatalf("sibling root %q leaked into %#v", sibling, args)
	}
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o700); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
