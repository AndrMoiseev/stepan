package impl_loop

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// StartupRun is the read-only implementation context presented when Stepan
// starts in a working copy that has an unfinished run. It intentionally holds
// no StateStore, controller lease, runtime, or check runner: discovery alone
// must not be capable of continuing the run.
type StartupRun struct {
	Run              *implementationstate.Run
	Summary          StartupSummary
	RecoveryRequired bool
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
	journal, err := store.Open(run.Identity.ID)
	if err != nil {
		return nil, fmt.Errorf("open discovered implementation run: %w", err)
	}
	events, err := runstore.JournalStates(journal)
	if err != nil {
		return nil, err
	}
	summary := summarizeStartupJournal(run, events)
	recoveryRequired := false
	if run.Status == implementationstate.RunActive {
		owned, err := ControllerOwned(ctx, store, workCopy)
		if err != nil {
			return nil, err
		}
		if !owned {
			recoveryRequired = true
			summary.Lifecycle = LifecyclePaused
			summary.StopReason = "previous Stepan controller was interrupted; explicit /resume required"
		}
	}
	return &StartupRun{Run: run, Summary: summary, RecoveryRequired: recoveryRequired}, nil
}

func summarizeStartupJournal(run *implementationstate.Run, events []implementationstate.Event) StartupSummary {
	summary := SummarizeStartupRun(run)
	if action, resumeCheck := latestJournalAction(events); action != "" {
		summary.LastAction = action
		if resumeCheck {
			summary.Stage = "resume required checks"
		}
	}
	return summary
}

// latestJournalAction walks durable snapshots backwards, rather than relying
// on the projection's structural order. A failed resume writes a result and
// then a separate pause event, so the pause is attributed to the immediately
// preceding uncounted check result instead of hiding it as merely "pause run".
func latestJournalAction(events []implementationstate.Event) (string, bool) {
	for index := len(events) - 1; index > 0; index-- {
		previous, current := events[index-1].State, events[index].State
		if current == nil || previous == nil {
			continue
		}
		if current.Status != previous.Status && current.Status == implementationstate.RunPaused {
			// Some controller routes add the failed assignment result and the
			// execution-blocked pause to one cloned candidate before recording a
			// single event. Attribute that atomic transition to the result first.
			if result := latestChangedRunResult(previous, current); result != nil {
				if operation := runOperation(current, result.OperationID); operation != nil {
					return operationAction(operation) + " (failed)", operation.UncountedResumeCheck
				}
			}
			if result := latestChangedAssignmentResult(previous, current); result != nil {
				if operation := startupAssignmentOperation(current, result.assignment, result.result.OperationID); operation != nil {
					return operationAction(operation) + " (failed)", false
				}
			}
			if action, resumeCheck, ok := resultActionAt(events, index-1); ok {
				return action + " (failed)", resumeCheck
			}
			return "pause run", false
		}
		if result := latestChangedRunResult(previous, current); result != nil {
			if operation := runOperation(current, result.OperationID); operation != nil {
				if operation.UncountedResumeCheck {
					return operationAction(operation), true
				}
				return operationAction(operation), false
			}
		}
		if result := latestChangedAssignmentResult(previous, current); result != nil {
			if operation := startupAssignmentOperation(current, result.assignment, result.result.OperationID); operation != nil {
				return operationAction(operation), false
			}
		}
		if action := journalLastAction(previous, current, ""); action != "" {
			return action, false
		}
	}
	return "", false
}

func resultActionAt(events []implementationstate.Event, index int) (string, bool, bool) {
	if index <= 0 || index >= len(events) {
		return "", false, false
	}
	previous, current := events[index-1].State, events[index].State
	if result := latestChangedRunResult(previous, current); result != nil {
		if operation := runOperation(current, result.OperationID); operation != nil {
			return operationAction(operation), operation.UncountedResumeCheck, true
		}
	}
	if result := latestChangedAssignmentResult(previous, current); result != nil {
		if operation := startupAssignmentOperation(current, result.assignment, result.result.OperationID); operation != nil {
			return operationAction(operation), false, true
		}
	}
	return "", false, false
}

func journalLastAction(previous, current *implementationstate.Run, fallback string) string {
	if previous == nil || current == nil {
		return fallback
	}
	if current.Status != previous.Status {
		switch current.Status {
		case implementationstate.RunPaused:
			if operation := latestChangedOperation(previous, current); operation != nil && operation.UncountedResumeCheck {
				return operationAction(operation) + " (failed)"
			}
			return "pause run"
		case implementationstate.RunClosed:
			return "close run"
		case implementationstate.RunActive:
			return "resume reconciliation"
		}
	}
	if operation := latestChangedOperation(previous, current); operation != nil {
		return operationAction(operation)
	}
	if assignment := latestChangedAssignment(previous, current); assignment != nil {
		switch assignment.Status {
		case implementationstate.AssignmentActive:
			return "select assignment " + string(assignment.ID)
		case implementationstate.AssignmentAcceptedAwaitingCommit:
			return "accept assignment " + string(assignment.ID)
		case implementationstate.AssignmentCommitted:
			return "commit assignment " + string(assignment.ID)
		}
	}
	if current.InitialBaselineStatus != previous.InitialBaselineStatus {
		return "record initial required checks"
	}
	if current.TaskExtractionPending != previous.TaskExtractionPending {
		return "record extracted implementation tasks"
	}
	return fallback
}

func latestChangedOperation(previous, current *implementationstate.Run) *implementationstate.Operation {
	for index := len(current.RunOperations) - 1; index >= 0; index-- {
		if index >= len(previous.RunOperations) || !reflect.DeepEqual(current.RunOperations[index], previous.RunOperations[index]) {
			return &current.RunOperations[index]
		}
	}
	for i := len(current.Assignments) - 1; i >= 0; i-- {
		if i >= len(previous.Assignments) {
			continue
		}
		for operation := len(current.Assignments[i].Operations) - 1; operation >= 0; operation-- {
			if operation >= len(previous.Assignments[i].Operations) || !reflect.DeepEqual(current.Assignments[i].Operations[operation], previous.Assignments[i].Operations[operation]) {
				return &current.Assignments[i].Operations[operation]
			}
		}
	}
	return nil
}

func latestChangedRunResult(previous, current *implementationstate.Run) *implementationstate.OperationResult {
	for index := len(current.RunResults) - 1; index >= 0; index-- {
		if index >= len(previous.RunResults) || !reflect.DeepEqual(current.RunResults[index], previous.RunResults[index]) {
			return &current.RunResults[index]
		}
	}
	return nil
}

type assignmentResultChange struct {
	assignment implementationstate.AssignmentID
	result     *implementationstate.OperationResult
}

func latestChangedAssignmentResult(previous, current *implementationstate.Run) *assignmentResultChange {
	for assignment := len(current.Assignments) - 1; assignment >= 0; assignment-- {
		if assignment >= len(previous.Assignments) {
			continue
		}
		currentResults := current.Assignments[assignment].Results
		previousResults := previous.Assignments[assignment].Results
		for result := len(currentResults) - 1; result >= 0; result-- {
			if result >= len(previousResults) || !reflect.DeepEqual(currentResults[result], previousResults[result]) {
				return &assignmentResultChange{assignment: current.Assignments[assignment].ID, result: &currentResults[result]}
			}
		}
	}
	return nil
}

func runOperation(run *implementationstate.Run, id implementationstate.OperationID) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	for index := range run.RunOperations {
		if run.RunOperations[index].ID == id {
			return &run.RunOperations[index]
		}
	}
	return nil
}

func startupAssignmentOperation(run *implementationstate.Run, assignment implementationstate.AssignmentID, id implementationstate.OperationID) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	for assignmentIndex := range run.Assignments {
		current := &run.Assignments[assignmentIndex]
		if current.ID != assignment {
			continue
		}
		for operationIndex := range current.Operations {
			if current.Operations[operationIndex].ID == id {
				return &current.Operations[operationIndex]
			}
		}
	}
	return nil
}

func latestChangedAssignment(previous, current *implementationstate.Run) *implementationstate.Assignment {
	for index := len(current.Assignments) - 1; index >= 0; index-- {
		if index >= len(previous.Assignments) || !reflect.DeepEqual(current.Assignments[index], previous.Assignments[index]) {
			return &current.Assignments[index]
		}
	}
	return nil
}

func operationAction(operation *implementationstate.Operation) string {
	if operation == nil {
		return ""
	}
	if operation.UncountedResumeCheck {
		if strings.TrimSpace(operation.Description) != "" {
			return "resume required check: " + operation.Description
		}
		return "resume required checks"
	}
	if strings.TrimSpace(operation.Description) != "" {
		return operation.Description
	}
	return string(operation.Kind)
}

func journalStage(current, previous *implementationstate.Run, fallback string) string {
	if operation := latestChangedOperation(previous, current); operation != nil && operation.UncountedResumeCheck {
		return "resume required checks"
	}
	return fallback
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
	case len(run.PendingLeafTasks()) != 0:
		return "selecting next assignment"
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
