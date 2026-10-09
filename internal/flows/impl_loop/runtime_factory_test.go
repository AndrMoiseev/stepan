package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

type recordingRuntimeFactory struct {
	profiles       []setting.RuntimeProfile
	err            error
	reasoningError error
}

func (factory *recordingRuntimeFactory) Preflight(profile setting.RuntimeProfile) error {
	factory.profiles = append(factory.profiles, profile)
	if profile.Reasoning != "" && factory.reasoningError != nil {
		return factory.reasoningError
	}
	return factory.err
}

func (factory *recordingRuntimeFactory) Create(context.Context, setting.RuntimeProfile) (agentruntime.Runtime, error) {
	return nil, factory.err
}

func TestPrepareRuntimesRejectsMissingFinalReviewerBeforeProviderPreflight(t *testing.T) {
	configuration := runtimeConfiguration(t, `{"low":{},"medium":{},"high":{}}`)
	factory := &recordingRuntimeFactory{}
	_, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"codex": factory})
	if err == nil || !strings.Contains(err.Error(), `role "final_reviewer" references missing profile "ultra"`) {
		t.Fatalf("PrepareRuntimes error = %v", err)
	}
	if len(factory.profiles) != 0 {
		t.Fatalf("provider preflight ran before every role resolved: %#v", factory.profiles)
	}
}

func TestPrepareRuntimesRejectsUnsupportedReasoning(t *testing.T) {
	configuration := runtimeConfiguration(t, `{"low":{"reasoning":"low"},"medium":{},"high":{},"ultra":{}}`)
	factory := &recordingRuntimeFactory{reasoningError: errors.New("reasoning is unsupported")}
	_, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{"codex": factory})
	if err == nil || !strings.Contains(err.Error(), `role "explorer" profile "low": reasoning is unsupported`) {
		t.Fatalf("PrepareRuntimes error = %v", err)
	}
}

func TestPrepareRuntimesUsesOnlyConfiguredProviderFactories(t *testing.T) {
	configuration := runtimeConfiguration(t, `{"low":{},"medium":{},"high":{},"ultra":{}}`)
	codex := &recordingRuntimeFactory{}
	unusedClaude := &recordingRuntimeFactory{err: errors.New("should not be called")}
	unusedNessy := &recordingRuntimeFactory{err: errors.New("should not be called")}
	prepared, err := PrepareRuntimes(configuration, map[string]RuntimeFactory{
		"codex":  codex,
		"claude": unusedClaude,
		"nessy":  unusedNessy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(codex.profiles) != len(loopRuntimeRoles) || len(unusedClaude.profiles) != 0 || len(unusedNessy.profiles) != 0 {
		t.Fatalf("factory calls codex=%d claude=%d nessy=%d", len(codex.profiles), len(unusedClaude.profiles), len(unusedNessy.profiles))
	}
	final, ok := prepared.Role(setting.RoleFinalReviewer)
	if !ok || final.Profile.Name != "ultra" || final.Profile.Model != "model-ultra" || final.Factory != codex {
		t.Fatalf("final reviewer binding = %#v, configured=%t", final, ok)
	}
}

func runtimeConfiguration(t *testing.T, profiles string) setting.Configuration {
	t.Helper()
	var values map[string]map[string]any
	if err := json.Unmarshal([]byte(profiles), &values); err != nil {
		t.Fatal(err)
	}
	for name, profile := range values {
		if _, present := profile["provider"]; !present {
			profile["provider"] = "codex"
		}
		if _, present := profile["model"]; !present {
			profile["model"] = "model-" + name
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := setting.Merge(loopTestSources([]byte(`{"profiles":`+string(encoded)+`}`), nil))
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}
