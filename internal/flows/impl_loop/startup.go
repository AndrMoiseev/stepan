package impl_loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// StartupRun is the read-only implementation context presented when Stepan
// starts in a working copy that has an unfinished run. It intentionally holds
// no StateStore, controller lease, runtime, or check runner: discovery alone
// must not be capable of continuing the run.
type StartupRun struct {
	Run     *implementationstate.Run
	Summary StartupSummary
}

// StartupSummary contains the small durable status panel rendered before an
// explicit lifecycle command. Stage and LastAction are descriptive only; the
// Run remains the source of truth for transitions.
type StartupSummary struct {
	Change     string
	Stage      string
	LastAction string
	StopReason string
	Lifecycle  ImplementationLifecycle
}

// DiscoverStartupRun finds the sole unfinished run for an already resolved
// Git work-copy root. It reads JSONL through FindUnclosedRun's status path and
// never opens a SQLite projection, takes a controller lock, starts a session,
// invokes an agent, or executes a check.
func DiscoverStartupRun(ctx context.Context, store *runstore.Store, workCopy string) (*StartupRun, error) {
	run, err := findUnclosedRun(ctx, store, workCopy)
	if err != nil || run == nil {
		return nil, err
	}
	return &StartupRun{Run: run, Summary: SummarizeStartupRun(run)}, nil
}

// SummarizeStartupRun derives a human-readable startup panel from durable run
// state. It does not mutate the state and is deliberately less detailed than
// the continuous-progress presentation added later.
func SummarizeStartupRun(run *implementationstate.Run) StartupSummary {
	if run == nil {
		return StartupSummary{Stage: "no implementation run", Lifecycle: LifecycleNoRun}
	}
	summary := StartupSummary{
		Change:     run.Identity.Change,
		Stage:      startupStage(run),
		LastAction: startupLastAction(run),
		Lifecycle:  lifecycleForRun(run),
	}
	switch run.Status {
	case implementationstate.RunPaused:
		summary.StopReason = run.PauseReason
		if run.ExecutionBlock != nil && strings.TrimSpace(run.ExecutionBlock.Diagnostic) != "" {
			summary.StopReason = run.ExecutionBlock.Diagnostic
		}
	case implementationstate.RunClosed:
		summary.StopReason = run.CloseReason
	}
	return summary
}

// FormatStartupSummary keeps startup rendering independent from terminal
// widgets. The caller may display the returned text and CommandsForLifecycle
// alongside its existing document-flow interface.
func FormatStartupSummary(summary StartupSummary) string {
	if summary.Lifecycle == LifecycleNoRun {
		return ""
	}
	lines := []string{
		"Unfinished implementation run:",
		fmt.Sprintf("  change: %s", summary.Change),
		fmt.Sprintf("  stage: %s", summary.Stage),
		fmt.Sprintf("  last action: %s", summary.LastAction),
	}
	if summary.StopReason != "" {
		lines = append(lines, fmt.Sprintf("  stop reason: %s", summary.StopReason))
	}
	commands := CommandsForLifecycle(summary.Lifecycle)
	if len(commands) != 0 {
		available := make([]string, 0, len(commands))
		for _, command := range commands {
			available = append(available, string(command.Command))
		}
		lines = append(lines, "  available: "+strings.Join(available, " "))
	}
	return strings.Join(lines, "\n")
}

func startupStage(run *implementationstate.Run) string {
	switch {
	case run.TaskExtractionPending:
		return "extracting implementation tasks"
	case run.InitialBaselineStatus == implementationstate.InitialBaselinePending:
		return "initial required checks"
	case pendingCommitAssignment(run) != "":
		return "committing accepted assignment " + string(pendingCommitAssignment(run))
	case activeAssignment(run) != "":
		return "implementing assignment " + string(activeAssignment(run))
	case run.FinalAcceptance == nil:
		return "final acceptance"
	default:
		return "finishing implementation run"
	}
}

func startupLastAction(run *implementationstate.Run) string {
	if operation := latestStartupOperation(run); operation != nil {
		if strings.TrimSpace(operation.Description) != "" {
			return operation.Description
		}
		return string(operation.Kind)
	}
	return "created implementation run"
}

func latestStartupOperation(run *implementationstate.Run) *implementationstate.Operation {
	// Active assignments are the newest actionable context. Their operation
	// history is therefore preferable to unrelated run-scoped setup work.
	for index := len(run.Assignments) - 1; index >= 0; index-- {
		assignment := &run.Assignments[index]
		if assignment.Status == implementationstate.AssignmentActive && len(assignment.Operations) != 0 {
			return &assignment.Operations[len(assignment.Operations)-1]
		}
	}
	if len(run.RunOperations) != 0 {
		return &run.RunOperations[len(run.RunOperations)-1]
	}
	return nil
}

func activeAssignment(run *implementationstate.Run) implementationstate.AssignmentID {
	for index := len(run.Assignments) - 1; index >= 0; index-- {
		if run.Assignments[index].Status == implementationstate.AssignmentActive {
			return run.Assignments[index].ID
		}
	}
	return ""
}

func pendingCommitAssignment(run *implementationstate.Run) implementationstate.AssignmentID {
	for index := len(run.Assignments) - 1; index >= 0; index-- {
		if run.Assignments[index].Status == implementationstate.AssignmentAcceptedAwaitingCommit {
			return run.Assignments[index].ID
		}
	}
	return ""
}
