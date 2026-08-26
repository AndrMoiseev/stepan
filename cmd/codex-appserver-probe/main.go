package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/AndrMoiseev/stepan/internal/agentruntime/codexapp"
)

func main() {
	os.Exit(run())
}

func run() int {
	var config codexapp.ProbeConfig
	var schemaPath, policyPath string
	flag.StringVar(&config.Executable, "executable", "codex", "absolute Codex path or PATH name")
	flag.StringVar(&config.Workspace, "workspace", "", "absolute role workspace")
	flag.StringVar(&config.ArtifactDir, "artifacts", "", "new absolute artifact directory")
	flag.StringVar(&config.Nonce, "nonce", "", "expected structured-output nonce")
	flag.StringVar(&config.ThreadID, "thread", "", "explicit thread ID to resume")
	flag.StringVar(&schemaPath, "schema", "", "final-output JSON Schema; built-in probe schema when omitted")
	flag.StringVar(&policyPath, "policy", "", "access policy JSON; defaults to workspace read-only")
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
	if policyPath != "" {
		file, openErr := os.Open(policyPath)
		if openErr != nil {
			fmt.Fprintln(os.Stderr, "read policy:", openErr)
			return 2
		}
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&config.AccessPolicy)
		if err == nil {
			var trailing any
			if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
				err = errors.New("policy must contain exactly one JSON object")
			}
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			fmt.Fprintln(os.Stderr, "read policy:", errors.Join(err, closeErr))
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
