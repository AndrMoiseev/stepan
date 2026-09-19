package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/claudeapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/nessyapp"
	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop"
	"github.com/AndrMoiseev/stepan/internal/flows/spec"
	"github.com/AndrMoiseev/stepan/internal/implementationruntime"
	"github.com/AndrMoiseev/stepan/internal/platformsupport"
	"github.com/AndrMoiseev/stepan/internal/runstore"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	config, err := parseAgentConfig(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Stepan:", err)
		fmt.Fprintln(os.Stderr, usageText)
		return 2
	}
	if err := preflight(runtime.GOOS, runtime.GOARCH, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "start Stepan:", err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "read working directory:", err)
		return 2
	}
	root, err := specflow.FindGitRoot(ctx, cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if config.bootstrap {
		if err := runBootstrapMode(ctx, root, config, newBootstrapConsole(os.Stdin, os.Stdout)); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "bootstrap:", err)
			return 2
		}
		return 0
	}
	startup, store, err := openImplementationStartup(ctx, root, os.UserHomeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "inspect implementation run:", err)
		return 2
	}
	if startup != nil {
		composition := implementationStartupCompositionForConfig(config, root, setting.NessyAuthToken)
		if err := runImplementationStartupInteractive(ctx, root, store, startup, newImplementationConsoleUI(), composition); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return 0
	}
	sources, err := setting.Load(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read settings:", err)
		return 2
	}
	if _, err := setting.Merge(sources); err != nil {
		fmt.Fprintln(os.Stderr, "resolve settings:", err)
		return 2
	}
	factory, err := configuredRuntimeFactory(config, root, setting.NessyAuthToken, defaultRuntimeStarters())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start Stepan:", err)
		return 2
	}
	session := specflow.NewSession(factory)
	defer session.Close()
	controller, registry, err := composePlanningFlow(root, session, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start planning flow:", err)
		return 2
	}
	defer registry.Close()
	stopInterrupt := context.AfterFunc(ctx, func() { _ = session.Interrupt() })
	defer stopInterrupt()
	err = specflow.RunPlanningInteractive(ctx, controller, specflow.NewUI())
	if errors.Is(err, context.Canceled) || errors.Is(err, specflow.ErrCanceled) {
		return 130
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

type bootstrapPrompter interface {
	SelectBootstrapProfile(context.Context) (impl_loop.BootstrapperProfileSelection, error)
	ReportBootstrapReady(impl_loop.BootstrapperProfileSelection)
	ConfirmBootstrapConfiguration(context.Context, []impl_loop.BootstrapConfigurationDiff) (bool, error)
}

type bootstrapConsole struct {
	input  *bufio.Reader
	output io.Writer
}

func newBootstrapConsole(input io.Reader, output io.Writer) *bootstrapConsole {
	return &bootstrapConsole{input: bufio.NewReader(input), output: output}
}

func (console *bootstrapConsole) SelectBootstrapProfile(ctx context.Context) (impl_loop.BootstrapperProfileSelection, error) {
	read := func(label string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprint(console.output, label)
		value, err := console.input.ReadString('\n')
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
	provider, err := read("Bootstrap provider (codex, claude, nessy): ")
	if err != nil {
		return impl_loop.BootstrapperProfileSelection{}, err
	}
	model, err := read("Bootstrap model: ")
	if err != nil {
		return impl_loop.BootstrapperProfileSelection{}, err
	}
	reasoning, err := read("Bootstrap reasoning (leave blank if unused): ")
	if err != nil {
		return impl_loop.BootstrapperProfileSelection{}, err
	}
	return impl_loop.BootstrapperProfileSelection{Provider: provider, Model: model, Reasoning: reasoning}, nil
}

func (console *bootstrapConsole) ReportBootstrapReady(profile impl_loop.BootstrapperProfileSelection) {
	fmt.Fprintf(console.output, "Bootstrap profile prepared (provider=%s, model=%s).\n", profile.Provider, profile.Model)
}

func (console *bootstrapConsole) ConfirmBootstrapConfiguration(ctx context.Context, diffs []impl_loop.BootstrapConfigurationDiff) (bool, error) {
	for _, diff := range diffs {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		fmt.Fprintf(console.output, "Configuration proposal for %s:\n%s", diff.Path, diff.Diff)
	}
	fmt.Fprint(console.output, "Save these configuration changes? [y/N]: ")
	line, err := console.input.ReadString('\n')
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func runBootstrapMode(ctx context.Context, root string, config agentConfig, prompt bootstrapPrompter) error {
	options := implementationruntime.FactoryOptions{Workspace: root, EnvelopeSchema: impl_loop.ImplementationEnvelopeSchema(), NessyAuthToken: setting.NessyAuthToken, NessyJSONContract: nessyapp.JSONContract}
	if config.kind == agentCodex {
		options.CodexExecutable = config.executable
	}
	if config.kind == agentClaude {
		options.ClaudeExecutable = config.executable
	}
	if prompt == nil {
		return errors.New("bootstrap profile prompt is required")
	}
	return impl_loop.RunBootstrapperMode(ctx, impl_loop.BootstrapperModeInput{Repository: root, Factories: implementationruntime.NewFactories(options), Base: agentruntime.ThreadConfig{Workspace: root}, SelectProfile: prompt.SelectBootstrapProfile, ReportReady: prompt.ReportBootstrapReady, ConfirmConfiguration: prompt.ConfirmBootstrapConfiguration})
}

// discoverImplementationStartup is intentionally before runtime/session
// composition. It uses only the JSONL status-read path, so an ordinary start
// can show a saved implementation run without creating agent sessions or
// triggering reconciliation checks. Those actions remain behind /resume.
func openImplementationStartup(ctx context.Context, workCopy string, userHome func() (string, error)) (*impl_loop.StartupRun, *runstore.Store, error) {
	home, err := userHome()
	if err != nil {
		return nil, nil, fmt.Errorf("find user home for implementation run store: %w", err)
	}
	store, err := runstore.OpenExisting(runstore.DefaultRoot(home))
	if errors.Is(err, runstore.ErrStoreNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	found, err := impl_loop.DiscoverStartupRun(ctx, store, workCopy)
	if err != nil || found == nil {
		return nil, nil, err
	}
	return found, store, nil
}

type implementationStartupComposition struct {
	Factories                   map[string]impl_loop.RuntimeFactory
	SessionBase                 agentruntime.ThreadConfig
	EnvelopeSchema              []byte
	ClassifySpecificationChange func(previous, current []byte) (impl_loop.SpecificationChange, error)
	// Continue is the application-owned durable continuation dispatcher. It
	// receives only a fresh SessionOwner created by Resume; it must rebuild
	// role contexts from the journal and never recover provider history.
	Continue func(context.Context, *impl_loop.InteractiveRun, *impl_loop.SessionOwner) error
}

func implementationStartupCompositionForConfig(config agentConfig, root string, nessyAuth func() (string, error)) implementationStartupComposition {
	envelope := impl_loop.ImplementationEnvelopeSchema()
	options := implementationruntime.FactoryOptions{
		Workspace: root, EnvelopeSchema: envelope, NessyAuthToken: nessyAuth, NessyJSONContract: nessyapp.JSONContract,
	}
	if config.kind == agentCodex {
		options.CodexExecutable = config.executable
	}
	if config.kind == agentClaude {
		options.ClaudeExecutable = config.executable
	}
	return implementationStartupComposition{
		Factories:                   implementationruntime.NewFactories(options),
		EnvelopeSchema:              append([]byte(nil), envelope...),
		SessionBase:                 agentruntime.ThreadConfig{Workspace: root},
		ClassifySpecificationChange: classifyImplementationSpecification,
		Continue: func(ctx context.Context, run *impl_loop.InteractiveRun, owner *impl_loop.SessionOwner) error {
			if run == nil || run.ResumeResult == nil {
				return errors.New("restart continuation has no successful resume result")
			}
			return impl_loop.DispatchRestartContinuation(ctx, impl_loop.RestartContinuationInput{
				Owner: owner, Journal: run.ResumeInput.Journal, StateStore: run.ResumeInput.StateStore, Run: run.Run, Repository: run.ResumeInput.Repository,
				Workspace: run.ResumeInput.Workspace, Runner: run.ResumeInput.Runner, UserControl: run.Control,
				Configuration: run.ResumeResult.Configuration, Checks: run.ResumeResult.Checks, Rules: run.ResumeResult.Rules,
				ProtectedPaths: run.ResumeInput.ProtectedPaths,
			})
		},
	}
}

// classifyImplementationSpecification compares the versioned documents in
// resumeSpecification evidence. JSON rendering and line-ending changes are
// compatible. Adding/removing a requirement document or changing proposal or
// specification content is a known new scope. A design-only semantic change
// is deliberately indeterminate: Resume turns the error into a durable pause
// instead of either silently accepting it or terminally closing the run.
func classifyImplementationSpecification(previous, current []byte) (impl_loop.SpecificationChange, error) {
	before, err := decodeImplementationSpecification(previous)
	if err != nil {
		return impl_loop.SpecificationChange{}, fmt.Errorf("decode saved resume specification: %w", err)
	}
	after, err := decodeImplementationSpecification(current)
	if err != nil {
		return impl_loop.SpecificationChange{}, fmt.Errorf("decode current resume specification: %w", err)
	}
	requiresNewScope := len(before) != len(after)
	indeterminate := make([]string, 0)
	paths := make([]string, 0, len(before))
	for path := range before {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		saved := before[path]
		latest, ok := after[path]
		if !ok {
			requiresNewScope = true
			continue
		}
		if normalizeImplementationMarkdown(saved.Content) == normalizeImplementationMarkdown(latest.Content) {
			continue
		}
		switch implementationSpecificationDocumentKind(path) {
		case "proposal", "specification":
			requiresNewScope = true
		case "design":
			indeterminate = append(indeterminate, path)
		default:
			indeterminate = append(indeterminate, path)
		}
	}
	if requiresNewScope {
		return impl_loop.SpecificationChange{RequiresNewScope: true}, nil
	}
	if len(indeterminate) != 0 {
		return impl_loop.SpecificationChange{}, fmt.Errorf("semantic changes to %s cannot be safely classified as compatible or new scope", strings.Join(indeterminate, ", "))
	}
	return impl_loop.SpecificationChange{}, nil
}

type implementationSpecificationDocument struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Content string `json:"Content"`
}

func decodeImplementationSpecification(data []byte) (map[string]implementationSpecificationDocument, error) {
	var documents []implementationSpecificationDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&documents); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("resume specification has trailing JSON values")
	}
	decoded := make(map[string]implementationSpecificationDocument, len(documents))
	for _, document := range documents {
		path := filepath.ToSlash(filepath.Clean(document.Path))
		if path != document.Path || path == "." || path == "" || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			return nil, fmt.Errorf("invalid document path %q", document.Path)
		}
		if implementationSpecificationDocumentKind(path) == "" {
			return nil, fmt.Errorf("unrecognized resume specification document %s", path)
		}
		digest := sha256.Sum256([]byte(document.Content))
		if document.Version != hex.EncodeToString(digest[:]) {
			return nil, fmt.Errorf("document %s version does not match its content", path)
		}
		if _, exists := decoded[path]; exists {
			return nil, fmt.Errorf("duplicate document path %s", path)
		}
		document.Path = path
		decoded[path] = document
	}
	if len(decoded) == 0 {
		return nil, errors.New("resume specification has no documents")
	}
	return decoded, nil
}

func normalizeImplementationMarkdown(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func implementationSpecificationDocumentKind(path string) string {
	switch {
	case strings.HasSuffix(path, "/proposal.md"):
		return "proposal"
	case strings.HasSuffix(path, "/design.md"):
		return "design"
	case strings.Contains(path, "/specs/") && strings.HasSuffix(path, ".md"):
		return "specification"
	default:
		return ""
	}
}

func runImplementationStartupInteractive(ctx context.Context, workCopy string, store *runstore.Store, startup *impl_loop.StartupRun, ui impl_loop.ImplementationInteractiveUI, composition implementationStartupComposition) error {
	if startup == nil || startup.Run == nil || store == nil {
		return errors.New("implementation startup requires discovered run and store")
	}
	var owned *impl_loop.ResumedRun
	var ownedInteractive *impl_loop.InteractiveRun
	var owner *impl_loop.SessionOwner
	defer func() {
		if owner != nil {
			_ = owner.Close()
		}
		if owned != nil {
			_ = owned.Close()
		}
	}()
	return runDiscoveredImplementationInteractive(ctx, startup, ui, func(callCtx context.Context) (*impl_loop.InteractiveRun, error) {
		if owned != nil {
			return ownedInteractive, nil
		}
		resumed, control, err := impl_loop.RecoverOwnRun(callCtx, store, workCopy)
		if err != nil {
			return nil, err
		}
		owned = resumed
		ownedInteractive = &impl_loop.InteractiveRun{Run: resumed.Run, Control: control, ResumeInput: impl_loop.ResumeInput{
			Run: resumed.Run, StateStore: resumed.StateStore, Journal: resumed.Journal, Repository: workCopy,
			Workspace: impl_loop.GitWorkspaceControl{}, Runner: impl_loop.DirectCheckRunner{},
			Factories: composition.Factories, SessionOwner: &owner, SessionBase: composition.SessionBase,
			ClassifySpecificationChange: composition.ClassifySpecificationChange,
		}}
		return ownedInteractive, nil
	}, func(continueCtx context.Context, run *impl_loop.InteractiveRun) error {
		if composition.Continue == nil {
			return nil
		}
		if owner == nil {
			return errors.New("implementation resume did not create a fresh session owner")
		}
		return composition.Continue(continueCtx, run, owner)
	})
}

// runDiscoveredImplementationInteractive is the startup composition seam. It
// does not recover a run itself: the supplied callback is invoked only after
// a lifecycle command, which makes the no-auto-work property testable without
// real providers, checks, or Git processes.
func runDiscoveredImplementationInteractive(ctx context.Context, startup *impl_loop.StartupRun, ui impl_loop.ImplementationInteractiveUI, recover func(context.Context) (*impl_loop.InteractiveRun, error), continueRun func(context.Context, *impl_loop.InteractiveRun) error) error {
	if startup == nil || startup.Run == nil {
		return errors.New("implementation startup requires discovered run")
	}
	current := &impl_loop.InteractiveRun{Run: startup.Run, RecoveryRequired: startup.RecoveryRequired, Recover: recover}
	ui.Report(impl_loop.FormatStartupSummary(startup.Summary))
	controller := impl_loop.ImplementationInteractiveController{
		Current:  func(context.Context) (*impl_loop.InteractiveRun, error) { return current, nil },
		Continue: continueRun,
	}
	if recover != nil {
		current.Recover = func(callCtx context.Context) (*impl_loop.InteractiveRun, error) {
			next, err := recover(callCtx)
			if err == nil && next != nil {
				current = next
			}
			return current, err
		}
	}
	return impl_loop.RunImplementationInteractive(ctx, controller, ui)
}

type consoleLine struct {
	value string
	err   error
}

// implementationConsoleUI owns exactly one blocking stdin reader. Prompt
// callers may come and go as the interactive driver redraws after a canceled
// operation, but the pump never overlaps ReadString calls on the console.
// This lets Ctrl+C cancel a prompt immediately without trying to cancel an OS
// console read, which is not portable across Windows and macOS.
type implementationConsoleUI struct {
	input  *bufio.Reader
	output io.Writer
	errors io.Writer
	once   sync.Once
	lines  chan consoleLine
}

func newImplementationConsoleUI() *implementationConsoleUI {
	return newImplementationConsoleUIWithIO(os.Stdin, os.Stdout, os.Stderr)
}
func newImplementationConsoleUIWithIO(input io.Reader, output, errorOutput io.Writer) *implementationConsoleUI {
	return &implementationConsoleUI{input: bufio.NewReader(input), output: output, errors: errorOutput, lines: make(chan consoleLine, 1)}
}
func (u *implementationConsoleUI) startPump() {
	u.once.Do(func() {
		go func() {
			for {
				value, err := u.input.ReadString('\n')
				u.lines <- consoleLine{value: value, err: err}
				if err != nil {
					return
				}
			}
		}()
	})
}
func (u *implementationConsoleUI) Prompt(ctx context.Context, menu impl_loop.CommandMenu) (string, error) {
	for _, command := range menu.Commands {
		fmt.Fprintf(u.output, "%s — %s\n", command.Command, command.Description)
	}
	fmt.Fprint(u.output, "You > ")
	u.startPump()
	select {
	case <-ctx.Done():
		return "", impl_loop.ErrInteractiveInputCanceled
	case line := <-u.lines:
		if line.err != nil {
			return "", impl_loop.ErrInteractiveInputCanceled
		}
		return line.value, nil
	}
}
func (u *implementationConsoleUI) Report(message string) {
	if message != "" {
		fmt.Fprintln(u.output, message)
	}
}
func (u *implementationConsoleUI) ReportError(err error) {
	fmt.Fprintln(u.errors, "implementation:", err)
}

func configuredRuntimeFactory(config agentConfig, root string, load func() (string, error), starters runtimeStarters) (func(context.Context) (agentruntime.Runtime, error), error) {
	token := ""
	if config.kind == agentNessy {
		var err error
		token, err = load()
		if err != nil {
			return nil, err
		}
	}
	return runtimeFactoryWithStarters(config, root, starters, token), nil
}

func composePlanningFlow(root string, session *specflow.Session, config agentConfig) (*specflow.ApplicationController, *specflow.SessionRegistry, error) {
	repository, err := specflow.NewFSFeatureRepository(root)
	if err != nil {
		return nil, nil, err
	}
	registry, err := specflow.NewSessionRegistry(session, repository)
	if err != nil {
		return nil, nil, err
	}
	catalog := specflow.NewEmbeddedPromptCatalog()
	author, err := specflow.NewStageEngine(root, registry, repository, catalog)
	if err != nil {
		return nil, nil, err
	}
	reviewer, err := specflow.NewReviewEngine(root, registry, repository, catalog)
	if err != nil {
		return nil, nil, err
	}
	flow, err := specflow.NewFeatureController(repository, author, reviewer)
	if err != nil {
		return nil, nil, err
	}
	manager, err := specflow.NewResumeManager(repository, registry, flow)
	if err != nil {
		return nil, nil, err
	}
	application, err := specflow.NewApplicationController(root, session, manager, flow, runtimeIdentity(config))
	if err != nil {
		return nil, nil, err
	}
	return application, registry, nil
}

func runtimeIdentity(config agentConfig) specflow.RuntimeIdentity {
	return specflow.RuntimeIdentity{Provider: string(config.kind), Model: "default"}
}

func runtimeFactory(config agentConfig, root string, token string) func(context.Context) (agentruntime.Runtime, error) {
	return runtimeFactoryWithStarters(config, root, defaultRuntimeStarters(), token)
}

type runtimeStarters struct {
	codex  func(string, string) (agentruntime.Runtime, error)
	claude func(context.Context, claudeapp.Config) (agentruntime.Runtime, error)
	nessy  func(nessyapp.Config) (agentruntime.Runtime, error)
}

func defaultRuntimeStarters() runtimeStarters {
	return runtimeStarters{
		codex: func(executable, workspace string) (agentruntime.Runtime, error) {
			return codexapp.StartRuntime(executable, workspace)
		},
		claude: func(ctx context.Context, config claudeapp.Config) (agentruntime.Runtime, error) {
			return claudeapp.StartRuntime(ctx, config)
		},
		nessy: func(config nessyapp.Config) (agentruntime.Runtime, error) {
			return nessyapp.StartRuntime(config)
		},
	}
}

func runtimeFactoryWithStarters(config agentConfig, root string, starters runtimeStarters, token string) func(context.Context) (agentruntime.Runtime, error) {
	switch config.kind {
	case agentClaude:
		return func(ctx context.Context) (agentruntime.Runtime, error) {
			return starters.claude(ctx, claudeapp.Config{
				Executable:     config.executable,
				Workspace:      root,
				EnvelopeSchema: specflow.FlowEnvelopeSchema(),
			})
		}
	case agentNessy:
		return func(context.Context) (agentruntime.Runtime, error) {
			return starters.nessy(nessyapp.Config{
				AuthToken:      token,
				Workspace:      root,
				JSONContract:   nessyapp.JSONContract,
				EnvelopeSchema: specflow.FlowEnvelopeSchema(),
			})
		}
	case agentCodex:
		return func(context.Context) (agentruntime.Runtime, error) {
			return starters.codex(config.executable, root)
		}
	default:
		return func(context.Context) (agentruntime.Runtime, error) {
			return nil, fmt.Errorf("unknown agent %q: %w", config.kind, agentruntime.ErrRuntimeConfiguration)
		}
	}
}

func preflight(goos, goarch string, stdin, stdout *os.File) error {
	if err := platformsupport.Validate(goos, goarch); err != nil {
		return err
	}
	if !isConsole(stdin) {
		if goos == "windows" {
			return errors.New("stdin must be a Windows console terminal")
		}
		return errors.New("stdin must be a terminal")
	}
	if !isConsole(stdout) {
		if goos == "windows" {
			return errors.New("stdout must be a Windows console terminal")
		}
		return errors.New("stdout must be a terminal")
	}
	return nil
}
