package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"

	"github.com/AndrMoiseev/stepan/internal/platformsupport"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx))
}

func run(ctx context.Context) int {
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

	session := specflow.NewSession("codex", root)
	defer session.Close()
	controller := specflow.NewController(root, session)
	err = specflow.RunInteractive(ctx, controller, specflow.NewUI(controller), session.Interrupt)
	if errors.Is(err, context.Canceled) || errors.Is(err, specflow.ErrCanceled) {
		return 130
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
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
