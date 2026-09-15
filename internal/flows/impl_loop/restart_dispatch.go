package impl_loop

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// RestartContinuationInput contains the durable facts needed to select the
// first recovered role after a successful /resume. It deliberately contains
// no provider session or history identifier: SessionOwner.Restore always
// creates a new provider-neutral session from a complete durable context.
type RestartContinuationInput struct {
	Owner      *SessionOwner
	Journal    *runstore.Run
	Run        *implementationstate.Run
	Repository string
}

// DispatchRestartContinuation restores the next durable role boundary. An
// open assignment resumes at its briefer boundary; otherwise the orchestrator
// receives the complete package and current machine state to choose the next
// controller action. More specialized interrupted-role routing remains owned
// by its existing controller primitives rather than replaying provider turns.
func DispatchRestartContinuation(ctx context.Context, input RestartContinuationInput) error {
	if input.Owner == nil || input.Journal == nil || input.Run == nil || input.Repository == "" {
		return fmt.Errorf("restart continuation requires owner, journal, run, and repository")
	}
	if assignment := activeAssignment(input.Run); assignment != "" {
		start, err := BuildBrieferStartContext(input.Journal, input.Run, assignment)
		if err != nil {
			return fmt.Errorf("build recovered briefer context: %w", err)
		}
		_, err = input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleBriefer, AssignmentID: assignment, Start: start.roleStartContext()})
		return err
	}
	pkg, err := openspec.Load(input.Repository, input.Run.Identity.Change)
	if err != nil {
		return fmt.Errorf("load OpenSpec package for recovered orchestrator: %w", err)
	}
	tasks, err := json.Marshal(input.Run.Tasks)
	if err != nil {
		return fmt.Errorf("encode recovered machine task list: %w", err)
	}
	state, err := json.Marshal(input.Run)
	if err != nil {
		return fmt.Errorf("encode recovered run state: %w", err)
	}
	start, err := BuildOrchestratorStartContext(OrchestratorStartInput{
		OpenSpecPackage: pkg.CompleteSpecification(), MachineTaskList: string(tasks), RunState: string(state),
	})
	if err != nil {
		return fmt.Errorf("build recovered orchestrator context: %w", err)
	}
	_, err = input.Owner.Restore(ctx, SessionRestore{Role: ResponseRoleOrchestrator, Start: start})
	return err
}
