// Package runtime composes provider adapters for the
// implementation loop. The loop itself depends only on agentruntime's
// provider-neutral Runtime interface and its RuntimeFactory contract.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/claudeapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/nessyapp"
	implloop "github.com/AndrMoiseev/stepan/internal/flows/impl_loop"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// FactoryOptions supplies the process-wide values that are independent from a
// role profile. Credentials remain a callback so they are loaded only when a
// Nessy role is actually used and never enter implementation settings.
type FactoryOptions struct {
	Workspace         string
	EnvelopeSchema    json.RawMessage
	CodexExecutable   string
	ClaudeExecutable  string
	NessyAuthToken    func() (string, error)
	NessyJSONContract string
}

// NewFactories creates production factories for every supported provider.
// They do no work until a profile resolves to that provider.
func NewFactories(options FactoryOptions) map[string]implloop.RuntimeFactory {
	return newFactories(options, defaultStarters())
}

type starters struct {
	codex  func(context.Context, codexapp.RuntimeConfig) (agentruntime.Runtime, error)
	claude func(context.Context, claudeapp.Config) (agentruntime.Runtime, error)
	nessy  func(nessyapp.Config) (agentruntime.Runtime, error)
}

func defaultStarters() starters {
	return starters{
		codex: func(ctx context.Context, config codexapp.RuntimeConfig) (agentruntime.Runtime, error) {
			return codexapp.StartRuntimeWithConfigContext(ctx, config)
		},
		claude: func(ctx context.Context, config claudeapp.Config) (agentruntime.Runtime, error) {
			return claudeapp.StartRuntime(ctx, config)
		},
		nessy: func(config nessyapp.Config) (agentruntime.Runtime, error) {
			return nessyapp.StartRuntime(config)
		},
	}
}

func newFactories(options FactoryOptions, factoryStarters starters) map[string]implloop.RuntimeFactory {
	if options.CodexExecutable == "" {
		options.CodexExecutable = "codex"
	}
	if options.ClaudeExecutable == "" {
		options.ClaudeExecutable = "claude"
	}
	if options.NessyJSONContract == "" {
		options.NessyJSONContract = nessyapp.JSONContract
	}
	return map[string]implloop.RuntimeFactory{
		"codex": providerFactory{
			preflight: func(profile setting.RuntimeProfile) error {
				return codexapp.ValidateRuntimeConfig(codexConfig(options, profile))
			},
			create: func(ctx context.Context, profile setting.RuntimeProfile) (agentruntime.Runtime, error) {
				return factoryStarters.codex(ctx, codexConfig(options, profile))
			},
		},
		"claude": providerFactory{
			preflight: func(profile setting.RuntimeProfile) error {
				return claudeapp.ValidateRuntimeConfig(claudeConfig(options, profile))
			},
			create: func(ctx context.Context, profile setting.RuntimeProfile) (agentruntime.Runtime, error) {
				return factoryStarters.claude(ctx, claudeConfig(options, profile))
			},
		},
		"nessy": providerFactory{
			preflight: func(profile setting.RuntimeProfile) error {
				config, err := nessyConfig(options, profile)
				if err != nil {
					return err
				}
				return nessyapp.ValidateRuntimeConfig(config)
			},
			create: func(_ context.Context, profile setting.RuntimeProfile) (agentruntime.Runtime, error) {
				config, err := nessyConfig(options, profile)
				if err != nil {
					return nil, err
				}
				return factoryStarters.nessy(config)
			},
		},
	}
}

type providerFactory struct {
	preflight func(setting.RuntimeProfile) error
	create    func(context.Context, setting.RuntimeProfile) (agentruntime.Runtime, error)
}

func (factory providerFactory) Preflight(profile setting.RuntimeProfile) error {
	if factory.preflight == nil {
		return errors.New("runtime preflight is unavailable")
	}
	return factory.preflight(profile)
}

func (factory providerFactory) Create(ctx context.Context, profile setting.RuntimeProfile) (agentruntime.Runtime, error) {
	if factory.create == nil {
		return nil, errors.New("runtime creation is unavailable")
	}
	return factory.create(ctx, profile)
}

func codexConfig(options FactoryOptions, profile setting.RuntimeProfile) codexapp.RuntimeConfig {
	return codexapp.RuntimeConfig{Executable: options.CodexExecutable, Workspace: options.Workspace, Model: profile.Model, Reasoning: profile.Reasoning}
}

func claudeConfig(options FactoryOptions, profile setting.RuntimeProfile) claudeapp.Config {
	return claudeapp.Config{Executable: options.ClaudeExecutable, Workspace: options.Workspace, EnvelopeSchema: append(json.RawMessage(nil), options.EnvelopeSchema...), Model: profile.Model, Reasoning: profile.Reasoning}
}

func nessyConfig(options FactoryOptions, profile setting.RuntimeProfile) (nessyapp.Config, error) {
	if options.NessyAuthToken == nil {
		return nessyapp.Config{}, errors.New("nessy authentication loader is required")
	}
	token, err := options.NessyAuthToken()
	if err != nil {
		return nessyapp.Config{}, fmt.Errorf("load Nessy authentication: %w", err)
	}
	return nessyapp.Config{AuthToken: token, Workspace: options.Workspace, JSONContract: options.NessyJSONContract, EnvelopeSchema: append(json.RawMessage(nil), options.EnvelopeSchema...), Model: profile.Model, Reasoning: profile.Reasoning}, nil
}
