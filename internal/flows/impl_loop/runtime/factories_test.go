package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/claudeapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/nessyapp"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

type inertRuntime struct{}

func (inertRuntime) StartThread(agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return nil, errors.New("not used")
}

func (inertRuntime) RunTurn(agentruntime.Thread, string) (json.RawMessage, error) {
	return nil, errors.New("not used")
}
func (inertRuntime) CloseThread(agentruntime.Thread) error { return nil }
func (inertRuntime) Interrupt() error                      { return nil }
func (inertRuntime) Close() error                          { return nil }

func TestProductionFactoriesRejectMissingUsedCLI(t *testing.T) {
	factories := NewFactories(FactoryOptions{
		Workspace:       t.TempDir(),
		CodexExecutable: "definitely-missing-stepan-codex-cli",
	})
	err := factories["codex"].Preflight(setting.RuntimeProfile{Name: "medium", Provider: "codex", Model: "gpt"})
	if err == nil || !strings.Contains(err.Error(), "resolve executable") {
		t.Fatalf("missing Codex CLI preflight = %v", err)
	}
}

func TestProductionFactoriesRejectUnsupportedNessyReasoning(t *testing.T) {
	factories := NewFactories(FactoryOptions{
		Workspace:      t.TempDir(),
		NessyAuthToken: func() (string, error) { return "test-token", nil },
	})
	err := factories["nessy"].Preflight(setting.RuntimeProfile{Name: "low", Provider: "nessy", Model: "qwen", Reasoning: "high"})
	if err == nil || !strings.Contains(err.Error(), "does not support configured reasoning") {
		t.Fatalf("Nessy reasoning preflight = %v", err)
	}
}

func TestProductionFactoriesPassProfileModelAndReasoningToStarters(t *testing.T) {
	var codexSeen codexapp.RuntimeConfig
	var claudeSeen claudeapp.Config
	var nessySeen nessyapp.Config
	factories := newFactories(FactoryOptions{
		Workspace:        t.TempDir(),
		EnvelopeSchema:   json.RawMessage(`{"type":"object"}`),
		CodexExecutable:  "codex-test",
		ClaudeExecutable: "claude-test",
		NessyAuthToken:   func() (string, error) { return "test-token", nil },
	}, starters{
		codex: func(config codexapp.RuntimeConfig) (agentruntime.Runtime, error) {
			codexSeen = config
			return inertRuntime{}, nil
		},
		claude: func(_ context.Context, config claudeapp.Config) (agentruntime.Runtime, error) {
			claudeSeen = config
			return inertRuntime{}, nil
		},
		nessy: func(config nessyapp.Config) (agentruntime.Runtime, error) {
			nessySeen = config
			return inertRuntime{}, nil
		},
	})

	profiles := []setting.RuntimeProfile{
		{Name: "medium", Provider: "codex", Model: "gpt", Reasoning: "high"},
		{Name: "high", Provider: "claude", Model: "sonnet", Reasoning: "max"},
		{Name: "low", Provider: "nessy", Model: "qwen"},
	}
	for _, profile := range profiles {
		if _, err := factories[profile.Provider].Create(context.Background(), profile); err != nil {
			t.Fatalf("create %s runtime: %v", profile.Provider, err)
		}
	}
	if codexSeen.Model != "gpt" || codexSeen.Reasoning != "high" {
		t.Fatalf("Codex config = %#v", codexSeen)
	}
	if claudeSeen.Model != "sonnet" || claudeSeen.Reasoning != "max" {
		t.Fatalf("Claude config = %#v", claudeSeen)
	}
	if nessySeen.Model != "qwen" || nessySeen.Reasoning != "" || nessySeen.AuthToken != "test-token" {
		t.Fatalf("Nessy config = %#v", nessySeen)
	}
}
