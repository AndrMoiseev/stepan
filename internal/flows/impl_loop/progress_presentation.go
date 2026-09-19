package impl_loop

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/checkexec"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// ProgressPresentationInput contains only already durable controller facts.
// It intentionally has no provider transcript or raw agent response: those
// streams are not part of the interactive implementation UI.
type ProgressPresentationInput struct {
	Run             *implementationstate.Run
	Runtime         setting.Platform
	StartedAt       time.Time
	Now             time.Time
	ArtifactPath    func(implementationstate.EvidenceRef) (string, error)
	ReadArtifact    func(implementationstate.EvidenceRef) ([]byte, error)
	RecentCheckRuns []CheckSetResult
}

// FormatImplementationProgress renders the small, user-facing status panel
// for active, paused, and terminal runs. It is deliberately a pure formatter
// so the terminal does not own lifecycle decisions or inspect provider output.
func FormatImplementationProgress(input ProgressPresentationInput) string {
	if input.Run == nil {
		return "no implementation run for this working copy"
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	lines := []string{fmt.Sprintf("implementation run %s is %s", input.Run.Identity.ID, input.Run.Status)}
	if input.Runtime.OS != "" && input.Runtime.Architecture != "" {
		lines = append(lines, fmt.Sprintf("runtime platform: %s/%s", input.Runtime.OS, input.Runtime.Architecture))
	}
	if !input.StartedAt.IsZero() && !input.Now.Before(input.StartedAt) {
		lines = append(lines, "duration: "+input.Now.Sub(input.StartedAt).Round(time.Second).String())
	}
	if assignment := currentPresentationAssignment(input.Run); assignment != nil {
		lines = append(lines, formatAssignment(input.Run, assignment))
	}
	if operation := activeOperation(input.Run); operation != nil {
		lines = append(lines, fmt.Sprintf("current: %s — %s", presentationRole(*operation), operationDescription(*operation)))
	}
	if counters := formatCounters(input.Run); counters != "" {
		lines = append(lines, "counters: "+counters)
	}
	if result, operation := latestResult(input.Run); result != nil && operation != nil {
		lines = append(lines, fmt.Sprintf("latest result: %s %s", presentationRole(*operation), result.Status))
		lines = append(lines, formatPublishedCheckResults(result.Evidence, input.ReadArtifact)...)
		if links := formatArtifactLinks(result.Evidence, input.ArtifactPath); links != "" {
			lines = append(lines, "artifacts: "+links)
		}
	}
	for _, check := range input.RecentCheckRuns {
		if line := FormatCheckProgressResult(check, input.Runtime); line != "" {
			lines = append(lines, line)
		}
	}
	if input.Run.ExecutionBlock != nil {
		block := input.Run.ExecutionBlock
		lines = append(lines, fmt.Sprintf("diagnostic pause: %s — %s; required: %s", block.BlockedAction, block.Diagnostic, block.RequiredUserAction))
	}
	if input.Run.Status == implementationstate.RunClosed || input.Run.Status == implementationstate.RunSucceeded {
		lines = append(lines, "final summary: terminal run retained for audit")
	}
	return strings.Join(lines, "\n")
}

// checkProgressEvidence is the intentionally narrow, persisted check summary
// written by the controller. It has no raw stdout/stderr, so decoding it for
// the UI cannot turn an internal agent stream into terminal output.
type checkProgressEvidence struct {
	Results []struct {
		Name         string             `json:"name"`
		Status       CheckStatus        `json:"status"`
		Command      string             `json:"command,omitempty"`
		Presentation *CheckPresentation `json:"presentation,omitempty"`
	} `json:"results"`
}

func formatPublishedCheckResults(references []implementationstate.EvidenceRef, read func(implementationstate.EvidenceRef) ([]byte, error)) []string {
	if read == nil {
		return nil
	}
	var lines []string
	for _, reference := range references {
		data, err := read(reference)
		if err != nil {
			continue
		}
		var evidence checkProgressEvidence
		if err := json.Unmarshal(data, &evidence); err != nil || len(evidence.Results) == 0 {
			continue
		}
		for _, check := range evidence.Results {
			if strings.TrimSpace(check.Name) == "" {
				continue
			}
			line := fmt.Sprintf("check %s: %s", check.Name, check.Status)
			if check.Command != "" {
				line += "; command " + check.Command
			}
			if check.Presentation != nil {
				if check.Presentation.Duration > 0 {
					line += " in " + check.Presentation.Duration.Round(time.Second).String()
				}
				logs := make([]string, 0, 2)
				for _, log := range []CheckLog{check.Presentation.Stdout, check.Presentation.Stderr} {
					if log.Path != "" {
						logs = append(logs, filepath.Clean(log.Path))
					} else if log.Reference.ID != "" {
						logs = append(logs, string(log.Reference.ID))
					}
				}
				if len(logs) != 0 {
					line += "; logs " + strings.Join(logs, ", ")
				}
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// FormatCheckProgressResult reports a completed configured check without
// exposing command environment values. GOOS/GOARCH are the sole exception:
// they describe a cross-build target, and the wording explicitly prevents a
// successful cross-build from being represented as target-OS runtime evidence.
func FormatCheckProgressResult(check CheckSetResult, runtimePlatform setting.Platform) string {
	if strings.TrimSpace(check.Name) == "" {
		return ""
	}
	line := fmt.Sprintf("check %s: %s", check.Name, check.Status)
	if check.Duration > 0 {
		line += " in " + check.Duration.Round(time.Second).String()
	}
	if target := crossBuildTarget(check.Command, runtimePlatform); target != "" {
		line += "; cross-build target " + target + " (artifact build only; target runtime not accepted)"
	}
	return line
}

func crossBuildTarget(command checkexec.Command, runtimePlatform setting.Platform) string {
	if command.Env == nil {
		return ""
	}
	os, architecture := strings.TrimSpace(command.Env["GOOS"]), strings.TrimSpace(command.Env["GOARCH"])
	if os == "" || architecture == "" || (os == runtimePlatform.OS && architecture == runtimePlatform.Architecture) {
		return ""
	}
	return os + "/" + architecture
}

func currentPresentationAssignment(run *implementationstate.Run) *implementationstate.Assignment {
	if run == nil {
		return nil
	}
	for index := len(run.Assignments) - 1; index >= 0; index-- {
		if run.Assignments[index].Status == implementationstate.AssignmentActive {
			return &run.Assignments[index]
		}
	}
	return nil
}

func activeOperation(run *implementationstate.Run) *implementationstate.Operation {
	if run == nil {
		return nil
	}
	var active *implementationstate.Operation
	consider := func(operation *implementationstate.Operation, hasResult bool) {
		if operation != nil && !hasResult {
			active = operation
		}
	}
	for index := range run.RunOperations {
		consider(&run.RunOperations[index], runResultExists(run.RunResults, run.RunOperations[index].ID))
	}
	for assignmentIndex := range run.Assignments {
		assignment := &run.Assignments[assignmentIndex]
		for operationIndex := range assignment.Operations {
			consider(&assignment.Operations[operationIndex], runResultExists(assignment.Results, assignment.Operations[operationIndex].ID))
		}
	}
	return active
}

func latestResult(run *implementationstate.Run) (*implementationstate.OperationResult, *implementationstate.Operation) {
	if run == nil {
		return nil, nil
	}
	var result *implementationstate.OperationResult
	var operation *implementationstate.Operation
	for index := range run.RunResults {
		if found := findOperation(run.RunOperations, run.RunResults[index].OperationID); found != nil {
			result, operation = &run.RunResults[index], found
		}
	}
	for assignmentIndex := range run.Assignments {
		assignment := &run.Assignments[assignmentIndex]
		for resultIndex := range assignment.Results {
			if found := findOperation(assignment.Operations, assignment.Results[resultIndex].OperationID); found != nil {
				result, operation = &assignment.Results[resultIndex], found
			}
		}
	}
	return result, operation
}

func findOperation(operations []implementationstate.Operation, id implementationstate.OperationID) *implementationstate.Operation {
	for index := range operations {
		if operations[index].ID == id {
			return &operations[index]
		}
	}
	return nil
}

func runResultExists(results []implementationstate.OperationResult, operationID implementationstate.OperationID) bool {
	for _, result := range results {
		if result.OperationID == operationID {
			return true
		}
	}
	return false
}

func formatAssignment(run *implementationstate.Run, assignment *implementationstate.Assignment) string {
	tasks := make([]string, 0, len(assignment.TaskIDs))
	for _, id := range assignment.TaskIDs {
		label := string(id)
		for _, task := range run.Tasks {
			if task.ID == id {
				label += " (" + task.Title + ")"
				break
			}
		}
		tasks = append(tasks, label)
	}
	return fmt.Sprintf("assignment: %s — %s", assignment.ID, strings.Join(tasks, ", "))
}

func presentationRole(operation implementationstate.Operation) string {
	switch operation.Kind {
	case implementationstate.OperationCheck:
		return "check"
	case implementationstate.OperationReview:
		if operation.Counter == implementationstate.CycleCounterFinalReview {
			return "final reviewer"
		}
		return "task reviewer"
	case implementationstate.OperationAgent:
		description := strings.ToLower(operation.Description)
		switch {
		case strings.Contains(description, "select") || strings.Contains(description, "brief"):
			return "briefer"
		case strings.Contains(description, "extract") || strings.Contains(description, "reflect") || strings.Contains(description, "append tasks"):
			return "orchestrator"
		case strings.Contains(description, "explor"):
			return "explorer"
		default:
			return "implementer"
		}
	default:
		return "controller"
	}
}

func operationDescription(operation implementationstate.Operation) string {
	if strings.TrimSpace(operation.Description) != "" {
		return operation.Description
	}
	return string(operation.ID)
}

func formatCounters(run *implementationstate.Run) string {
	counters := make(map[implementationstate.CycleCounter]uint64)
	for _, operation := range run.RunOperations {
		counters[operation.Counter] += uint64(len(operation.Attempts))
	}
	for _, assignment := range run.Assignments {
		for _, operation := range assignment.Operations {
			counters[operation.Counter] += uint64(len(operation.Attempts))
		}
	}
	parts := make([]string, 0, len(counters))
	for _, counter := range []implementationstate.CycleCounter{implementationstate.CycleCounterMandatoryChecks, implementationstate.CycleCounterChecksRequested, implementationstate.CycleCounterAssignmentReview, implementationstate.CycleCounterBriefRefinement, implementationstate.CycleCounterExplorer, implementationstate.CycleCounterFinalReview} {
		if counters[counter] != 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", counter, counters[counter]))
		}
	}
	return strings.Join(parts, ", ")
}

func formatArtifactLinks(references []implementationstate.EvidenceRef, resolve func(implementationstate.EvidenceRef) (string, error)) string {
	if len(references) == 0 {
		return ""
	}
	links := make([]string, 0, len(references))
	for _, reference := range references {
		link := string(reference.ID)
		if resolve != nil {
			if path, err := resolve(reference); err == nil && path != "" {
				link = filepath.Clean(path)
			}
		}
		links = append(links, link)
	}
	return strings.Join(links, ", ")
}
