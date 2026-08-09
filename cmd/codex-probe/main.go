package main

import (
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

	plan := struct {
		Executable string   `json:"executable"`
		Args       []string `json:"args"`
	}{cfg.Executable, cfg.Args()}
	if err := json.NewEncoder(os.Stdout).Encode(plan); err != nil {
		fmt.Fprintln(os.Stderr, "write plan:", err)
		return 2
	}
	return 0
}
