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

// TurnOptions controls one sequential agent turn.
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

// Runtime owns one provider connection. Calls to RunTurn are sequential.
type Runtime interface {
	StartThread() (Thread, error)
	RunTurn(Thread, string, TurnOptions) (json.RawMessage, error)
	Interrupt() error
	Close() error
}
