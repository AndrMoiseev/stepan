package impl_loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
	"github.com/AndrMoiseev/stepan/internal/git"
)

// ErrAssignmentCommit identifies an invalid controller-owned commit step.
var ErrAssignmentCommit = errors.New("invalid assignment commit")

// ErrPendingCommitInactive prevents a durable intent from bypassing the
// explicit active-run boundary. A paused run must be resumed by the user and
// a closed run is terminal before either can cause another Git mutation.
var ErrPendingCommitInactive = errors.New("pending Git commit requires an active run")

// ErrCommitReacceptanceRequired reports a commit made by Git whose resulting
// content is no longer the content that passed acceptance. The commit is
// deliberately retained; the assignment is reopened so the changed working
// state can pass the ordinary acceptance cycle and be fixed by a new commit.
var ErrCommitReacceptanceRequired = errors.New("commit result requires repeated acceptance")

// CommitPreparation is the already-observed Git state for the one local
// commit. It is captured by the controller's existing post-operation snapshot
// before this commit step, then persisted before the first Git mutation.
type CommitPreparation struct {
	ParentCommit string
	Tree         string
}

// CommitPreparationFromSnapshot derives the commit's expected parent and tree
// from a snapshot the controller has already captured. It performs no Git or
// filesystem action, preserving the durable-before-mutation boundary.
func CommitPreparationFromSnapshot(snapshot git.Snapshot) (CommitPreparation, error) {
	preparation := CommitPreparation{ParentCommit: strings.TrimSpace(snapshot.HeadOID), Tree: strings.TrimSpace(snapshot.TreeOID)}
	if preparation.ParentCommit == "" || preparation.Tree == "" {
		return CommitPreparation{}, fmt.Errorf("%w: captured snapshot lacks parent or tree", ErrAssignmentCommit)
	}
	return preparation, nil
}

// CommitAcceptedAssignmentInput is owned entirely by the controller. Response
// is the current implementation_ready response: a later repair replaces its
// message naturally, and no agent is asked merely to draft a commit message.
type CommitAcceptedAssignmentInput struct {
	Run          *implstate.Run
	StateStore   *runstore.StateStore
	Journal      *runstore.Run
	Repository   string
	AssignmentID implstate.AssignmentID
	OperationID  implstate.OperationID
	Response     AgentResponse
	// Preparation is the post-reflection workspace snapshot turned into Git
	// facts by CommitPreparationFromSnapshot. It is controller evidence, not
	// an agent suggestion and it is recorded before Commit is called.
	Preparation CommitPreparation
	Control     workcopy.Committer
	// Observer is used only when a durable commit intent already exists. It is
	// normally gitworkspace.Control; the seam keeps restart reconciliation
	// independently testable without changing the mutation contract.
	Observer workcopy.CommitObserver
}

type CommitAcceptedAssignmentResult struct {
	Intent               implstate.CommitIntent
	Commit               implstate.CommitEvidence
	ReacceptanceRequired bool
}

// CommitAcceptedAssignment stages accepted code and its informational
// tasks.md mark together, persists the exact intent, then creates one local
// commit. It never calls an agent and it never infers completion from Markdown.
func CommitAcceptedAssignment(ctx context.Context, input CommitAcceptedAssignmentInput) (CommitAcceptedAssignmentResult, error) {
	if err := validateCommitAcceptedAssignmentInput(input); err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	reconciled, ok, err := reusableReconciledCommit(input)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("verify retained hook-created commit: %w", err))
	}
	if ok {
		return finalizeReconciledCommit(ctx, input, reconciled)
	}
	intent, pending := pendingCommitIntent(input.Run, input.AssignmentID)
	if pending {
		recovery, err := ReconcilePendingCommit(ctx, ReconcilePendingCommitInput{
			Run: input.Run, StateStore: input.StateStore, Repository: input.Repository, AssignmentID: input.AssignmentID, Observer: input.Observer,
		})
		if err != nil {
			return CommitAcceptedAssignmentResult{Intent: intent}, err
		}
		if recovery.Adopted {
			return CommitAcceptedAssignmentResult{Intent: recovery.Intent, Commit: recovery.Commit}, nil
		}
		if !recovery.Retry {
			return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: pending commit reconciliation made no decision", ErrAssignmentCommit)
		}
	} else {
		beforeIntent, err := cloneCommitRun(input.Run)
		if err != nil {
			return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: checkpoint run before commit intent: %v", ErrAssignmentCommit, err)
		}
		message, err := messageForImplementationCommit(input.Run, input.AssignmentID, input.OperationID, input.Response)
		if err != nil {
			return CommitAcceptedAssignmentResult{}, err
		}
		intent = implstate.CommitIntent{OperationID: input.OperationID, ParentCommit: input.Preparation.ParentCommit, Tree: input.Preparation.Tree, Message: message}
		if err := input.Run.SetPendingCommitIntent(input.AssignmentID, intent); err != nil {
			return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: record commit intent: %v", ErrAssignmentCommit, err)
		}
		if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
			restoreUndurableCommitRun(input.Run, beforeIntent, event)
			return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: persist commit intent: %v", ErrAssignmentCommit, err)
		}
	}
	control := input.Control
	if control == nil {
		control = gitworkspace.Control{}
	}
	if err := requireActiveCommitRun(input.Run); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: %w", ErrAssignmentCommit, err)
	}
	observed, err := control.Commit(ctx, input.Repository, intent.Message)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("git commit was refused or failed: %w", err))
	}
	if canReacceptChangedCommit(intent, observed) {
		return reconcileChangedCommit(ctx, input, intent, observed)
	}
	if !commitMatchesIntent(intent, observed) || !commitHasExpectedTrailers(input.Run, input.AssignmentID, observed.Message) || !worktreeMatchesCommit(observed) {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("actual commit or working copy does not match accepted intent: commit=%q parent=%q tree=%q", observed.CommitID, observed.ParentCommit, observed.Tree))
	}
	commit := implstate.CommitEvidence{OperationID: intent.OperationID, CommitID: observed.CommitID, ParentCommit: observed.ParentCommit, Tree: observed.Tree, Message: observed.Message, State: input.Run.CurrentState, Basis: implstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}}
	beforeCompletion, err := cloneCommitRun(input.Run)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: checkpoint run before completed commit: %v", ErrAssignmentCommit, err)
	}
	if err := input.Run.CommitAssignment(input.AssignmentID, commit); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: verify Git commit: %v", ErrAssignmentCommit, err)
	}
	if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		restoreUndurableCommitRun(input.Run, beforeCompletion, event)
		return CommitAcceptedAssignmentResult{Intent: intent, Commit: commit}, fmt.Errorf("%w: persist completed assignment: %v", ErrAssignmentCommit, err)
	}
	return CommitAcceptedAssignmentResult{Intent: intent, Commit: commit}, nil
}

func pendingCommitIntent(run *implstate.Run, assignmentID implstate.AssignmentID) (implstate.CommitIntent, bool) {
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil {
		return implstate.CommitIntent{}, false
	}
	intent := assignment.Acceptance.PendingCommit
	return intent, intent.OperationID != "" && intent.ParentCommit != "" && intent.Tree != "" && strings.TrimSpace(intent.Message) != ""
}

func commitMatchesIntent(intent implstate.CommitIntent, observed workcopy.CommitObservation) bool {
	return strings.TrimSpace(observed.CommitID) != "" && observed.ParentCommit == intent.ParentCommit && observed.Tree == intent.Tree && observed.Message == intent.Message
}

func worktreeMatchesCommit(observed workcopy.CommitObservation) bool {
	return observed.Worktree.HeadOID == observed.CommitID && observed.Worktree.TreeOID == observed.Tree
}

func commitHasExpectedTrailers(run *implstate.Run, assignmentID implstate.AssignmentID, message string) bool {
	if run == nil {
		return false
	}
	return hasExactlyOneTrailer(message, "Stepan-Run", string(run.Identity.ID)) &&
		hasExactlyOneTrailer(message, "Stepan-Assignment", string(assignmentID)) &&
		hasExactlyOneTrailer(message, "Stepan-Operation", string(pendingOperationID(run, assignmentID)))
}

func pendingOperationID(run *implstate.Run, assignmentID implstate.AssignmentID) implstate.OperationID {
	intent, ok := pendingCommitIntent(run, assignmentID)
	if !ok {
		return ""
	}
	return intent.OperationID
}

func hasExactlyOneTrailer(message, key, value string) bool {
	expected := key + ": " + value
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if line == expected {
			count++
		}
	}
	return count == 1
}

// cloneCommitRun gives the commit boundary a reversible in-memory checkpoint.
// StateStore.Record returns an event with a sequence only after the JSONL line
// is durable. When it fails earlier, restoring this checkpoint prevents a
// caller from treating an unrecorded intent or completed assignment as state
// that recovery can rely on.
func cloneCommitRun(run *implstate.Run) (*implstate.Run, error) {
	data, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	var clone implstate.Run
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func restoreUndurableCommitRun(run, before *implstate.Run, event implstate.Event) {
	if run != nil && before != nil && event.Sequence == 0 {
		*run = *before
	}
}

// commitContentChanged identifies hook-produced source or worktree changes.
// A changed commit tree or a dirty post-hook worktree must not be completed
// using acceptance evidence for the old tree. Message-only mismatches are
// instead paused: reaccepting unchanged files could not create a corrective
// commit, but the unexpected commit remains untouched for user reconciliation.
func canReacceptChangedCommit(intent implstate.CommitIntent, observed workcopy.CommitObservation) bool {
	// Only the tree/worktree is allowed to differ for an ordinary hook. A
	// changed parent, message, or HEAD is ambiguous Git control-state drift and
	// must stay paused rather than being misclassified as safe reacceptance.
	return strings.TrimSpace(observed.CommitID) != "" && observed.ParentCommit == intent.ParentCommit && observed.Message == intent.Message && observed.Worktree.HeadOID == observed.CommitID && (observed.Tree != intent.Tree || observed.Worktree.TreeOID != observed.Tree)
}

func reconcileChangedCommit(ctx context.Context, input CommitAcceptedAssignmentInput, intent implstate.CommitIntent, observed workcopy.CommitObservation) (CommitAcceptedAssignmentResult, error) {
	if input.Journal == nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, errors.New("hook changed commit content but no run journal is available to record the actual working state"))
	}
	stateData, err := json.Marshal(observed.Worktree)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("encode hook-modified working state: %w", err))
	}
	stateID := implstate.EvidenceID(fmt.Sprintf("%s-commit-reconciliation-state", input.OperationID))
	state, err := input.Journal.Publish(stateID, stateData)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("publish hook-modified working state: %w", err))
	}
	reconciled := implstate.CommitEvidence{OperationID: input.OperationID, CommitID: observed.CommitID, ParentCommit: observed.ParentCommit, Tree: observed.Tree, Message: observed.Message, State: state, Basis: implstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}}
	if err := input.Run.RecordReconciledCommit(input.AssignmentID, reconciled); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("record hook-created commit for reconciliation: %w", err))
	}
	if err := input.Run.ReopenAssignment(input.AssignmentID, state); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, pauseCommitAwaitingRetry(ctx, input, fmt.Errorf("reopen assignment for hook-modified content: %w", err))
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent, ReacceptanceRequired: true}, fmt.Errorf("%w: persist hook-modified assignment state: %v", ErrAssignmentCommit, err)
	}
	return CommitAcceptedAssignmentResult{Intent: intent, ReacceptanceRequired: true}, fmt.Errorf("%w: actual tree %q or working copy differed from accepted tree %q", ErrCommitReacceptanceRequired, observed.Tree, intent.Tree)
}

// reusableReconciledCommit recognizes a hook-created commit only after a new
// acceptance proves exactly the state published during reconciliation. The
// current snapshot-derived preparation additionally proves that no later edit
// requires a corrective child commit.
func reusableReconciledCommit(input CommitAcceptedAssignmentInput) (implstate.CommitEvidence, bool, error) {
	assignment := assignmentByID(input.Run, input.AssignmentID)
	if assignment == nil || assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil {
		return implstate.CommitEvidence{}, false, nil
	}
	if len(assignment.ReconciledCommits) != 0 && input.Journal == nil {
		return implstate.CommitEvidence{}, false, errors.New("run journal is required to verify retained hook-created commit")
	}
	for index := len(assignment.ReconciledCommits) - 1; index >= 0; index-- {
		commit := assignment.ReconciledCommits[index]
		if commit.Basis != assignment.Acceptance.Basis || commit.CommitID != input.Preparation.ParentCommit || commit.Tree != input.Preparation.Tree || commit.State.Digest != assignment.Acceptance.State.Digest {
			continue
		}
		// Different evidence IDs are expected after a fresh check publishes the
		// same snapshot. Verify both immutable artifacts, then compare their
		// content identity (digest), not their transport IDs.
		if err := input.Journal.VerifyReference(commit.State); err != nil {
			return implstate.CommitEvidence{}, false, fmt.Errorf("verify reconciled state %q: %w", commit.State.ID, err)
		}
		if err := input.Journal.VerifyReference(assignment.Acceptance.State); err != nil {
			return implstate.CommitEvidence{}, false, fmt.Errorf("verify accepted state %q: %w", assignment.Acceptance.State.ID, err)
		}
		return commit, true, nil
	}
	return implstate.CommitEvidence{}, false, nil
}

func finalizeReconciledCommit(ctx context.Context, input CommitAcceptedAssignmentInput, commit implstate.CommitEvidence) (CommitAcceptedAssignmentResult, error) {
	intent := implstate.CommitIntent{OperationID: commit.OperationID, ParentCommit: commit.ParentCommit, Tree: commit.Tree, Message: commit.Message}
	assignment := assignmentByID(input.Run, input.AssignmentID)
	if assignment == nil || assignment.Acceptance == nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: accepted assignment disappeared before reconciled completion", ErrAssignmentCommit)
	}
	// The retained observation remains in ReconciledCommits with its original
	// artifact ID. CommitAssignment records the newly accepted equivalent state
	// so its exact-state invariant remains true even when fresh checks used a
	// new evidence transport ID for identical snapshot content.
	completed := commit
	completed.State = assignment.Acceptance.State
	completed.Basis = assignment.Acceptance.Basis
	beforeIntent, err := cloneCommitRun(input.Run)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: checkpoint run before reconciled commit intent: %v", ErrAssignmentCommit, err)
	}
	if err := input.Run.SetPendingCommitIntent(input.AssignmentID, intent); err != nil {
		return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: record reconciled commit intent: %v", ErrAssignmentCommit, err)
	}
	if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		restoreUndurableCommitRun(input.Run, beforeIntent, event)
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: persist reconciled commit intent: %v", ErrAssignmentCommit, err)
	}
	beforeCompletion, err := cloneCommitRun(input.Run)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: checkpoint run before reconciled completion: %v", ErrAssignmentCommit, err)
	}
	if err := input.Run.CommitAssignment(input.AssignmentID, completed); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: complete reconciled commit: %v", ErrAssignmentCommit, err)
	}
	if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		restoreUndurableCommitRun(input.Run, beforeCompletion, event)
		return CommitAcceptedAssignmentResult{Intent: intent, Commit: completed}, fmt.Errorf("%w: persist completed reconciled assignment: %v", ErrAssignmentCommit, err)
	}
	return CommitAcceptedAssignmentResult{Intent: intent, Commit: completed}, nil
}

func pauseCommitAwaitingRetry(ctx context.Context, input CommitAcceptedAssignmentInput, cause error) error {
	beforePause, checkpointErr := cloneCommitRun(input.Run)
	if checkpointErr != nil {
		return fmt.Errorf("%w: checkpoint run before pause: %v", ErrAssignmentCommit, checkpointErr)
	}
	if input.Run.Status == implstate.RunActive {
		if err := input.Run.Pause(cause.Error()); err != nil {
			return errors.Join(cause, fmt.Errorf("pause awaiting commit: %w", err))
		}
	}
	if event, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		restoreUndurableCommitRun(input.Run, beforePause, event)
		return fmt.Errorf("%w: persist paused commit state: %v", ErrAssignmentCommit, errors.Join(cause, err))
	}
	return fmt.Errorf("%w: %v", ErrAssignmentCommit, cause)
}

func validateCommitAcceptedAssignmentInput(input CommitAcceptedAssignmentInput) error {
	if input.Run == nil || input.StateStore == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.OperationID == "" || strings.TrimSpace(input.Preparation.ParentCommit) == "" || strings.TrimSpace(input.Preparation.Tree) == "" {
		return fmt.Errorf("%w: run, state store, repository, assignment, operation, and prepared Git facts are required", ErrAssignmentCommit)
	}
	if err := requireActiveCommitRun(input.Run); err != nil {
		return fmt.Errorf("%w: %w", ErrAssignmentCommit, err)
	}
	return nil
}

func requireActiveCommitRun(run *implstate.Run) error {
	if run == nil || run.Status != implstate.RunActive {
		status := implstate.RunStatus("")
		if run != nil {
			status = run.Status
		}
		return fmt.Errorf("%w: run status is %q", ErrPendingCommitInactive, status)
	}
	return nil
}

func messageForImplementationCommit(run *implstate.Run, assignmentID implstate.AssignmentID, operationID implstate.OperationID, response AgentResponse) (string, error) {
	if run == nil || response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" {
		return "", fmt.Errorf("%w: implementation_ready with a non-empty message is required", ErrAssignmentCommit)
	}
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || assignment.Status != implstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || !responseMatchesAcceptedAssignment(run, *assignment, response) {
		return "", fmt.Errorf("%w: implementation response is not bound to the accepted assignment", ErrAssignmentCommit)
	}
	body := strings.TrimSpace(*response.Message)
	return body + "\n\nStepan-Run: " + string(run.Identity.ID) + "\nStepan-Assignment: " + string(assignmentID) + "\nStepan-Operation: " + string(operationID), nil
}

func assignmentByID(run *implstate.Run, id implstate.AssignmentID) *implstate.Assignment {
	for index := range run.Assignments {
		if run.Assignments[index].ID == id {
			return &run.Assignments[index]
		}
	}
	return nil
}

func responseMatchesAcceptedAssignment(run *implstate.Run, assignment implstate.Assignment, response AgentResponse) bool {
	return response.Binding.RunID == run.Identity.ID && response.Binding.AssignmentID == assignment.ID && response.Binding.BriefID == assignment.Acceptance.BriefID && response.Binding.Specification == run.Identity.Specification && response.Binding.Configuration == run.Identity.Configuration && response.Binding.TaskList == run.Identity.TaskList && strings.TrimSpace(response.Binding.CallID) != ""
}
