package impl_loop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

type bootstrapRuntime struct {
	configs []agentruntime.ThreadConfig
	closed  int
}

func (runtime *bootstrapRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	runtime.configs = append(runtime.configs, config.Clone())
	return len(runtime.configs), nil
}
func (*bootstrapRuntime) RunTurn(agentruntime.Thread, string) (json.RawMessage, error) {
	return nil, nil
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
