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

// DefaultRetryLimit is the shared upper bound for retry-shaped agent loops.
// A limit of three means three responses in total, not three retries after the
// initial response.
const DefaultRetryLimit = 3

var (
	ErrRuntimeClosed   = errors.New("agent runtime closed")
	ErrTurnInterrupted = errors.New("turn interrupted by operator")
	ErrRuntimeExited   = errors.New("agent runtime exited unexpectedly")
	ErrTurnInProgress  = errors.New("a turn is already in progress")
	// ErrThreadFailed marks a failure whose ownership is known to be limited to
	// one logical thread. Session owners may discard that handle without
	// restarting a runtime that still owns healthy sibling threads.
	ErrThreadFailed = errors.New("agent thread failed")
	// The remaining sentinels classify adapter failures without exposing a
	// provider protocol through Runtime.
	ErrRuntimeConfiguration = errors.New("agent runtime configuration is invalid")
	ErrRuntimeStartup       = errors.New("agent runtime failed to start")
	ErrRuntimeContainment   = errors.New("agent runtime containment failed")
	ErrRuntimeIncompatible  = errors.New("agent runtime is incompatible")
	ErrRuntimeProtocol      = errors.New("agent runtime protocol violation")
	ErrStructuredResponse   = errors.New("agent structured response is invalid")
	ErrRuntimeCleanup       = errors.New("agent thread cleanup failed")
	// ErrPermissionDenied reports that an adapter rejected a filesystem
	// operation under the immutable turn policy. The error is deliberately
	// provider-neutral and contains no requested path or file content.
	ErrPermissionDenied = errors.New("agent filesystem permission denied")
)

type threadFailure struct{ cause error }

func (failure threadFailure) Error() string { return failure.cause.Error() }
func (failure threadFailure) Unwrap() []error {
	return []error{ErrThreadFailed, failure.cause}
}

// MarkThreadFailed preserves the classified cause while declaring that it is
// safe for a caller to keep using sibling threads in the same runtime.
func MarkThreadFailed(cause error) error {
	if cause == nil || errors.Is(cause, ErrThreadFailed) {
		return cause
	}
	return threadFailure{cause: cause}
}

// Thread is an opaque provider-owned logical conversation handle.
type Thread any

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

// Runtime owns a provider lifecycle. An adapter may use one shared connection
// or independent per-thread processes. A ThreadConfig is accepted only when
// the thread is created; RunTurn deliberately has no policy or schema inputs.
// Calls to RunTurn are sequential.
type Runtime interface {
	StartThread(ThreadConfig) (Thread, error)
	RunTurn(Thread, string) (json.RawMessage, error)
	CloseThread(Thread) error
	Interrupt() error
	Close() error
}
