// Package agentruntime defines the small provider-neutral boundary used by
// specflow. It intentionally contains no CLI protocol, UI, Git, or provider
// configuration types.
package agentruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

var (
	ErrRuntimeClosed   = errors.New("agent runtime closed")
	ErrTurnInterrupted = errors.New("turn interrupted by operator")
	ErrRuntimeExited   = errors.New("agent runtime exited unexpectedly")
	ErrTurnInProgress  = errors.New("a turn is already in progress")
)

// Thread is an opaque provider-owned logical conversation handle.
type Thread any

// TurnOptions is retained for adapter protocol tests. Intent-flow callers do
// not use it: the schema and filesystem policy belong to ThreadConfig.
type TurnOptions struct {
	OutputSchema json.RawMessage
	Policy       TurnPolicy
}

// Clone prevents a caller from mutating the schema bytes retained by a
// provider while a turn is in flight.
func (options TurnOptions) Clone() TurnOptions {
	options.OutputSchema = append(json.RawMessage(nil), options.OutputSchema...)
	return options
}

// TurnPolicy permits either no writes or one absolute writable root. The
// provider remains responsible for checking that the root is valid for its
// workspace before the turn begins.
type TurnPolicy struct{ writableRoot string }

func ReadOnlyTurnPolicy() TurnPolicy { return TurnPolicy{} }

func SingleWriteRootTurnPolicy(root string) (TurnPolicy, error) {
	if !filepath.IsAbs(root) {
		return TurnPolicy{}, fmt.Errorf("writable root %q must be absolute", root)
	}
	return TurnPolicy{writableRoot: filepath.Clean(root)}, nil
}

func (policy TurnPolicy) WritableRoot() (string, bool) {
	return policy.writableRoot, policy.writableRoot != ""
}

// ThreadConfig is immutable security and conversation setup for a logical
// thread. Workspace is readable, ArtifactRoot (when present) is the exact
// single writable directory, and everything else is denied by adapters.
type ThreadConfig struct {
	BootstrapInstructions string
	OutputSchema          json.RawMessage
	Workspace             string
	ArtifactRoot          string
}

func (config ThreadConfig) Clone() ThreadConfig {
	config.OutputSchema = append(json.RawMessage(nil), config.OutputSchema...)
	return config
}

func (config ThreadConfig) Validate() error {
	if config.Workspace == "" || !filepath.IsAbs(config.Workspace) {
		return fmt.Errorf("workspace must be an absolute path")
	}
	if config.ArtifactRoot != "" && !filepath.IsAbs(config.ArtifactRoot) {
		return fmt.Errorf("artifact root must be an absolute path")
	}
	if len(config.OutputSchema) == 0 {
		return fmt.Errorf("output schema is required")
	}
	return nil
}

// Runtime owns one provider connection. Calls to RunTurn are sequential.
type Runtime interface {
	// The optional form keeps adapter protocol tests source-compatible. New
	// callers must supply exactly one ThreadConfig; a thread never accepts a
	// policy change after creation.
	StartThread(...ThreadConfig) (Thread, error)
	RunTurn(Thread, string, ...TurnOptions) (json.RawMessage, error)
	Interrupt() error
	Close() error
}
