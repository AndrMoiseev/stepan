package impl_loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// ExecutionBlockFromResponse converts the separately validated transport
// result into durable execution-state data. It intentionally accepts only
// execution_blocked, so a clarification can never be accidentally turned
// into a resumable environment/configuration pause.
func ExecutionBlockFromResponse(response AgentResponse) (implementationstate.ExecutionBlock, error) {
	if response.Kind != ResponseExecutionBlocked || response.BlockedAction == nil || response.Diagnostic == nil || response.RequiredUserAction == nil {
		return implementationstate.ExecutionBlock{}, fmt.Errorf("execution block requires an execution_blocked response")
	}
	return executionBlock(*response.BlockedAction, *response.Diagnostic, response.Attempts, *response.RequiredUserAction)
}

// ExecutionBlockForUserRemediation records controller-detected environment or
// configuration failures with the same durable shape as an agent escalation.
// It is not a recovery mechanism: it only supplies the information the user
// needs before explicitly resuming the run.
func ExecutionBlockForUserRemediation(action, diagnostic string, attempts []string, userAction string) (implementationstate.ExecutionBlock, error) {
	return executionBlock(action, diagnostic, attempts, userAction)
}

func executionBlock(action, diagnostic string, attempts []string, userAction string) (implementationstate.ExecutionBlock, error) {
	block := implementationstate.ExecutionBlock{
		BlockedAction: strings.TrimSpace(action), Diagnostic: strings.TrimSpace(diagnostic),
		Attempts: append([]string(nil), attempts...), RequiredUserAction: strings.TrimSpace(userAction),
	}
	if block.BlockedAction == "" || block.Diagnostic == "" || block.RequiredUserAction == "" || len(block.Attempts) == 0 {
		return implementationstate.ExecutionBlock{}, fmt.Errorf("execution block requires action, diagnostic, attempts, and user action")
	}
	for _, attempt := range block.Attempts {
		if strings.TrimSpace(attempt) == "" {
			return implementationstate.ExecutionBlock{}, fmt.Errorf("execution block attempts must be non-empty")
		}
	}
	return block, nil
}

// PersistExecutionBlock pauses a cloned state before publishing it, then
// replaces the caller model only after the journal-backed StateStore accepts
// the event. No task, configuration, required check selection, or scope is
// changed by this transition.
func PersistExecutionBlock(ctx context.Context, store *runstore.StateStore, current *implementationstate.Run, block implementationstate.ExecutionBlock) error {
	if store == nil || current == nil {
		return fmt.Errorf("persist execution block requires state store and run")
	}
	event, err := implementationstate.NewRunStateEvent(1, current)
	if err != nil {
		return fmt.Errorf("clone execution-blocked run: %w", err)
	}
	candidate := event.State
	if err := candidate.PauseExecutionBlocked(block); err != nil {
		return err
	}
	written, err := store.Record(context.WithoutCancel(ctx), candidate)
	if written.Sequence != 0 {
		*current = *candidate
	}
	if err != nil {
		return fmt.Errorf("persist execution-blocked pause: %w", err)
	}
	return nil
}
