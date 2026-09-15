package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

// BootstrapperInput contains only the setup dependencies for one explicit
// `stepan bootstrap` invocation. It deliberately does not use SessionOwner:
// bootstrap conversations never belong to an implementation run and must not
// share an implementation-loop session.
type BootstrapperInput struct {
	Configuration implementationconfig.Configuration
	Factories     map[string]RuntimeFactory
	Base          agentruntime.ThreadConfig
	Start         RoleStartContext
	SelectProfile func(context.Context) (implementationconfig.RuntimeProfile, error)
}

// BootstrapperProfileSelection is the temporary explicit identity selected by
// an operator when no bootstrapper profile has been configured yet.
type BootstrapperProfileSelection struct {
	Provider  string
	Model     string
	Reasoning string
}

// BootstrapperModeInput is the application-facing dependency set for the
// standalone mode. Keeping settings loading in the flow prevents the CLI
// boundary from depending directly on implementation configuration details.
type BootstrapperModeInput struct {
	Repository    string
	Factories     map[string]RuntimeFactory
	Base          agentruntime.ThreadConfig
	SelectProfile func(context.Context) (BootstrapperProfileSelection, error)
	ReportReady   func(BootstrapperProfileSelection)
}

// RunBootstrapperMode prepares exactly one independent bootstrapper session
// for the current repository. It intentionally stops before an agent turn:
// task 13.2 adds the controller-built project request and Explorer routing.
func RunBootstrapperMode(ctx context.Context, input BootstrapperModeInput) error {
	sources, err := implementationconfig.Load(input.Repository)
	if err != nil {
		return err
	}
	configuration, err := implementationconfig.Merge(sources)
	if err != nil {
		return err
	}
	start, err := BuildBootstrapperStartContext(BootstrapperStartInput{Repository: input.Repository})
	if err != nil {
		return err
	}
	session, err := StartBootstrapper(ctx, BootstrapperInput{
		Configuration: configuration, Factories: input.Factories, Base: input.Base, Start: start,
		SelectProfile: func(callCtx context.Context) (implementationconfig.RuntimeProfile, error) {
			if input.SelectProfile == nil {
				return implementationconfig.RuntimeProfile{}, errors.New("bootstrapper profile is not configured; select provider, model, and reasoning")
			}
			selected, err := input.SelectProfile(callCtx)
			return implementationconfig.RuntimeProfile{Provider: selected.Provider, Model: selected.Model, Reasoning: selected.Reasoning}, err
		},
	})
	if err != nil {
		return err
	}
	defer session.Close()
	if input.ReportReady != nil {
		input.ReportReady(BootstrapperProfileSelection{Provider: session.Profile.Provider, Model: session.Profile.Model, Reasoning: session.Profile.Reasoning})
	}
	return nil
}

// BootstrapperSession is the one fresh, process-local bootstrapper session.
// Its caller owns the lifecycle and must close it when the distinct bootstrap
// mode ends. Later bootstrap tasks send the controller-built request through
// RunTurn; the setup path here never starts a check or an implementation run.
type BootstrapperSession struct {
	Profile implementationconfig.RuntimeProfile
	runtime agentruntime.Runtime
	thread  agentruntime.Thread
}

// StartBootstrapper resolves the configured bootstrapper profile, or asks for
// an explicit temporary provider/model/reasoning selection when the default
// high profile does not exist. The selected profile is preflighted separately
// from the implementation-loop roles before a new dedicated thread starts.
func StartBootstrapper(ctx context.Context, input BootstrapperInput) (*BootstrapperSession, error) {
	if input.Start.Role != ResponseRoleBootstrapper {
		return nil, fmt.Errorf("bootstrapper start context must use bootstrapper role")
	}
	if !filepath.IsAbs(input.Base.Workspace) {
		return nil, errors.New("bootstrapper requires an absolute workspace")
	}

	profile, err := bootstrapperProfile(ctx, input)
	if err != nil {
		return nil, err
	}
	factory, ok := input.Factories[profile.Provider]
	if !ok || factory == nil {
		return nil, fmt.Errorf("bootstrapper profile %q uses unsupported provider %q", profile.Name, profile.Provider)
	}
	if err := factory.Preflight(profile); err != nil {
		return nil, fmt.Errorf("bootstrapper profile %q: %w", profile.Name, err)
	}

	config := input.Base.Clone()
	config.WorkspaceWriteAllowed = false
	config, err = ThreadConfigForRoleContext(input.Start, config)
	if err != nil {
		return nil, fmt.Errorf("prepare bootstrapper session: %w", err)
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("validate bootstrapper session: %w", err)
	}
	runtime, err := factory.Create(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("start bootstrapper profile %q: %w", profile.Name, err)
	}
	if runtime == nil {
		return nil, errors.New("start bootstrapper: runtime factory returned nil")
	}
	thread, err := runtime.StartThread(config)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start bootstrapper session: %w", err), runtime.Close())
	}
	return &BootstrapperSession{Profile: profile, runtime: runtime, thread: thread}, nil
}

func bootstrapperProfile(ctx context.Context, input BootstrapperInput) (implementationconfig.RuntimeProfile, error) {
	_, configured, err := input.Configuration.BootstrapProfile()
	if err != nil {
		return implementationconfig.RuntimeProfile{}, err
	}
	if configured {
		profile, err := input.Configuration.ResolveRoleProfile(implementationconfig.RoleBootstrapper)
		if err != nil {
			return implementationconfig.RuntimeProfile{}, err
		}
		return profile, nil
	}
	if input.SelectProfile == nil {
		return implementationconfig.RuntimeProfile{}, errors.New("bootstrapper profile is not configured; select provider, model, and reasoning")
	}
	profile, err := input.SelectProfile(ctx)
	if err != nil {
		return implementationconfig.RuntimeProfile{}, fmt.Errorf("select bootstrapper profile: %w", err)
	}
	profile.Name = "bootstrapper-selected"
	if strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "" {
		return implementationconfig.RuntimeProfile{}, errors.New("bootstrapper selection requires provider and model")
	}
	return profile, nil
}

// RunTurn is intentionally a narrow continuation surface. Task 13.2 supplies
// the controller-built project context and routes Explorer; this method does
// not permit the setup layer to invent either of them.
func (session *BootstrapperSession) RunTurn(message string) ([]byte, error) {
	if session == nil || session.runtime == nil {
		return nil, errors.New("bootstrapper session is closed")
	}
	return session.runtime.RunTurn(session.thread, message)
}

// Close releases the dedicated bootstrapper thread and provider runtime.
func (session *BootstrapperSession) Close() error {
	if session == nil || session.runtime == nil {
		return nil
	}
	err := errors.Join(session.runtime.CloseThread(session.thread), session.runtime.Close())
	session.runtime = nil
	session.thread = nil
	return err
}
