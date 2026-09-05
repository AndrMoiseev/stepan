// Package qwenapp implements the contained Qwen Code ACP adapter.
package qwenapp

import (
	"os"
	"os/exec"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

const (
	defaultExecutable  = "qwen"
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

// Config is immutable input shared by the contained processes of one Qwen
// runtime. An empty Executable selects the qwen PATH name. JSONContract is the
// process-level instruction that requires one JSON object per completed turn.
type Config struct {
	Executable   string
	Workspace    string
	JSONContract string
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
