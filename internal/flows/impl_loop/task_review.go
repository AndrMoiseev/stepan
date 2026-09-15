package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var ErrInvalidTaskReviewRoute = errors.New("invalid task review route")

// TaskReviewInput contains only controller-allocated identities.  In
// particular, a reviewer cannot select an assignment baseline, a check
// result, or a session by returning them in its JSON response.
type TaskReviewInput struct {
	Owner        *SessionOwner
	Workspace    WorkspaceControl
	Run          *implementationstate.Run
	StateStore   *runstore.StateStore
	Journal      *runstore.Run
	Repository   string
	AssignmentID implementationstate.AssignmentID
	Rules        RulesIndex
	Checks       []CheckCatalogEntry
	OperationID  implementationstate.OperationID
	ResultID     implementationstate.ResultID
	CallID       string
	Limits       implementationstate.CycleLimits
	Timeout      time.Duration // zero selects ControlledAgentCall default
	// ReviewTestChanges is derived by the controller from the complete diff.
	// It cannot be supplied by a reviewer.
	ReviewTestChanges bool
}

// TaskReviewResult is evidence of review only.  It intentionally carries no
// acceptance or commit transition; task 10 consumes a successful review later.
type TaskReviewResult struct {
	Response AgentResponse
	Session  *AgentSession
	Record   implementationstate.TaskReviewRecord
	Attempts uint64
	DiffBase string
}

// StartTaskReview is the sole entry point for a new assignment-review round.
// It proves the current mandatory check gate before opening the task-reviewer
// session, then gives that reviewer the entire assignment diff and durable
// evidence rather than an executor-selected summary.
func StartTaskReview(ctx context.Context, input TaskReviewInput) (TaskReviewResult, error) {
	if err := validateTaskReviewInput(input); err != nil {
		return TaskReviewResult{}, err
	}
	if input.Owner == nil {
		return TaskReviewResult{}, fmt.Errorf("%w: task reviewer owner is required to start a new review", ErrInvalidTaskReviewRoute)
	}
	if err := CanStartTaskReview(input.Run, input.AssignmentID); err != nil {
		return TaskReviewResult{}, err
	}
	brief, err := currentAssignmentBrief(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return TaskReviewResult{}, err
	}
	diffBase := assignmentDiffBase(input.Run, input.AssignmentID)
	diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, diffBase)
	if err != nil {
		return TaskReviewResult{}, err
	}
	checks, err := currentRequiredCheckEvidence(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return TaskReviewResult{}, err
	}
	previousDiscussion := renderTaskReviewHistory(input.Run, input.AssignmentID)
	start, err := BuildTaskReviewerReviewContext(TaskReviewerStartInput{
		TaskRoleStartInput: TaskRoleStartInput{AssignmentID: input.AssignmentID, BriefID: brief.ID, Brief: brief.Text, Rules: input.Rules, Checks: input.Checks},
		AssignmentDiff:     diff, RequiredCheckResults: checks, PreviousDiscussion: previousDiscussion,
	})
	if err != nil {
		return TaskReviewResult{}, err
	}
	session, err := input.Owner.Assignment(ctx, input.AssignmentID, ResponseRoleTaskReviewer, start)
	if err != nil {
		return TaskReviewResult{}, fmt.Errorf("start task reviewer session: %w", err)
	}
	input.ReviewTestChanges = modifiedExistingTests(diff)
	packet := fmt.Sprintf("# Current task review packet\n\n## Current complete brief\n\n%s\n\n", brief.Text) + renderTaskReviewPacket(input.Rules, diff, checks, previousDiscussion) + "\n\nReview this complete current packet and return the next structured review action."
	return runTaskReviewerTurn(ctx, input, session, brief.ID, packet, diffBase)
}

// RouteTaskReviewDispute forwards an executor's single-finding objection to
// the already-live reviewer session.  It is a continuation, not a new review
// start, so it never creates an independent reviewer conversation.
func RouteTaskReviewDispute(ctx context.Context, input TaskReviewInput, reviewer *AgentSession, dispute AgentResponse) (TaskReviewResult, error) {
	if err := validateTaskReviewInput(input); err != nil {
		return TaskReviewResult{}, err
	}
	if reviewer == nil || reviewer.Role != ResponseRoleTaskReviewer {
		return TaskReviewResult{}, fmt.Errorf("%w: current task reviewer session is required", ErrInvalidTaskReviewRoute)
	}
	if err := validateExecutorDispute(input.Run, input.AssignmentID, dispute); err != nil {
		return TaskReviewResult{}, err
	}
	if err := input.Run.RecordTaskReviewDispute(input.AssignmentID, implementationstate.TaskReviewDispute{FindingID: dispute.FindingIDs[0], Arguments: *dispute.Message, References: slices.Clone(dispute.References)}); err != nil {
		return TaskReviewResult{}, fmt.Errorf("%w: record executor dispute: %v", ErrInvalidTaskReviewRoute, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return TaskReviewResult{}, fmt.Errorf("%w: persist executor dispute: %v", ErrInvalidTaskReviewRoute, err)
	}
	brief, err := currentAssignmentBrief(input.Journal, input.Run, input.AssignmentID)
	if err != nil {
		return TaskReviewResult{}, err
	}
	finding := dispute.FindingIDs[0]
	message := fmt.Sprintf("# Executor review dispute\n\nFinding ID: %s\n\n## Arguments\n\n%s\n\n## Supporting references\n%s\n\nReconsider this finding in the existing review discussion. Resolve it with a reason, or retain it while answering these arguments.", finding, *dispute.Message, markdownList(dispute.References))
	diff, err := effectiveWorkspaceControl(input.Workspace).AssignmentDiff(ctx, input.Repository, assignmentDiffBase(input.Run, input.AssignmentID))
	if err != nil {
		return TaskReviewResult{}, err
	}
	input.ReviewTestChanges = modifiedExistingTests(diff)
	return runTaskReviewerTurn(ctx, input, reviewer, brief.ID, message, assignmentDiffBase(input.Run, input.AssignmentID))
}

// RouteTaskReviewChanges returns reviewer findings to the exact executor
// session.  The executor may dispute one open finding, request configured
// checks, or announce implementation readiness; it never receives authority
// to close the finding itself.
func RouteTaskReviewChanges(ctx context.Context, review TaskReviewResult, executorCall ControlledAgentCall) (ControlledAgentCallResult, error) {
	if review.Response.Kind != ResponseChangesRequested || executorCall.Session == nil || executorCall.Session.Role != ResponseRoleImplementer {
		return ControlledAgentCallResult{}, fmt.Errorf("%w: changes must return to an executor session", ErrInvalidTaskReviewRoute)
	}
	if executorCall.Run == nil || executorCall.AssignmentID == "" || executorCall.AssignmentID != review.Response.Binding.AssignmentID || executorCall.Expectation.Role != ResponseRoleImplementer || executorCall.Expectation.State != ResponseStateImplementing || executorCall.Policy.Role != AgentRoleExecutor || executorCall.Policy.CallID != executorCall.Expectation.Binding.CallID || !sameTaskReviewBinding(executorCall.Expectation.Binding, review.Response.Binding) {
		return ControlledAgentCallResult{}, fmt.Errorf("%w: executor continuation is not bound to reviewed assignment", ErrInvalidTaskReviewRoute)
	}
	previous := executorCall.ValidateResponse
	executorCall.ValidateResponse = func(response AgentResponse) error {
		if response.Kind == ResponseExecutionBlocked {
			_, err := ExecutionBlockFromResponse(response)
			return err
		}
		if response.Kind != ResponseImplementationReady && response.Kind != ResponseChecksRequested && response.Kind != ResponseReviewDisputed {
			return fmt.Errorf("%w: executor must fix, check, or dispute review findings", ErrInvalidTaskReviewRoute)
		}
		if err := validateImplementerResponseBinding(executorCall.Run, executorCall.AssignmentID, executorCall.Expectation.Binding.BriefID, response); err != nil {
			return err
		}
		if response.Kind == ResponseReviewDisputed {
			return validateExecutorDispute(executorCall.Run, executorCall.AssignmentID, response)
		}
		if previous != nil {
			return previous(response)
		}
		return nil
	}
	executorCall.Message = taskReviewChangesMessage(review.Record)
	turn, err := InvokeControlledAgentCall(ctx, executorCall)
	if err != nil || turn.Response.Kind != ResponseExecutionBlocked {
		return turn, err
	}
	block, err := ExecutionBlockFromResponse(turn.Response)
	if err != nil {
		return turn, err
	}
	return turn, PersistExecutionBlock(ctx, executorCall.StateStore, executorCall.Run, block)
}

type assignmentBrief struct {
	ID   implementationstate.BriefID
	Text string
}

func runTaskReviewerTurn(ctx context.Context, input TaskReviewInput, session *AgentSession, briefID implementationstate.BriefID, message, diffBase string) (TaskReviewResult, error) {
	basis := implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}
	existing := assignmentOperation(input.Run, input.AssignmentID, input.OperationID)
	if existing == nil {
		if err := input.Run.AddOperation(input.AssignmentID, implementationstate.Operation{ID: input.OperationID, Kind: implementationstate.OperationReview, BriefID: briefID, Basis: basis, Description: "task review", Counter: implementationstate.CycleCounterAssignmentReview}); err != nil {
			return TaskReviewResult{}, fmt.Errorf("%w: create review operation: %v", ErrInvalidTaskReviewRoute, err)
		}
		if _, err := input.StateStore.Record(ctx, input.Run); err != nil {
			return TaskReviewResult{}, fmt.Errorf("%w: persist review operation: %v", ErrInvalidTaskReviewRoute, err)
		}
	} else if existing.Kind != implementationstate.OperationReview || existing.BriefID != briefID || existing.Basis != basis || existing.Description != "task review" || existing.Counter != implementationstate.CycleCounterAssignmentReview || assignmentResultForOperation(input.Run, input.AssignmentID, existing.ID) != nil {
		return TaskReviewResult{}, fmt.Errorf("%w: review operation cannot be resumed", ErrInvalidTaskReviewRoute)
	}
	binding := ResponseBinding{CallID: input.CallID, RunID: input.Run.Identity.ID, AssignmentID: input.AssignmentID, BriefID: briefID, Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration, TaskList: input.Run.Identity.TaskList}
	call := ControlledAgentCall{Session: session, Repository: input.Repository, Workspace: input.Workspace, Policy: AgentCallPolicy{Role: AgentRoleTaskReviewer, CallID: input.CallID}, Run: input.Run, Journal: input.Journal, StateStore: input.StateStore, AssignmentID: input.AssignmentID, OperationID: input.OperationID, Limits: input.Limits, Expectation: ResponseExpectation{Role: ResponseRoleTaskReviewer, State: ResponseStateTaskReview, Scope: ResponseScopeAssignment, Binding: binding}, Message: message}
	call.Timeout = input.Timeout
	call.ValidateResponse = func(response AgentResponse) error {
		if response.Kind == ResponseExecutionBlocked {
			_, err := ExecutionBlockFromResponse(response)
			return err
		}
		if err := validateTaskReviewerResponse(input.Run, input.AssignmentID, response); err != nil {
			return err
		}
		if input.ReviewTestChanges && response.Kind == ResponseReviewPassed && !reviewPassExplainsTestChange(response) {
			return fmt.Errorf("%w: review of changed existing tests must state brief rationale and retained or replacement coverage", ErrInvalidTaskReviewRoute)
		}
		return nil
	}
	call.ContinueOnResponseRejection = true
	turn, err := InvokeControlledAgentCall(ctx, call)
	if err != nil {
		return TaskReviewResult{Session: session, Attempts: turn.Attempts, DiffBase: diffBase}, err
	}
	if turn.Response.Kind == ResponseExecutionBlocked {
		block, err := ExecutionBlockFromResponse(turn.Response)
		if err != nil {
			return TaskReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
		}
		if err := PersistExecutionBlock(ctx, input.StateStore, input.Run, block); err != nil {
			return TaskReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
		}
		return TaskReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, nil
	}
	record, resultStatus, err := taskReviewRecord(input.Run, input.AssignmentID, input.OperationID, input.ResultID, turn.Response)
	if err != nil {
		return TaskReviewResult{Response: turn.Response, Session: turn.Session, Attempts: turn.Attempts, DiffBase: diffBase}, err
	}
	evidence, err := publishTaskReviewEvidence(input.Journal, input.ResultID, record)
	if err != nil {
		return TaskReviewResult{}, err
	}
	if err := input.Run.AddResult(input.AssignmentID, implementationstate.OperationResult{ID: input.ResultID, OperationID: input.OperationID, Status: resultStatus, State: input.Run.CurrentState, Basis: basis, Evidence: []implementationstate.EvidenceRef{evidence}}); err != nil {
		return TaskReviewResult{}, err
	}
	if err := input.Run.RecordTaskReview(input.AssignmentID, record); err != nil {
		return TaskReviewResult{}, err
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return TaskReviewResult{}, err
	}
	return TaskReviewResult{Response: turn.Response, Session: turn.Session, Record: record, Attempts: turn.Attempts, DiffBase: diffBase}, nil
}

func validateTaskReviewInput(input TaskReviewInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.OperationID == "" || input.ResultID == "" || strings.TrimSpace(input.CallID) == "" || !validTransitionLimits(input.Limits) {
		return fmt.Errorf("%w: durable state, assignment, review identities, and limits are required", ErrInvalidTaskReviewRoute)
	}
	return nil
}

func currentAssignmentBrief(journal *runstore.Run, run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (assignmentBrief, error) {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil || len(assignment.Briefs) == 0 {
		return assignmentBrief{}, fmt.Errorf("%w: assignment has no current brief", ErrInvalidTaskReviewRoute)
	}
	brief := assignment.Briefs[len(assignment.Briefs)-1]
	data, err := journal.Read(brief.Document)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return assignmentBrief{}, fmt.Errorf("%w: read current brief: %v", ErrInvalidTaskReviewRoute, err)
	}
	return assignmentBrief{ID: brief.ID, Text: string(data)}, nil
}

func assignmentForReview(run *implementationstate.Run, id implementationstate.AssignmentID) *implementationstate.Assignment {
	if run == nil {
		return nil
	}
	for index := range run.Assignments {
		if run.Assignments[index].ID == id {
			return &run.Assignments[index]
		}
	}
	return nil
}

func assignmentDiffBase(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) string {
	base := run.Identity.BaselineCommit
	for _, assignment := range run.Assignments {
		if assignment.ID == assignmentID {
			break
		}
		if assignment.Commit != nil && assignment.Commit.CommitID != "" {
			base = assignment.Commit.CommitID
		}
	}
	return base
}

func gitAssignmentDiff(ctx context.Context, repository, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("%w: assignment diff base is required", ErrInvalidTaskReviewRoute)
	}
	command := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--no-ext-diff", "--find-renames", base, "--")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: capture assignment diff: %v: %s", ErrInvalidTaskReviewRoute, err, strings.TrimSpace(string(output)))
	}
	untracked, err := untrackedAssignmentDiff(ctx, repository)
	if err != nil {
		return "", err
	}
	output = append(output, untracked...)
	if strings.TrimSpace(string(output)) == "" {
		return "(no working-tree changes)", nil
	}
	return string(output), nil
}

func assignmentDiff(ctx context.Context, repository, base string) (string, error) {
	return GitWorkspaceControl{}.AssignmentDiff(ctx, repository, base)
}

// untrackedAssignmentDiff adds every non-ignored untracked file to the
// controller-built review packet. git diff <base> cannot see those files, but
// generated output and newly introduced sources are part of an assignment's
// observable state. Names come from Git's NUL-delimited output and are
// rejected if they are not safe repository-relative paths before being handed
// back to Git.
func untrackedAssignmentDiff(ctx context.Context, repository string) ([]byte, error) {
	listed := exec.CommandContext(ctx, "git", "-C", repository, "ls-files", "--others", "--exclude-standard", "-z")
	paths, err := listed.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: list untracked assignment files: %v", ErrInvalidTaskReviewRoute, err)
	}
	var diff []byte
	for _, raw := range strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00") {
		if raw == "" {
			continue
		}
		path, err := safeAssignmentDiffPath(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: untracked assignment path: %v", ErrInvalidTaskReviewRoute, err)
		}
		command := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--no-index", "--", "/dev/null", path)
		output, err := command.CombinedOutput()
		// git diff --no-index uses exit code 1 for a normal difference.
		if err != nil {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				return nil, fmt.Errorf("%w: capture untracked file %q: %v: %s", ErrInvalidTaskReviewRoute, path, err, strings.TrimSpace(string(output)))
			}
		}
		diff = append(diff, output...)
	}
	return diff, nil
}

func safeAssignmentDiffPath(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", errors.New("path must be repository-relative")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes repository")
	}
	return filepath.ToSlash(clean), nil
}

func modifiedExistingTests(diff string) bool {
	for _, file := range strings.Split(diff, "diff --git ") {
		if !strings.Contains(file, "_test.go") || strings.Contains(file, "new file mode") {
			continue
		}
		for _, line := range strings.Split(file, "\n") {
			if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
				return true
			}
		}
	}
	return false
}

func reviewPassExplainsTestChange(response AgentResponse) bool {
	if response.Message == nil {
		return false
	}
	text := strings.ToLower(*response.Message + " " + strings.Join(response.References, " "))
	hasTest := strings.Contains(text, "test") || strings.Contains(text, "тест")
	hasBasis := strings.Contains(text, "brief") || strings.Contains(text, "requirement") || strings.Contains(text, "бриф") || strings.Contains(text, "требован")
	hasCoverage := strings.Contains(text, "cover") || strings.Contains(text, "coverage") || strings.Contains(text, "покрыт")
	return hasTest && hasBasis && hasCoverage
}

func renderTaskReviewPacket(rules RulesIndex, diff, checks, previous string) string {
	var text strings.Builder
	renderRulesIndex(&text, rules)
	fmt.Fprintf(&text, "\n# Entire current assignment diff\n\n%s\n\n# Required check evidence\n\n%s\n\n# Previous review discussion\n\n%s", diff, checks, nonEmptyReviewDiscussion(previous))
	return text.String()
}

func currentRequiredCheckEvidence(journal *runstore.Run, run *implementationstate.Run, assignmentID implementationstate.AssignmentID) (string, error) {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil {
		return "", fmt.Errorf("%w: unknown assignment", ErrInvalidTaskReviewRoute)
	}
	basis := implementationstate.AcceptanceBasis{Specification: run.Identity.Specification, Configuration: run.Identity.Configuration}
	briefID := assignment.Briefs[len(assignment.Briefs)-1].ID
	for index := len(assignment.Results) - 1; index >= 0; index-- {
		result := assignment.Results[index]
		for operationIndex := range assignment.Operations {
			operation := assignment.Operations[operationIndex]
			if operation.ID != result.OperationID || operation.Counter != implementationstate.CycleCounterMandatoryChecks || operation.BriefID != briefID || operation.Basis != basis || result.Status != implementationstate.ResultSucceeded || result.State != run.CurrentState || result.Basis != basis {
				continue
			}
			if len(result.Evidence) == 0 {
				return "", fmt.Errorf("%w: current required result lacks evidence", ErrInvalidTaskReviewRoute)
			}
			data, err := journal.Read(result.Evidence[0])
			if err != nil {
				return "", fmt.Errorf("%w: read required check evidence: %v", ErrInvalidTaskReviewRoute, err)
			}
			return string(data), nil
		}
	}
	return "", fmt.Errorf("%w: current required check evidence is absent", ErrInvalidTaskReviewRoute)
}

func validateTaskReviewerResponse(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, response AgentResponse) error {
	if response.Kind != ResponseReviewPassed && response.Kind != ResponseChangesRequested {
		return fmt.Errorf("%w: reviewer must pass or request changes", ErrInvalidTaskReviewRoute)
	}
	if response.Binding.AssignmentID != assignmentID || response.Binding.RunID != run.Identity.ID {
		return fmt.Errorf("%w: reviewer response is not bound to assignment", ErrInvalidTaskReviewRoute)
	}
	if response.Kind == ResponseChangesRequested {
		if len(response.FindingIDs) == 0 || len(response.FindingIDs) != len(response.Findings) || len(response.FindingIDs) != len(response.FindingDecisions) || len(response.FindingIDs) != len(response.FindingReasons) || len(response.FindingIDs) != len(response.Locations) || len(response.FindingIDs) != len(response.Bases) || len(response.FindingIDs) != len(response.ExpectedResults) {
			return fmt.Errorf("%w: reviewer finding decision fields have inconsistent lengths", ErrInvalidTaskReviewRoute)
		}
		previous := latestReviewFindings(assignmentForReview(run, assignmentID))
		seen := map[string]bool{}
		for index, id := range response.FindingIDs {
			decision := implementationstate.FindingStatus(response.FindingDecisions[index])
			if seen[id] || !decisionValidForFinding(decision, previous[id], previous[id].ID != "") || !blockingFindingBasis(response.Bases[index]) {
				return fmt.Errorf("%w: blocking finding %q lacks a defect, explicit requirement, or rule basis", ErrInvalidTaskReviewRoute, id)
			}
			if strings.TrimSpace(response.FindingReasons[index]) == "" {
				return fmt.Errorf("%w: finding %q lacks a reviewer decision reason", ErrInvalidTaskReviewRoute, id)
			}
			seen[id] = true
		}
		for id, finding := range previous {
			if (finding.Status == implementationstate.FindingOpen || finding.Status == implementationstate.FindingRetained) && !seen[id] {
				return fmt.Errorf("%w: reviewer omitted an unresolved finding %q", ErrInvalidTaskReviewRoute, id)
			}
		}
		return nil
	}
	return nil
}

func decisionValidForFinding(decision implementationstate.FindingStatus, previous implementationstate.TaskReviewFinding, exists bool) bool {
	if !decisionValid(decision) {
		return false
	}
	if exists {
		return decision == implementationstate.FindingResolved || decision == implementationstate.FindingRetained
	}
	return decision == implementationstate.FindingOpen
}

func decisionValid(decision implementationstate.FindingStatus) bool {
	return decision == implementationstate.FindingOpen || decision == implementationstate.FindingResolved || decision == implementationstate.FindingRetained
}

func blockingFindingBasis(basis string) bool {
	value := strings.ToLower(strings.TrimSpace(basis))
	if strings.Contains(value, "preference") || strings.Contains(value, "предпочт") {
		return false
	}
	for _, marker := range []string{"defect", "bug", "brief", "requirement", "rule", "дефект", "требован", "правил"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func taskReviewRecord(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, resultID implementationstate.ResultID, response AgentResponse) (implementationstate.TaskReviewRecord, implementationstate.ResultStatus, error) {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil {
		return implementationstate.TaskReviewRecord{}, "", fmt.Errorf("%w: unknown assignment", ErrInvalidTaskReviewRoute)
	}
	previous := latestReviewFindings(assignment)
	record := implementationstate.TaskReviewRecord{OperationID: operationID, ResultID: resultID, Round: uint64(len(assignment.TaskReviews) + 1), State: run.CurrentState, Disputes: cloneTaskReviewDisputes(assignment.PendingTaskReviewDisputes)}
	if response.Kind == ResponseReviewPassed {
		for _, finding := range previous {
			finding.Status, finding.Resolution = implementationstate.FindingResolved, "reviewer confirmed the current diff resolves the finding"
			record.Findings = append(record.Findings, finding)
		}
		record.Discussion = *response.Message
		return record, implementationstate.ResultSucceeded, nil
	}
	for index, id := range response.FindingIDs {
		finding := implementationstate.TaskReviewFinding{ID: id, Problem: response.Findings[index], Location: response.Locations[index], Basis: response.Bases[index], ExpectedResult: response.ExpectedResults[index], Status: implementationstate.FindingStatus(response.FindingDecisions[index]), Resolution: response.FindingReasons[index]}
		if prior, found := previous[id]; found {
			// The reviewer must retain the original identity and description when
			// deciding an existing finding; its decision reason is the mutable
			// part of the discussion.
			finding.Problem, finding.Location, finding.Basis, finding.ExpectedResult = prior.Problem, prior.Location, prior.Basis, prior.ExpectedResult
		}
		record.Findings = append(record.Findings, finding)
	}
	record.Discussion = "reviewer recorded explicit per-finding decisions"
	return record, implementationstate.ResultFailed, nil
}

func cloneTaskReviewDisputes(disputes []implementationstate.TaskReviewDispute) []implementationstate.TaskReviewDispute {
	result := make([]implementationstate.TaskReviewDispute, len(disputes))
	for index, dispute := range disputes {
		result[index] = implementationstate.TaskReviewDispute{FindingID: dispute.FindingID, Arguments: dispute.Arguments, References: slices.Clone(dispute.References)}
	}
	return result
}

func latestReviewFindings(assignment *implementationstate.Assignment) map[string]implementationstate.TaskReviewFinding {
	if assignment == nil || len(assignment.TaskReviews) == 0 {
		return map[string]implementationstate.TaskReviewFinding{}
	}
	findings := make(map[string]implementationstate.TaskReviewFinding)
	for _, finding := range assignment.TaskReviews[len(assignment.TaskReviews)-1].Findings {
		findings[finding.ID] = finding
	}
	return findings
}

func validateExecutorDispute(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, response AgentResponse) error {
	if response.Kind != ResponseReviewDisputed || response.Binding.RunID != run.Identity.ID || response.Binding.AssignmentID != assignmentID || len(response.FindingIDs) != 1 || response.Message == nil || strings.TrimSpace(*response.Message) == "" || len(response.References) == 0 {
		return fmt.Errorf("%w: invalid executor review dispute", ErrInvalidTaskReviewRoute)
	}
	for _, finding := range run.OpenTaskReviewFindings(assignmentID) {
		if finding.ID == response.FindingIDs[0] {
			return nil
		}
	}
	return fmt.Errorf("%w: executor cannot dispute a closed or unknown finding", ErrInvalidTaskReviewRoute)
}

func publishTaskReviewEvidence(journal *runstore.Run, resultID implementationstate.ResultID, record implementationstate.TaskReviewRecord) (implementationstate.EvidenceRef, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return implementationstate.EvidenceRef{}, err
	}
	return journal.Publish(implementationstate.EvidenceID(string(resultID)+"-discussion"), data)
}

func renderTaskReviewHistory(run *implementationstate.Run, assignmentID implementationstate.AssignmentID) string {
	assignment := assignmentForReview(run, assignmentID)
	if assignment == nil || len(assignment.TaskReviews) == 0 {
		return ""
	}
	var text strings.Builder
	for _, review := range assignment.TaskReviews {
		fmt.Fprintf(&text, "## Review round %d\n\n%s\n", review.Round, review.Discussion)
		for _, finding := range review.Findings {
			fmt.Fprintf(&text, "\n### Finding %s [%s]\n\nProblem: %s\n\nLocation: %s\n\nBasis: %s\n\nExpected result: %s\n\nReviewer decision reason: %s\n", finding.ID, finding.Status, finding.Problem, finding.Location, finding.Basis, finding.ExpectedResult, finding.Resolution)
		}
		for _, dispute := range review.Disputes {
			fmt.Fprintf(&text, "\n### Executor dispute for %s\n\nArguments: %s\n\nReferences:\n%s\n", dispute.FindingID, dispute.Arguments, markdownList(dispute.References))
		}
	}
	return strings.TrimSpace(text.String())
}

func taskReviewChangesMessage(record implementationstate.TaskReviewRecord) string {
	var text strings.Builder
	text.WriteString("# Task reviewer findings\n")
	for _, finding := range record.Findings {
		if finding.Status != implementationstate.FindingOpen && finding.Status != implementationstate.FindingRetained {
			continue
		}
		fmt.Fprintf(&text, "\n## %s\n\nProblem: %s\n\nLocation: %s\n\nBasis: %s\n\nExpected result: %s\n", finding.ID, finding.Problem, finding.Location, finding.Basis, finding.ExpectedResult)
	}
	text.WriteString("\nDo not close these findings yourself. Fix the implementation and return implementation_ready or checks_requested, or dispute exactly one finding with supporting evidence.\n")
	return text.String()
}

func markdownList(values []string) string {
	if len(values) == 0 {
		return "- none"
	}
	return "- " + strings.Join(slices.Clone(values), "\n- ")
}

func sameTaskReviewBinding(left, right ResponseBinding) bool {
	return left.RunID == right.RunID && left.AssignmentID == right.AssignmentID && left.BriefID == right.BriefID && left.Specification == right.Specification && left.Configuration == right.Configuration && left.TaskList == right.TaskList
}
