package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

type bootstrapRuntime struct {
	configs  []agentruntime.ThreadConfig
	closed   int
	turns    []json.RawMessage
	messages []string
}

func (runtime *bootstrapRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.configs = append(runtime.configs, config.Clone())
	return len(runtime.configs), nil
}
func (runtime *bootstrapRuntime) RunTurn(_ agentruntime.Thread, message string) (json.RawMessage, error) {
	runtime.messages = append(runtime.messages, message)
	if len(runtime.turns) == 0 {
		return nil, errors.New("unexpected bootstrap turn")
	}
	response := runtime.turns[0]
	runtime.turns = runtime.turns[1:]
	return response, nil
}
func (*bootstrapRuntime) CloseThread(agentruntime.Thread) error { return nil }
func (*bootstrapRuntime) Interrupt() error                      { return nil }
func (runtime *bootstrapRuntime) Close() error                  { runtime.closed++; return nil }

type bootstrapFactory struct {
	profiles []implementationconfig.RuntimeProfile
	runtime  *bootstrapRuntime
}

func (factory *bootstrapFactory) Preflight(profile implementationconfig.RuntimeProfile) error {
	factory.profiles = append(factory.profiles, profile)
	return nil
}
func (factory *bootstrapFactory) Create(context.Context, implementationconfig.RuntimeProfile) (agentruntime.Runtime, error) {
	return factory.runtime, nil
}

func TestStartBootstrapperUsesDefaultHighProfileInDedicatedReadOnlySession(t *testing.T) {
	configuration := bootstrapConfiguration(t, `{"high":{"provider":"test","model":"high-model","reasoning":"high"}}`, "")
	factory := &bootstrapFactory{runtime: &bootstrapRuntime{}}
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := StartBootstrapper(context.Background(), BootstrapperInput{
		Configuration: configuration, Factories: map[string]RuntimeFactory{"test": factory}, Base: agentruntime.ThreadConfig{Workspace: t.TempDir()}, Start: start,
		SelectProfile: func(context.Context) (implementationconfig.RuntimeProfile, error) {
			t.Fatal("configured high profile should not prompt")
			return implementationconfig.RuntimeProfile{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Profile.Name != "high" || len(factory.profiles) != 1 || factory.profiles[0] != session.Profile {
		t.Fatalf("bootstrap profile = %#v; preflight = %#v", session.Profile, factory.profiles)
	}
	if len(factory.runtime.configs) != 1 || factory.runtime.configs[0].WorkspaceWriteAllowed || !containsBootstrapperSchema(factory.runtime.configs[0].OutputSchema) {
		t.Fatalf("bootstrap thread config = %#v", factory.runtime.configs)
	}
	if err := session.Close(); err != nil || factory.runtime.closed != 1 {
		t.Fatalf("close = %v; runtime closes=%d", err, factory.runtime.closed)
	}
}

func TestStartBootstrapperPromptsForTemporaryProfileWhenHighIsAbsent(t *testing.T) {
	configuration := bootstrapConfiguration(t, `{}`, "")
	factory := &bootstrapFactory{runtime: &bootstrapRuntime{}}
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	prompts := 0
	session, err := StartBootstrapper(context.Background(), BootstrapperInput{
		Configuration: configuration, Factories: map[string]RuntimeFactory{"chosen": factory}, Base: agentruntime.ThreadConfig{Workspace: t.TempDir()}, Start: start,
		SelectProfile: func(context.Context) (implementationconfig.RuntimeProfile, error) {
			prompts++
			return implementationconfig.RuntimeProfile{Provider: "chosen", Model: "selected-model", Reasoning: "selected-reasoning"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if prompts != 1 || session.Profile != (implementationconfig.RuntimeProfile{Name: "bootstrapper-selected", Provider: "chosen", Model: "selected-model", Reasoning: "selected-reasoning"}) {
		t.Fatalf("prompt calls=%d profile=%#v", prompts, session.Profile)
	}
}

func TestBuildBootstrapperStartContextBindsOnlyCurrentRepository(t *testing.T) {
	repository := t.TempDir()
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	if start.Role != ResponseRoleBootstrapper || !strings.Contains(start.StartMessage, repository) || strings.Contains(start.StartMessage, "Run:") {
		t.Fatalf("bootstrap start context = %#v", start)
	}
}

func bootstrapConfiguration(t *testing.T, profiles, roles string) implementationconfig.Configuration {
	t.Helper()
	raw := `{"profiles":` + profiles
	if roles != "" {
		raw += `,"roles":` + roles
	}
	raw += `}`
	configuration, err := implementationconfig.Merge(implementationconfig.Sources{User: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func containsBootstrapperSchema(schema []byte) bool {
	return strings.Contains(string(schema), `"configuration_proposed"`)
}

func TestBuildBootstrapperProjectContextIncludesProjectCIAndScriptsWithoutAuthorization(t *testing.T) {
	repository := t.TempDir()
	writeBootstrapFile(t, repository, "go.mod", "module example.com/project\n")
	writeBootstrapFile(t, repository, ".github/workflows/test.yml", "name: test\nenv:\n  API_TOKEN: actual-token\n")
	writeBootstrapFile(t, repository, "scripts/check.ps1", "$env:AUTHORIZATION = 'actual-authorization'\nWrite-Output test\n")
	context, err := BuildBootstrapperProjectContext(repository, implementationconfig.Sources{
		User:    json.RawMessage(`{"profiles":{"high":{"provider":"test","model":"high"}},"auth_token":"user-token"}`),
		Project: json.RawMessage(`{"checks":{},"nested":{"password":"project-password"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := BuildBootstrapperRequest(context)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go.mod", ".github/workflows/test.yml", "scripts/check.ps1", "no authority to execute checks"} {
		if !strings.Contains(request, want) {
			t.Fatalf("bootstrap request missing %q:\n%s", want, request)
		}
	}
	for _, secret := range []string{"actual-token", "actual-authorization", "user-token", "project-password"} {
		if strings.Contains(request, secret) {
			t.Fatalf("bootstrap request leaked authorization %q:\n%s", secret, request)
		}
	}
}

func TestBootstrapperControllerRoutesExplorerUntilExactLimitWithoutRunningChecks(t *testing.T) {
	configuration := bootstrapConfiguration(t, `{"high":{"provider":"bootstrap","model":"high"},"low":{"provider":"explorer","model":"low"}}`, `{"bootstrapper":"high","explorer":"low"}`)
	configuration.Limits["exploration_limit"] = json.RawMessage(`1`)
	bootstrapAgent := &bootstrapRuntime{turns: []json.RawMessage{bootstrapResponse(t, ResponseExplorationRequested), bootstrapResponse(t, ResponseExplorationRequested)}}
	explorerRuntime := &bootstrapRuntime{turns: []json.RawMessage{bootstrapResponse(t, ResponseExplorationResult)}}
	repository := t.TempDir()
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	session, err := StartBootstrapper(context.Background(), BootstrapperInput{Configuration: configuration, Factories: map[string]RuntimeFactory{
		"bootstrap": &bootstrapFactory{runtime: bootstrapAgent}, "explorer": &bootstrapFactory{runtime: explorerRuntime},
	}, Base: agentruntime.ThreadConfig{Workspace: repository}, Start: start})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	project := BootstrapProjectContext{Repository: repository}
	_, err = NewBootstrapperController(session, configuration, project).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Explorer limit of 1") {
		t.Fatalf("controller error = %v, want exact Explorer-limit rejection", err)
	}
	if len(bootstrapAgent.messages) != 2 || len(explorerRuntime.messages) != 1 {
		t.Fatalf("turns bootstrap=%d explorer=%d; want 2 and 1", len(bootstrapAgent.messages), len(explorerRuntime.messages))
	}
	if !strings.Contains(explorerRuntime.messages[0], "Do not run commands") {
		t.Fatalf("Explorer request lacks command prohibition: %q", explorerRuntime.messages[0])
	}
}

func TestBootstrapperControllerRejectsChecksRequestedBeforeAnyCommandCanRun(t *testing.T) {
	configuration := bootstrapConfiguration(t, `{"high":{"provider":"bootstrap","model":"high"}}`, `{"bootstrapper":"high"}`)
	runtime := &bootstrapRuntime{turns: []json.RawMessage{bootstrapResponse(t, ResponseChecksRequested)}}
	repository := t.TempDir()
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: repository})
	if err != nil {
		t.Fatal(err)
	}
	session, err := StartBootstrapper(context.Background(), BootstrapperInput{Configuration: configuration, Factories: map[string]RuntimeFactory{"bootstrap": &bootstrapFactory{runtime: runtime}}, Base: agentruntime.ThreadConfig{Workspace: repository}, Start: start})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	_, err = NewBootstrapperController(session, configuration, BootstrapProjectContext{Repository: repository, UserSettings: `{"auth_token":"hidden"}`}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrResponseRole.Error()) {
		t.Fatalf("checks_requested error = %v", err)
	}
	if len(runtime.messages) != 1 || strings.Contains(runtime.messages[0], "hidden") {
		t.Fatalf("bootstrap turn count=%d or leaked authorization in request=%q", len(runtime.messages), runtime.messages)
	}
}

func bootstrapResponse(t *testing.T, kind ResponseKind) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(responsePayloadMap(kind))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeBootstrapFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
