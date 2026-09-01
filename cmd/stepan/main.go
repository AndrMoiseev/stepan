package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/claudeapp"
	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
	"github.com/AndrMoiseev/stepan/internal/platformsupport"
	"github.com/AndrMoiseev/stepan/internal/specflow"
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

	session := specflow.NewSession(runtimeFactory(config, root))
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
	application, err := specflow.NewApplicationController(root, session, manager, flow, specflow.RuntimeIdentity{Provider: string(config.kind), Model: "default"})
	if err != nil {
		return nil, nil, err
	}
	return application, registry, nil
}

func runtimeFactory(config agentConfig, root string) func(context.Context) (agentruntime.Runtime, error) {
	switch config.kind {
	case agentClaude:
		return func(ctx context.Context) (agentruntime.Runtime, error) {
			return claudeapp.StartRuntime(ctx, claudeapp.Config{
				Executable:     config.executable,
				Workspace:      root,
				EnvelopeSchema: specflow.FlowEnvelopeSchema(),
			})
		}
	default:
		return func(context.Context) (agentruntime.Runtime, error) {
			return codexapp.StartRuntime(config.executable, root)
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
