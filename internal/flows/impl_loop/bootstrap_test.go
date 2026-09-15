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

func TestBootstrapConfigurationProposalValidatesDiffsAndPreservesUnrelatedSettings(t *testing.T) {
	directory := t.TempDir()
	paths := BootstrapConfigurationPaths{User: filepath.Join(directory, "user.json"), Project: filepath.Join(directory, "project.json")}
	writeBootstrapFile(t, directory, "user.json", `{"nessy":{"auth_token":"user-secret"},"unrelated":{"keep":true},"implementation":{"profiles":{"old":{"provider":"test","model":"old"}}}}`)
	writeBootstrapFile(t, directory, "project.json", `{"authorization":"project-secret","other":{"keep":"yes"},"implementation":{"limits":{"exploration_limit":1}}}`)
	response := bootstrapConfigurationResponse(t, `{"profiles":{"high":{"provider":"test","model":"high"}},"roles":{"bootstrapper":"high"}}`, validBootstrapProjectImplementation())

	proposal, err := PrepareBootstrapConfigurationProposal(paths, response)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Diffs) != 2 || proposal.Diffs[0].Path != paths.User || proposal.Diffs[1].Path != paths.Project {
		t.Fatalf("proposal diffs = %#v", proposal.Diffs)
	}
	for _, diff := range proposal.Diffs {
		if strings.Contains(diff.Diff, "user-secret") || strings.Contains(diff.Diff, "project-secret") || strings.Contains(diff.Diff, "authorization") {
			t.Fatalf("unsafe bootstrap diff: %s", diff.Diff)
		}
	}
	if err := SaveBootstrapConfigurationProposal(proposal); err != nil {
		t.Fatal(err)
	}
	assertBootstrapSettings(t, paths.User, `{"nessy":{"auth_token":"user-secret"},"unrelated":{"keep":true},"implementation":{"profiles":{"high":{"provider":"test","model":"high"}},"roles":{"bootstrapper":"high"}}}`)
	assertBootstrapSettings(t, paths.Project, `{"authorization":"project-secret","other":{"keep":"yes"},"implementation":{"checks":{"unit":{"kind":"tests","command":{"program":"go","args":["test"]}}},"required_checks":["unit"]}}`)
}

func TestBootstrapConfigurationProposalRejectsWithoutWriting(t *testing.T) {
	directory := t.TempDir()
	paths := BootstrapConfigurationPaths{User: filepath.Join(directory, "user.json"), Project: filepath.Join(directory, "project.json")}
	writeBootstrapFile(t, directory, "user.json", `{"auth_token":"user-secret","implementation":{"profiles":{"high":{"provider":"test","model":"old"}}}}`)
	writeBootstrapFile(t, directory, "project.json", `{"authorization":"project-secret","implementation":{}}`)
	response := bootstrapConfigurationResponse(t, `{"profiles":{"high":{"provider":"test","model":"new"}}}`, `{}`)
	proposal, err := PrepareBootstrapConfigurationProposal(paths, response)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmAndSaveBootstrapConfiguration(context.Background(), proposal, func(context.Context, []BootstrapConfigurationDiff) (bool, error) { return false, nil })
	if err != nil || confirmed {
		t.Fatalf("rejected proposal = confirmed=%t err=%v", confirmed, err)
	}
	data, err := os.ReadFile(paths.User)
	if err != nil || strings.Contains(string(data), `"model":"new"`) || !strings.Contains(string(data), "user-secret") {
		t.Fatalf("rejection changed user settings: %q, %v", data, err)
	}
}

func TestBootstrapConfigurationProposalRejectsInvalidTypedInputWithoutSecrets(t *testing.T) {
	directory := t.TempDir()
	paths := BootstrapConfigurationPaths{User: filepath.Join(directory, "user.json"), Project: filepath.Join(directory, "project.json")}
	for _, test := range []struct {
		name    string
		user    string
		project string
	}{
		{name: "invalid json", user: `{`, project: `{}`},
		{name: "project field at user level", user: `{"checks":{}}`, project: `{}`},
		{name: "malformed profile", user: `{"profiles":{"high":{"provider":"test"}}}`, project: `{}`},
		{name: "unknown field", user: `{"auth_token":"proposal-secret"}`, project: `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := bootstrapConfigurationResponse(t, test.user, test.project)
			_, err := PrepareBootstrapConfigurationProposal(paths, response)
			if err == nil || strings.Contains(err.Error(), "proposal-secret") {
				t.Fatalf("proposal error = %v", err)
			}
		})
	}
}

func TestBootstrapModeSavesOnlyAfterConfirmationAndExitsWithoutImplementation(t *testing.T) {
	repository := t.TempDir()
	directory := t.TempDir()
	paths := BootstrapConfigurationPaths{User: filepath.Join(directory, "user.json"), Project: filepath.Join(repository, ".stepan", "settings.json")}
	writeBootstrapFile(t, directory, "user.json", `{"auth_token":"user-secret","implementation":{"profiles":{"high":{"provider":"bootstrap","model":"high"}}}}`)
	writeBootstrapFile(t, repository, ".stepan/settings.json", `{"authorization":"project-secret","implementation":{}}`)
	runtime := &bootstrapRuntime{turns: []json.RawMessage{bootstrapConfigurationPayload(t, `{"profiles":{"high":{"provider":"bootstrap","model":"new-high"}}}`, validBootstrapProjectImplementation())}}
	presented := 0
	err := runBootstrapperMode(context.Background(), BootstrapperModeInput{
		Repository: repository, Factories: map[string]RuntimeFactory{"bootstrap": &bootstrapFactory{runtime: runtime}}, Base: agentruntime.ThreadConfig{Workspace: repository},
		ConfirmConfiguration: func(_ context.Context, diffs []BootstrapConfigurationDiff) (bool, error) {
			presented += len(diffs)
			return true, nil
		},
	}, implementationconfig.Sources{User: json.RawMessage(`{"profiles":{"high":{"provider":"bootstrap","model":"high"}}}`), Project: json.RawMessage(`{}`)}, paths)
	if err != nil {
		t.Fatal(err)
	}
	if presented != 2 || len(runtime.messages) != 1 {
		t.Fatalf("diffs shown=%d turns=%d; bootstrap must finish after its proposal", presented, len(runtime.messages))
	}
	data, err := os.ReadFile(paths.User)
	if err != nil || !strings.Contains(string(data), `"model": "new-high"`) || !strings.Contains(string(data), "user-secret") {
		t.Fatalf("accepted proposal did not safely persist: %q, %v", data, err)
	}
}

func bootstrapConfigurationResponse(t *testing.T, user, project string) AgentResponse {
	t.Helper()
	userCopy, projectCopy, explanation := user, project, "bootstrap configuration"
	return AgentResponse{Kind: ResponseConfigurationProposed, UserImplementation: &userCopy, ProjectImplementation: &projectCopy, Explanation: &explanation}
}

func bootstrapConfigurationPayload(t *testing.T, user, project string) json.RawMessage {
	t.Helper()
	payload := responsePayloadMap(ResponseConfigurationProposed)
	payload["user_implementation"], payload["project_implementation"] = user, project
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func validBootstrapProjectImplementation() string {
	return `{"checks":{"unit":{"kind":"tests","command":{"program":"go","args":["test"]}}},"required_checks":["unit"]}`
}

func assertBootstrapSettings(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("settings mismatch: got %s want %s", gotJSON, wantJSON)
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
