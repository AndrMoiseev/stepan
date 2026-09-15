package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrFinalFindingTasks = errors.New("invalid final finding task route")

// FinalFindingTasksInput owns the one orchestrator turn that converts a
// failed final review into new, ordinary tasks. The orchestrator may edit only
// tasks.md; it cannot choose task status, bypass checks or review, or commit.
type FinalFindingTasksInput struct {
	Run        *implementationstate.Run
	StateStore *runstore.StateStore
	Journal    *runstore.Run
	Repository string
	Workspace  WorkspaceControl
	Session    *AgentSession

	Review    FinalReviewResult
	TasksPath string

	OperationID implementationstate.OperationID
	ResultID    implementationstate.ResultID
	CallID      string
	Limits      implementationstate.CycleLimits
	Timeout     time.Duration
}

type FinalFindingTasksResult struct {
	Call  ControlledAgentCallResult
	Tasks []implementationstate.Task
}

// AddFinalFindingTasks appends corrective root leaves after the completed
// source task list. Once appended they are indistinguishable from ordinary
// pending tasks to the briefer and the rest of the implementation loop.
func AddFinalFindingTasks(ctx context.Context, input FinalFindingTasksInput) (FinalFindingTasksResult, error) {
	finalReview, err := validateFinalFindingTasksInput(input)
	if err != nil {
		return FinalFindingTasksResult{}, err
	}
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	if existing := finalRunOperation(input.Run, input.OperationID); existing == nil {
		if err := input.Run.AddRunOperation(implementationstate.Operation{ID: input.OperationID, Kind: implementationstate.OperationAgent, Basis: basis, Description: "append tasks for final review findings"}); err != nil {
			return FinalFindingTasksResult{}, fmt.Errorf("%w: create task-addition operation: %v", ErrFinalFindingTasks, err)
		}
		// The operation is recorded before an external orchestrator turn. A crash
		// cannot make a Markdown edit look like a machine task transition.
		if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			return FinalFindingTasksResult{}, fmt.Errorf("%w: persist task-addition operation: %v", ErrFinalFindingTasks, err)
		}
	} else if existing.Kind != implementationstate.OperationAgent || existing.Description != "append tasks for final review findings" || existing.Basis != basis || finalRunResult(input.Run, input.ResultID) != nil {
		return FinalFindingTasksResult{}, fmt.Errorf("%w: task-addition operation cannot be retried", ErrFinalFindingTasks)
	}
	beforeMarkdown, err := readFinalFindingTasksMarkdown(input.Repository, input.TasksPath)
	if err != nil {
		return FinalFindingTasksResult{}, fmt.Errorf("%w: read tasks.md before task addition: %v", ErrFinalFindingTasks, err)
	}
	binding := ResponseBinding{CallID: input.CallID, RunID: input.Run.Identity.ID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	call := ControlledAgentCall{
		Session: input.Session, Repository: input.Repository, Workspace: input.Workspace,
		Policy: AgentCallPolicy{Role: AgentRoleOrchestrator, CallID: input.CallID, AllowedPaths: []string{input.TasksPath}},
		Run:    input.Run, Journal: input.Journal, StateStore: input.StateStore, OperationID: input.OperationID, Limits: input.Limits,
		Expectation: ResponseExpectation{Role: ResponseRoleOrchestrator, State: ResponseStateAddingTasks, Scope: ResponseScopeRun, Binding: binding},
		Message:     renderFinalFindingTaskRequest(finalReview, input.TasksPath), Timeout: input.Timeout,
		ValidateResponse: func(response AgentResponse) error {
			return validateFinalFindingTasksResponse(input.Run, input.Review.ResultID, finalReview, response)
		},
	}
	result, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return FinalFindingTasksResult{Call: result}, err
	}
	if result.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(result.Response)
		if err != nil {
			return FinalFindingTasksResult{Call: result}, err
		}
		return FinalFindingTasksResult{Call: result}, PersistExecutionBlock(context.WithoutCancel(ctx), input.StateStore, input.Run, block)
	}
	afterMarkdown, err := readFinalFindingTasksMarkdown(input.Repository, input.TasksPath)
	if err != nil {
		return FinalFindingTasksResult{Call: result}, fmt.Errorf("%w: read tasks.md after task addition: %v", ErrFinalFindingTasks, err)
	}
	if err := validateFinalFindingTasksMarkdownAppend(beforeMarkdown, afterMarkdown); err != nil {
		return FinalFindingTasksResult{Call: result}, err
	}
	tasks, err := decodeFinalFindingTasks(input.Review.ResultID, result.Response.TaskIDs, result.Response.TaskPayloads)
	if err != nil {
		return FinalFindingTasksResult{Call: result}, err
	}
	if err := validateFinalFindingCoverage(finalReview, tasks); err != nil {
		return FinalFindingTasksResult{Call: result}, err
	}
	for index := range tasks {
		tasks[index].Order = len(input.Run.Tasks) + index
	}
	evidence, err := publishFinalFindingTaskEvidence(input.Journal, input.ResultID, result.Response)
	if err != nil {
		return FinalFindingTasksResult{Call: result}, fmt.Errorf("%w: publish task-addition receipt: %v", ErrFinalFindingTasks, err)
	}
	if err := input.Run.AppendFinalFindingTasks(tasks); err != nil {
		return FinalFindingTasksResult{Call: result}, fmt.Errorf("%w: append machine tasks: %v", ErrFinalFindingTasks, err)
	}
	if err := input.Run.AddRunResult(implementationstate.OperationResult{ID: input.ResultID, OperationID: input.OperationID, Status: implementationstate.ResultSucceeded, State: input.Run.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		return FinalFindingTasksResult{Call: result}, fmt.Errorf("%w: record task-addition result: %v", ErrFinalFindingTasks, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return FinalFindingTasksResult{Call: result}, fmt.Errorf("%w: persist appended machine tasks: %v", ErrFinalFindingTasks, err)
	}
	return FinalFindingTasksResult{Call: result, Tasks: tasks}, nil
}

func readFinalFindingTasksMarkdown(repository, tasksPath string) ([]byte, error) {
	path, err := validateCallPath(tasksPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(repository, filepath.FromSlash(path)))
}

func validateFinalFindingTasksInput(input FinalFindingTasksInput) (AgentResponse, error) {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Session == nil || strings.TrimSpace(input.Repository) == "" || input.Review.Response.Kind != ResponseChangesRequested || input.Review.ResultID == "" || input.OperationID == "" || input.ResultID == "" || strings.TrimSpace(input.CallID) == "" || !validTransitionLimits(input.Limits) {
		return AgentResponse{}, fmt.Errorf("%w: run, failed final review, orchestrator, and controller identities are required", ErrFinalFindingTasks)
	}
	if input.Session.Role != ResponseRoleOrchestrator || !runHasOnlyCompletedTasks(input.Run) || !failedFinalReview(input.Run, input.Review.ResultID) {
		return AgentResponse{}, fmt.Errorf("%w: completed tasks and a durable failed final review are required", ErrFinalFindingTasks)
	}
	path, err := validateCallPath(input.TasksPath)
	expected, expectedErr := selectedChangeTasksPath(input.Run.Identity.Change)
	if err != nil || expectedErr != nil || path != expected {
		return AgentResponse{}, fmt.Errorf("%w: tasks path must name the selected OpenSpec tasks.md", ErrFinalFindingTasks)
	}
	finalReview, err := durableFailedFinalReview(input.Run, input.Journal, input.Review.ResultID)
	if err != nil {
		return AgentResponse{}, err
	}
	if !reflect.DeepEqual(input.Review.Response, finalReview) {
		return AgentResponse{}, fmt.Errorf("%w: supplied final findings differ from the durable final-review result", ErrFinalFindingTasks)
	}
	return finalReview, nil
}

func failedFinalReview(run *implementationstate.Run, resultID implementationstate.ResultID) bool {
	result := finalRunResult(run, resultID)
	operation := implementationstate.Operation{}
	if result != nil {
		if found := finalRunOperation(run, result.OperationID); found != nil {
			operation = *found
		}
	}
	return result != nil && result.Status == implementationstate.ResultFailed && operation.Kind == implementationstate.OperationReview && operation.Counter == implementationstate.CycleCounterFinalReview
}

func durableFailedFinalReview(run *implementationstate.Run, journal *runstore.Run, resultID implementationstate.ResultID) (AgentResponse, error) {
	result := finalRunResult(run, resultID)
	if result == nil || journal == nil {
		return AgentResponse{}, fmt.Errorf("%w: durable final-review receipt is required", ErrFinalFindingTasks)
	}
	for _, evidence := range result.Evidence {
		if evidence.ID != implementationstate.EvidenceID(string(resultID)+"-discussion") {
			continue
		}
		data, err := journal.Read(evidence)
		if err != nil {
			return AgentResponse{}, fmt.Errorf("%w: read durable final-review receipt: %v", ErrFinalFindingTasks, err)
		}
		var receipt struct {
			Response AgentResponse `json:"response"`
		}
		if err := json.Unmarshal(data, &receipt); err != nil {
			return AgentResponse{}, fmt.Errorf("%w: decode durable final-review receipt: %v", ErrFinalFindingTasks, err)
		}
		if err := validateFinalReviewerResponse(receipt.Response); err != nil || receipt.Response.Kind != ResponseChangesRequested {
			return AgentResponse{}, fmt.Errorf("%w: durable result is not a blocking final review: %v", ErrFinalFindingTasks, err)
		}
		return receipt.Response, nil
	}
	return AgentResponse{}, fmt.Errorf("%w: durable final-review receipt is missing", ErrFinalFindingTasks)
}

func validateFinalFindingTasksResponse(run *implementationstate.Run, reviewResultID implementationstate.ResultID, finalReview AgentResponse, response AgentResponse) error {
	if run == nil || response.Kind != ResponseTasksAdded || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != "" || response.Binding.BriefID != "" || response.Binding.Specification != run.Identity.Specification || response.Binding.Configuration != run.Identity.Configuration || response.Binding.TaskList != run.Identity.TaskList {
		return fmt.Errorf("%w: task additions are not bound to the final-review run", ErrFinalFindingTasks)
	}
	tasks, err := decodeFinalFindingTasks(reviewResultID, response.TaskIDs, response.TaskPayloads)
	if err != nil {
		return err
	}
	return validateFinalFindingCoverage(finalReview, tasks)
}

func validateFinalFindingTasksMarkdownAppend(before, after []byte) error {
	if bytes.Equal(before, after) {
		return fmt.Errorf("%w: orchestrator returned tasks_added without updating tasks.md", ErrFinalFindingTasks)
	}
	if !bytes.HasPrefix(after, before) {
		return fmt.Errorf("%w: tasks.md must preserve existing content and append final tasks at its end", ErrFinalFindingTasks)
	}
	return nil
}

func validateFinalFindingCoverage(review AgentResponse, tasks []implementationstate.Task) error {
	want := make(map[string]bool, len(review.FindingIDs))
	for _, id := range review.FindingIDs {
		want[id] = false
	}
	for _, task := range tasks {
		for _, finding := range task.FinalFindings {
			seen, ok := want[finding.FindingID]
			if !ok || seen {
				return fmt.Errorf("%w: task links an unknown or duplicate final finding %q", ErrFinalFindingTasks, finding.FindingID)
			}
			want[finding.FindingID] = true
		}
	}
	for id, seen := range want {
		if !seen {
			return fmt.Errorf("%w: final finding %q lacks a corrective task", ErrFinalFindingTasks, id)
		}
	}
	return nil
}

type finalFindingTaskPayload struct {
	ID         string   `json:"id"`
	ParentID   string   `json:"parent_id"`
	Title      string   `json:"title"`
	FindingIDs []string `json:"finding_ids"`
}

func decodeFinalFindingTasks(reviewResultID implementationstate.ResultID, ids []implementationstate.TaskID, payloads []string) ([]implementationstate.Task, error) {
	if reviewResultID == "" || len(ids) == 0 || len(ids) != len(payloads) {
		return nil, fmt.Errorf("%w: final task IDs and payloads must be non-empty and have equal length", ErrFinalFindingTasks)
	}
	seen := make(map[implementationstate.TaskID]bool, len(ids))
	tasks := make([]implementationstate.Task, 0, len(ids))
	for index, id := range ids {
		var payload finalFindingTaskPayload
		decoder := json.NewDecoder(strings.NewReader(payloads[index]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return nil, fmt.Errorf("%w: decode task %q: %v", ErrFinalFindingTasks, id, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: task %q has trailing values", ErrFinalFindingTasks, id)
		}
		if id == "" || payload.ID != string(id) || payload.ParentID != "" || strings.TrimSpace(payload.Title) == "" || len(payload.FindingIDs) == 0 || seen[id] {
			return nil, fmt.Errorf("%w: task %q is not an appended root finding task", ErrFinalFindingTasks, id)
		}
		findingSeen := make(map[string]bool, len(payload.FindingIDs))
		references := make([]implementationstate.FinalFindingReference, 0, len(payload.FindingIDs))
		for _, findingID := range payload.FindingIDs {
			if strings.TrimSpace(findingID) == "" || findingSeen[findingID] {
				return nil, fmt.Errorf("%w: task %q has invalid finding links", ErrFinalFindingTasks, id)
			}
			findingSeen[findingID] = true
			references = append(references, implementationstate.FinalFindingReference{ReviewResultID: reviewResultID, FindingID: findingID})
		}
		seen[id] = true
		tasks = append(tasks, implementationstate.Task{ID: id, Order: index, Title: payload.Title, FinalFindings: references})
	}
	return tasks, nil
}

func renderFinalFindingTaskRequest(review AgentResponse, tasksPath string) string {
	var text strings.Builder
	text.WriteString("Append new root corrective tasks only to the end of ")
	text.WriteString(tasksPath)
	text.WriteString(". Preserve every existing task and checkbox. Return tasks_added with each new task's id, title, parent_id as an empty string, and a non-empty finding_ids array that links it to these final findings. Do not change task status, select work, run checks, review, or commit.\n\n# Final findings\n")
	for index, id := range review.FindingIDs {
		fmt.Fprintf(&text, "\n## %s\n\nProblem: %s\n\nLocation: %s\n\nBasis: %s\n\nExpected result: %s\n", id, review.Findings[index], review.Locations[index], review.Bases[index], review.ExpectedResults[index])
	}
	return text.String()
}

func publishFinalFindingTaskEvidence(journal *runstore.Run, resultID implementationstate.ResultID, response AgentResponse) (implementationstate.EvidenceRef, error) {
	if journal == nil {
		return implementationstate.EvidenceRef{}, errors.New("task additions require a run journal")
	}
	data, err := json.Marshal(struct {
		Response AgentResponse `json:"response"`
	}{Response: response})
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(resultID)+"-tasks"), data)
}
