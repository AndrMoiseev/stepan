package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/claudeapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/qwenapp"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func TestPreflightRejectsUnsupportedPlatformBeforeHandles(t *testing.T) {
	err := preflight("linux", "amd64", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "require windows/amd64") {
		t.Fatalf("preflight error = %v", err)
	}
}

func TestComposePlanningFlowBuildsDurableApplication(t *testing.T) {
	root := t.TempDir()
	session := specflow.NewSession(func(context.Context) (agentruntime.Runtime, error) { return nil, context.Canceled })
	application, registry, err := composePlanningFlow(root, session, agentConfig{kind: agentCodex, executable: "codex"})
	if err != nil || application == nil || registry == nil {
		t.Fatalf("compose planning flow = %T, %T, %v", application, registry, err)
	}
}

func TestRunRejectsClaudeWithoutCLIPathBeforeTerminalPreflight(t *testing.T) {
	if code := run(context.Background(), []string{"--agent", "claude"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
}

func TestPreflightRejectsRedirectedInput(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "redirected")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = preflight("windows", "amd64", file, file)
	if err == nil || !strings.Contains(err.Error(), "stdin must be a Windows console") {
		t.Fatalf("preflight error = %v", err)
	}
}

type compositionRuntime struct {
	configs []agentruntime.ThreadConfig
	prompts []string
}

func (runtime *compositionRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.configs = append(runtime.configs, config.Clone())
	return len(runtime.configs), nil
}

func (runtime *compositionRuntime) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	index, ok := thread.(int)
	if !ok || index < 1 || index > len(runtime.configs) {
		return nil, errors.New("invalid composition test thread")
	}
	runtime.prompts = append(runtime.prompts, prompt)
	if strings.Contains(string(runtime.configs[index-1].OutputSchema), `"feature_id"`) {
		return json.RawMessage(`{"feature_id":"qwen-composition"}`), nil
	}
	return json.RawMessage(`{"kind":"message","message":"What should the intent guarantee?","decisions":[]}`), nil
}

func (*compositionRuntime) CloseThread(agentruntime.Thread) error { return nil }
func (*compositionRuntime) Interrupt() error                      { return nil }
func (*compositionRuntime) Close() error                          { return nil }

func TestQwenCompositionUsesOnlySelectedExecutableAndEphemeralIdentity(t *testing.T) {
	pathDirectory := t.TempDir()
	pathName := "qwen"
	if runtime.GOOS == "windows" {
		pathName += ".exe"
	}
	pathExecutable := filepath.Join(pathDirectory, pathName)
	if err := os.WriteFile(pathExecutable, []byte("fake PATH executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	explicit := filepath.Join(t.TempDir(), "corporate-compatible-agent.exe")
	if err := os.WriteFile(explicit, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		args       []string
		executable string
	}{
		{name: "PATH default", args: []string{"--agent", "qwen"}, executable: "qwen"},
		{name: "explicit nonstandard basename", args: []string{"--agent", "qwen", "--agent-cli", explicit}, executable: explicit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := parseAgentConfig(test.args)
			if err != nil {
				t.Fatal(err)
			}
			root := initializeCompositionRepository(t)
			providerRuntime := &compositionRuntime{}
			var captured qwenapp.Config
			qwenStarts, otherStarts := 0, 0
			starters := runtimeStarters{
				codex: func(string, string) (agentruntime.Runtime, error) {
					otherStarts++
					return nil, errors.New("unexpected Codex fallback")
				},
				claude: func(context.Context, claudeapp.Config) (agentruntime.Runtime, error) {
					otherStarts++
					return nil, errors.New("unexpected Claude fallback")
				},
				qwen: func(value qwenapp.Config) (agentruntime.Runtime, error) {
					qwenStarts++
					captured = value
					return providerRuntime, nil
				},
			}
			session := specflow.NewSession(runtimeFactoryWithStarters(config, root, starters))
			t.Cleanup(func() { _ = session.Close() })
			application, registry, err := composePlanningFlow(root, session, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = registry.Close() })

			progress, err := application.StartFeature("Exercise Qwen composition")
			if err != nil {
				t.Fatal(err)
			}
			if qwenStarts != 1 || otherStarts != 0 {
				t.Fatalf("provider starts: qwen=%d other=%d", qwenStarts, otherStarts)
			}
			if captured.Executable != test.executable || captured.Workspace != root {
				t.Fatalf("Qwen config selection = %#v", captured)
			}
			resolved, err := qwenapp.ResolveExecutable(captured.Executable)
			if err != nil {
				t.Fatal(err)
			}
			wantResolved := explicit
			if captured.Executable == "qwen" {
				wantResolved = pathExecutable
			}
			if resolved != filepath.Clean(wantResolved) {
				t.Fatalf("resolved selected executable = %q, want %q", resolved, wantResolved)
			}
			if captured.JSONContract != qwenapp.JSONContract || string(captured.EnvelopeSchema) != string(specflow.FlowEnvelopeSchema()) {
				t.Fatalf("Qwen structured contract = %#v", captured)
			}
			if got := runtimeIdentity(config); got != (specflow.RuntimeIdentity{Provider: "qwen", Model: "default"}) {
				t.Fatalf("runtime identity = %#v", got)
			}
			if len(providerRuntime.configs) != 2 || !strings.Contains(providerRuntime.configs[1].BootstrapInstructions, "Provider: qwen\nModel: default") {
				t.Fatalf("thread runtime context = %#v", providerRuntime.configs)
			}

			statePath := filepath.Join(root, "docs", "changes", "features", progress.FeatureID, "state.json")
			state, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			assertNoRuntimeIdentity(t, "state.json", state)
			fingerprint, err := specflow.NewFingerprint("document-hash", []specflow.UpstreamHash{{Stage: specflow.StageIntent, Hash: "upstream-hash"}})
			if err != nil {
				t.Fatal(err)
			}
			encodedFingerprint, err := json.Marshal(fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			assertNoRuntimeIdentity(t, "fingerprint", encodedFingerprint)
		})
	}
}

func TestRuntimeFactoryRejectsUnknownKindWithoutProviderFallback(t *testing.T) {
	starts := 0
	starters := runtimeStarters{
		codex: func(string, string) (agentruntime.Runtime, error) { starts++; return &compositionRuntime{}, nil },
		claude: func(context.Context, claudeapp.Config) (agentruntime.Runtime, error) {
			starts++
			return &compositionRuntime{}, nil
		},
		qwen: func(qwenapp.Config) (agentruntime.Runtime, error) { starts++; return &compositionRuntime{}, nil },
	}
	runtime, err := runtimeFactoryWithStarters(agentConfig{kind: agentKind("unknown")}, t.TempDir(), starters)(context.Background())
	if runtime != nil || !errors.Is(err, agentruntime.ErrRuntimeConfiguration) || starts != 0 {
		t.Fatalf("unknown provider factory = %#v, %v; starts=%d", runtime, err, starts)
	}
}

func TestQwenStartupFailureIsClassifiedBeforeDurableFlow(t *testing.T) {
	root := initializeCompositionRepository(t)
	t.Setenv("PATH", t.TempDir())
	config, err := parseAgentConfig([]string{"--agent", "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	session := specflow.NewSession(runtimeFactory(config, root))
	t.Cleanup(func() { _ = session.Close() })
	application, registry, err := composePlanningFlow(root, session, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	progress, err := application.StartFeature("Qwen must be available")
	if err == nil || !errors.Is(err, agentruntime.ErrRuntimeConfiguration) || !strings.Contains(err.Error(), "qwen start thread") {
		t.Fatalf("Qwen startup result = %#v, %v", progress, err)
	}
	features := filepath.Join(root, "docs", "changes", "features")
	if _, statErr := os.Stat(features); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("startup failure entered durable flow: %v", statErr)
	}
}

func TestRuntimeFactoryPreservesCodexAndClaudeComposition(t *testing.T) {
	root := t.TempDir()
	marker := &compositionRuntime{}
	var codexExecutable, codexWorkspace string
	var claudeConfig claudeapp.Config
	starters := runtimeStarters{
		codex: func(executable, workspace string) (agentruntime.Runtime, error) {
			codexExecutable, codexWorkspace = executable, workspace
			return marker, nil
		},
		claude: func(_ context.Context, config claudeapp.Config) (agentruntime.Runtime, error) {
			claudeConfig = config
			return marker, nil
		},
		qwen: func(qwenapp.Config) (agentruntime.Runtime, error) {
			return nil, errors.New("unexpected Qwen start")
		},
	}

	runtime, err := runtimeFactoryWithStarters(agentConfig{kind: agentCodex, executable: "codex"}, root, starters)(context.Background())
	if err != nil || runtime != marker || codexExecutable != "codex" || codexWorkspace != root {
		t.Fatalf("Codex composition = %#v, %v, executable=%q workspace=%q", runtime, err, codexExecutable, codexWorkspace)
	}
	claudeExecutable := filepath.Join(root, "corporate-claude")
	runtime, err = runtimeFactoryWithStarters(agentConfig{kind: agentClaude, executable: claudeExecutable}, root, starters)(context.Background())
	if err != nil || runtime != marker || claudeConfig.Executable != claudeExecutable || claudeConfig.Workspace != root || string(claudeConfig.EnvelopeSchema) != string(specflow.FlowEnvelopeSchema()) {
		t.Fatalf("Claude composition = %#v, %v, config=%#v", runtime, err, claudeConfig)
	}
}

func assertNoRuntimeIdentity(t *testing.T, name string, data []byte) {
	t.Helper()
	lower := strings.ToLower(string(data))
	for _, forbidden := range []string{"qwen", "default", "provider", "model", "process", "session_id"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("%s contains runtime identity %q: %s", name, forbidden, data)
		}
	}
}

func initializeCompositionRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("composition fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init"},
		{"config", "user.name", "Stepan Tests"},
		{"config", "user.email", "stepan-tests@example.invalid"},
		{"config", "commit.gpgsign", "false"},
		{"add", "README.md"},
		{"commit", "-m", "initial fixture"},
	}
	for _, args := range commands {
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	return root
}
