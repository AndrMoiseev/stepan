package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AndrMoiseev/stepan/internal/codexexec"
)

func main() {
	os.Exit(run())
}

func run() int {
	var cfg codexexec.Config
	flag.StringVar(&cfg.Executable, "executable", "codex", "absolute Codex path or executable name")
	flag.StringVar(&cfg.Workspace, "workspace", "", "absolute workspace path")
	flag.StringVar(&cfg.SchemaPath, "schema", "", "absolute final-output JSON Schema path")
	flag.StringVar(&cfg.ArtifactDir, "artifacts", "", "absolute run artifact directory")
	flag.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "process timeout")
	flag.StringVar(&cfg.SessionID, "session", "", "explicit session ID to resume")
	flag.StringVar(&cfg.Nonce, "nonce", "", "expected nonce in the structured final output")
	flag.Func("sandbox", "read-only or workspace-write", func(value string) error {
		cfg.Sandbox = codexexec.Sandbox(value)
		return nil
	})
	flag.Func("config", "isolated or inherited", func(value string) error {
		cfg.ConfigMode = codexexec.ConfigMode(value)
		return nil
	})
	flag.Parse()
	if cfg.Sandbox == "" {
		cfg.Sandbox = codexexec.ReadOnly
	}
	if cfg.ConfigMode == "" {
		cfg.ConfigMode = codexexec.Isolated
	}

	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read prompt:", err)
		return 2
	}
	cfg.Prompt = prompt
	cfg, err = cfg.Validate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid input:", err)
		return 2
	}

	cfg.CodexVersion, err = codexexec.Version(context.Background(), cfg.Executable)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read Codex version:", err)
		return 2
	}
	result, err := codexexec.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run probe:", err)
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "write result:", err)
		return 2
	}
	if result.TerminationReason == codexexec.SpawnFailed {
		return 2
	}
	if result.Outcome != codexexec.Pass {
		return 1
	}
	return 0
}
