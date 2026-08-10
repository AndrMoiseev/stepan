package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/AndrMoiseev/stepan/internal/codexapp"
)

func main() {
	os.Exit(run())
}

func run() int {
	var config codexapp.ProbeConfig
	var schemaPath string
	flag.StringVar(&config.Executable, "executable", "codex", "absolute Codex path or PATH name")
	flag.StringVar(&config.Workspace, "workspace", "", "absolute role workspace")
	flag.StringVar(&config.ArtifactDir, "artifacts", "", "new absolute artifact directory")
	flag.StringVar(&config.Nonce, "nonce", "", "expected structured-output nonce")
	flag.StringVar(&config.ThreadID, "thread", "", "explicit thread ID to resume")
	flag.StringVar(&schemaPath, "schema", "", "final-output JSON Schema; built-in probe schema when omitted")
	flag.Parse()
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read prompt:", err)
		return 2
	}
	config.Prompt = string(prompt)
	if schemaPath != "" {
		config.OutputSchema, err = os.ReadFile(schemaPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read schema:", err)
			return 2
		}
	}
	result, err := codexapp.RunProbe(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "run probe:", err)
		return 2
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "write result:", err)
		return 2
	}
	if result.Outcome != codexapp.Pass {
		return 1
	}
	return 0
}
