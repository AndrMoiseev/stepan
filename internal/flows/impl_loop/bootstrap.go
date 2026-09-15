package impl_loop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
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

// RunBootstrapperMode starts exactly one independent bootstrapper session and
// gives it a controller-built, read-only project request. It does not run
// configured checks: a bootstrap response can only propose configuration,
// request Explorer, clarify, or report a block.
func RunBootstrapperMode(ctx context.Context, input BootstrapperModeInput) error {
	sources, err := implementationconfig.Load(input.Repository)
	if err != nil {
		return err
	}
	project, err := BuildBootstrapperProjectContext(input.Repository, sources)
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
	_, err = NewBootstrapperController(session, configuration, project).Run(ctx)
	return err
}

// BootstrapperSession is the one fresh, process-local bootstrapper session.
// Its caller owns the lifecycle and must close it when the distinct bootstrap
// mode ends. Later bootstrap tasks send the controller-built request through
// RunTurn; the setup path here never starts a check or an implementation run.
type BootstrapperSession struct {
	Profile       implementationconfig.RuntimeProfile
	runtime       agentruntime.Runtime
	thread        agentruntime.Thread
	configuration implementationconfig.Configuration
	factories     map[string]RuntimeFactory
	base          agentruntime.ThreadConfig
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
	return &BootstrapperSession{Profile: profile, runtime: runtime, thread: thread, configuration: input.Configuration, factories: input.Factories, base: input.Base.Clone()}, nil
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

func (session *BootstrapperSession) startExplorer(ctx context.Context, start RoleStartContext) (*BootstrapperSession, error) {
	if session == nil || session.runtime == nil {
		return nil, errors.New("bootstrapper session is closed")
	}
	profile, err := session.configuration.ResolveRoleProfile(implementationconfig.RoleExplorer)
	if err != nil {
		return nil, fmt.Errorf("resolve Explorer profile: %w", err)
	}
	factory, ok := session.factories[profile.Provider]
	if !ok || factory == nil {
		return nil, fmt.Errorf("Explorer profile %q uses unsupported provider %q", profile.Name, profile.Provider)
	}
	if err := factory.Preflight(profile); err != nil {
		return nil, fmt.Errorf("Explorer profile %q: %w", profile.Name, err)
	}
	config := session.base.Clone()
	config.WorkspaceWriteAllowed = false
	config, err = ThreadConfigForRoleContext(start, config)
	if err != nil {
		return nil, fmt.Errorf("prepare Explorer session: %w", err)
	}
	runtime, err := factory.Create(ctx, profile)
	if err != nil {
		return nil, fmt.Errorf("start Explorer profile %q: %w", profile.Name, err)
	}
	thread, err := runtime.StartThread(config)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("start Explorer session: %w", err), runtime.Close())
	}
	return &BootstrapperSession{Profile: profile, runtime: runtime, thread: thread}, nil
}

// BootstrapperController owns the only allowable state transitions in the
// short-lived bootstrap conversation. Its in-memory counter is enough because
// bootstrap has no resumable run; ending the mode ends the source episode.
type BootstrapperController struct {
	session    *BootstrapperSession
	context    BootstrapProjectContext
	limit      int
	researches int
	initErr    error
}

// NewBootstrapperController derives the configured Explorer cap. It accepts
// no runner or command dependency by design, which makes autonomous checking
// impossible at this boundary.
func NewBootstrapperController(session *BootstrapperSession, configuration implementationconfig.Configuration, project BootstrapProjectContext) *BootstrapperController {
	project = sanitizedBootstrapProjectContext(project)
	limits, err := configuration.ResolveLimits()
	return &BootstrapperController{session: session, context: project, limit: limits.ExplorationLimit, initErr: err}
}

func sanitizedBootstrapProjectContext(project BootstrapProjectContext) BootstrapProjectContext {
	project.UserSettings = sanitizeBootstrapJSON([]byte(project.UserSettings))
	project.ProjectSettings = sanitizeBootstrapJSON([]byte(project.ProjectSettings))
	for index := range project.CI {
		project.CI[index].Content = sanitizeBootstrapText(project.CI[index].Content)
	}
	for index := range project.Scripts {
		project.Scripts[index].Content = sanitizeBootstrapText(project.Scripts[index].Content)
	}
	return project
}

// Run sends the initial context and routes each permitted Explorer request to
// a fresh read-only session before continuing the original bootstrapper.
func (controller *BootstrapperController) Run(ctx context.Context) (AgentResponse, error) {
	if controller != nil && controller.initErr != nil {
		return AgentResponse{}, fmt.Errorf("bootstrap controller limits: %w", controller.initErr)
	}
	if controller == nil || controller.session == nil || controller.limit <= 0 {
		return AgentResponse{}, errors.New("bootstrap controller requires a session and positive exploration limit")
	}
	message, err := BuildBootstrapperRequest(controller.context)
	if err != nil {
		return AgentResponse{}, err
	}
	return controller.runBootstrapper(ctx, message)
}

func (controller *BootstrapperController) runBootstrapper(ctx context.Context, message string) (AgentResponse, error) {
	callID := fmt.Sprintf("bootstrap-%d", controller.researches+1)
	expectation := bootstrapExpectation(callID, controller.context)
	raw, err := controller.session.RunTurn(message)
	if err != nil {
		return AgentResponse{}, fmt.Errorf("bootstrapper turn: %w", err)
	}
	response, err := BindAgentResponse(expectation, raw)
	if err != nil {
		return AgentResponse{}, fmt.Errorf("bootstrapper response: %w", err)
	}
	if response.Kind != ResponseExplorationRequested {
		return response, nil
	}
	if controller.researches >= controller.limit {
		return AgentResponse{}, fmt.Errorf("bootstrap Explorer limit of %d researches reached", controller.limit)
	}
	controller.researches++
	return controller.routeExplorer(ctx, expectation, response)
}

func (controller *BootstrapperController) routeExplorer(ctx context.Context, source ResponseExpectation, request AgentResponse) (AgentResponse, error) {
	if err := validateExplorerRequest(request); err != nil {
		return AgentResponse{}, err
	}
	start, err := BuildExplorerStartContext(ExplorerStartInput{Question: *request.Question, Context: *request.Context, Boundaries: *request.Boundaries, KnownFacts: request.KnownFacts})
	if err != nil {
		return AgentResponse{}, err
	}
	explorer, err := controller.session.startExplorer(ctx, start)
	if err != nil {
		return AgentResponse{}, err
	}
	defer explorer.Close()
	raw, err := explorer.RunTurn("Research the controller-supplied question and return the configured structured response. Do not run commands.")
	if err != nil {
		return AgentResponse{}, fmt.Errorf("Explorer turn: %w", err)
	}
	expectation := source
	expectation.Role, expectation.State, expectation.ExplorerSource = ResponseRoleExplorer, ResponseStateExploring, ExplorerSourceBootstrapper
	expectation.Binding.CallID = fmt.Sprintf("bootstrap-explorer-%d", controller.researches)
	response, err := BindAgentResponse(expectation, raw)
	if err != nil {
		return AgentResponse{}, fmt.Errorf("Explorer response: %w", err)
	}
	if response.Kind != ResponseExplorationResult {
		return response, nil
	}
	return controller.runBootstrapper(ctx, explorerContinuation(response))
}

func bootstrapExpectation(callID string, project BootstrapProjectContext) ResponseExpectation {
	return ResponseExpectation{Role: ResponseRoleBootstrapper, State: ResponseStateBootstrapping, Scope: ResponseScopeBootstrap, Binding: ResponseBinding{
		CallID: callID, UserConfiguration: bootstrapContextEvidence("bootstrap-user-settings", project.UserSettings), ProjectConfiguration: bootstrapContextEvidence("bootstrap-project-settings", project.ProjectSettings),
	}}
}

func bootstrapContextEvidence(id, value string) implementationstate.EvidenceRef {
	digest := sha256.Sum256([]byte(value))
	return implementationstate.EvidenceRef{ID: implementationstate.EvidenceID(id), Digest: hex.EncodeToString(digest[:])}
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
