// Package nessyapp implements the contained Nessy ACP adapter.
package nessyapp

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/platformsupport"
)

const (
	defaultExecutable  = "nessy"
	maxDiagnosticBytes = 64 << 10
)

var (
	// ErrConfiguration classifies executable, workspace, contract, and root
	// validation failures that happen before a child is started.
	ErrConfiguration = agentruntime.ErrRuntimeConfiguration
	// ErrStartup classifies failures to start the selected executable.
	ErrStartup = agentruntime.ErrRuntimeStartup
	// ErrContainment classifies failures to create, prepare, or assign the
	// process-tree supervisor.
	ErrContainment = agentruntime.ErrRuntimeContainment
)

// Config is immutable input shared by the contained processes of one Nessy
// runtime. AuthToken is a validated snapshot from user settings. The executable
// is always nessy from PATH. JSONContract is the process-level instruction that requires
// one JSON object per completed turn.
// EnvelopeSchema declares the common structured contract supplied by the
// composition root; each thread still validates its narrower OutputSchema.
type Config struct {
	AuthToken      string
	Workspace      string
	JSONContract   string
	EnvelopeSchema json.RawMessage
	Model          string
	Reasoning      string
}

// ValidateRuntimeConfig performs the non-interactive portion of Nessy's
// runtime preflight. Nessy exposes model selection through --model, but its
// ACP/session contract has no reasoning option, so an explicit reasoning
// value is rejected rather than silently ignored.
func ValidateRuntimeConfig(config Config) error {
	if err := validateAuthToken(config.AuthToken); err != nil {
		return err
	}
	if err := platformsupport.Validate(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	if strings.TrimSpace(config.JSONContract) == "" {
		return errors.New("nessy JSON contract is required")
	}
	if config.Reasoning != "" {
		return errors.New("nessy does not support configured reasoning")
	}
	if _, err := ResolveExecutable(); err != nil {
		return err
	}
	if _, err := canonicalGitRoot(config.Workspace); err != nil {
		return err
	}
	return nil
}

type processJob interface {
	Prepare(*exec.Cmd) error
	Assign(*os.Process) error
	Close() error
}

type processDependencies struct {
	command  func(string, ...string) *exec.Cmd
	newJob   func() (processJob, error)
	makeRoot func() (string, error)
	remove   func(string) error
	environ  func() []string
}
