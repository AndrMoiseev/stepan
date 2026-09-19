package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

const executionBlockedPauseReason = "execution_blocked: cannot safely attribute or restore agent file changes"

// AgentRole names a role whose workspace writes are observed by the
// controller. The policy, rather than this name, defines writable paths so
// future roles do not accidentally inherit executor authority.
type AgentRole string

const (
	AgentRoleOrchestrator AgentRole = "orchestrator"
	// AgentRoleBriefer is read-only. Keeping it distinct makes an attempted
	// write attributable to the role that selected the assignment rather than
	// to Explorer.
	AgentRoleBriefer  AgentRole = "briefer"
	AgentRoleExecutor AgentRole = "executor"
	// AgentRoleTaskReviewer is deliberately read-only; retaining a distinct
	// role makes a prohibited write attributable to the reviewer rather than
	// to the executor whose work it is inspecting.
	AgentRoleTaskReviewer AgentRole = "task_reviewer"
	// AgentRoleFinalReviewer is read-only and has its own identity so a final
	// review cannot inherit either an executor's writable scope or a
	// task-reviewer's assignment-scoped attribution.
	AgentRoleFinalReviewer AgentRole = "final_reviewer"
	// AgentRoleExplorer is read-only. It is retained in the call-policy
	// record so a detected write is attributed to Explorer rather than another
	// controller role.
	AgentRoleExplorer AgentRole = "explorer"
)

// AgentCallPolicy is the post-call write boundary for one agent invocation.
// Exact paths and roots are repository-relative. Protected paths always win.
// AllowUnprotected is useful for the executor: it permits normal code work
// while retaining a concise list of controller-owned files it cannot change.
type AgentCallPolicy struct {
	Role             AgentRole
	CallID           string
	AllowedPaths     []string
	AllowedRoots     []string
	ProtectedPaths   []string
	AllowUnprotected bool
}

// CallDisposition tells the controller whether the returned agent response
// may be consumed. A restored violation rejects that response and requires a
// technical retry of the same operation; it does not create another semantic
// round. ExecutionBlocked requires a user decision before dependent work.
type CallDisposition string

const (
	CallAccepted         CallDisposition = "accepted"
	CallRetry            CallDisposition = "retry"
	CallExecutionBlocked CallDisposition = "execution_blocked"
)

// AgentCallOutcome keeps the post-call snapshot that the controller must use
// as the expected state for its next operation. InvocationError is retained
// separately because a call can both fail and leave prohibited file edits.
type AgentCallOutcome struct {
	Disposition     CallDisposition
	Snapshot        git.Snapshot
	InvocationError error
	Diagnostic      string
	ViolationPaths  []string
}

// ObserveAgentCall captures one pre-call state, observes only this call's
// delta, and restores only confirmed prohibited file edits. It never performs
// a broad Git reset. The caller owns durable technical-attempt reservation and
// retries when Disposition is CallRetry.
func ObserveAgentCall(ctx context.Context, repository string, policy AgentCallPolicy, run *implementationstate.Run, journal *runstore.Run, invoke func() error) (AgentCallOutcome, error) {
	return observeAgentCall(ctx, GitWorkspaceControl{}, repository, policy, run, journal, invoke)
}

func observeAgentCall(ctx context.Context, workspace WorkspaceControl, repository string, policy AgentCallPolicy, run *implementationstate.Run, journal *runstore.Run, invoke func() error) (AgentCallOutcome, error) {
	if run == nil || journal == nil || invoke == nil {
		return AgentCallOutcome{}, errors.New("observe agent call requires run, violation journal, and invocation")
	}
	if run.Status != implementationstate.RunActive {
		return AgentCallOutcome{}, fmt.Errorf("observe agent call requires an active run")
	}
	policy, err := normalizeAgentCallPolicy(policy)
	if err != nil {
		return AgentCallOutcome{}, err
	}
	before, err := workspace.Capture(ctx, repository)
	if err != nil {
		return blockAgentCall(run, fmt.Errorf("capture before agent call: %w", err))
	}
	invocationErr := invoke()
	after, err := workspace.Capture(ctx, repository)
	if err != nil {
		return blockAgentCall(run, fmt.Errorf("capture after agent call: %w", err))
	}
	difference, err := workspace.Diff(ctx, repository, before, after)
	if err != nil {
		return blockAgentCall(run, fmt.Errorf("compare agent call changes: %w", err))
	}
	// HEAD, index, and populated-submodule changes cannot safely be attributed
	// to a role's file write. They are never repaired by a checkout/reset.
	if difference.HeadChanged || difference.HeadRefChanged || difference.IndexChanged || difference.SubmodulesChanged {
		return blockAgentCall(run, fmt.Errorf("agent call changed Git control state: %#v", difference))
	}
	violations, constraint := prohibitedPaths(difference.Paths, policy)
	if len(violations) == 0 {
		return AgentCallOutcome{Disposition: CallAccepted, Snapshot: after, InvocationError: invocationErr}, nil
	}
	restored, restoreErr := workspace.RestorePaths(ctx, repository, before, after, violations)
	result := "restored"
	if restoreErr != nil {
		result = "failed: " + restoreErr.Error()
	}
	journalErr := journal.AppendViolation(runstore.ViolationRecord{
		Role: policy.Role.String(), CallID: policy.CallID, Paths: violations,
		ViolatedConstraint: constraint, RestorationResult: result,
	})
	if restoreErr != nil || journalErr != nil {
		return blockAgentCall(run, errors.Join(restoreErr, journalErr))
	}
	diagnostic := fmt.Sprintf("agent %s call %s changed prohibited paths %s; those edits were restored and the response was rejected", policy.Role, policy.CallID, strings.Join(violations, ", "))
	return AgentCallOutcome{
		Disposition: CallRetry, Snapshot: restored, InvocationError: invocationErr,
		Diagnostic: diagnostic, ViolationPaths: append([]string(nil), violations...),
	}, nil
}

func (r AgentRole) String() string { return string(r) }

func normalizeAgentCallPolicy(policy AgentCallPolicy) (AgentCallPolicy, error) {
	if policy.Role == "" || strings.TrimSpace(policy.CallID) == "" {
		return AgentCallPolicy{}, errors.New("agent call policy requires role and call ID")
	}
	if policy.Role != AgentRoleOrchestrator && policy.Role != AgentRoleBriefer && policy.Role != AgentRoleExecutor && policy.Role != AgentRoleTaskReviewer && policy.Role != AgentRoleFinalReviewer && policy.Role != AgentRoleExplorer {
		return AgentCallPolicy{}, fmt.Errorf("agent call role %q cannot write the workspace", policy.Role)
	}
	if (policy.Role == AgentRoleOrchestrator || policy.Role == AgentRoleBriefer || policy.Role == AgentRoleTaskReviewer || policy.Role == AgentRoleFinalReviewer || policy.Role == AgentRoleExplorer) && policy.AllowUnprotected {
		return AgentCallPolicy{}, errors.New("read-only agent role must use an explicit path boundary")
	}
	collections := []*[]string{&policy.AllowedPaths, &policy.AllowedRoots, &policy.ProtectedPaths}
	for _, collection := range collections {
		normalized := make([]string, 0, len(*collection))
		for _, path := range *collection {
			path, err := validateCallPath(path)
			if err != nil {
				return AgentCallPolicy{}, err
			}
			normalized = append(normalized, path)
		}
		*collection = normalized
	}
	return policy, nil
}

func prohibitedPaths(paths []string, policy AgentCallPolicy) ([]string, string) {
	violations := make([]string, 0)
	constraint := "role may write only allowed paths"
	for _, path := range paths {
		if pathIn(policy.ProtectedPaths, path, false) {
			constraint = "protected path may be changed only by the controller"
			violations = append(violations, path)
			continue
		}
		if policy.AllowUnprotected || pathIn(policy.AllowedPaths, path, false) || pathIn(policy.AllowedRoots, path, true) {
			continue
		}
		violations = append(violations, path)
	}
	slices.Sort(violations)
	return violations, constraint
}

func pathIn(candidates []string, path string, root bool) bool {
	for _, candidate := range candidates {
		if candidate == path || root && strings.HasPrefix(path, candidate+"/") {
			return true
		}
	}
	return false
}

func validateCallPath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", fmt.Errorf("invalid agent call path %q", path)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid agent call path %q", path)
	}
	return clean, nil
}

func blockAgentCall(run *implementationstate.Run, cause error) (AgentCallOutcome, error) {
	if run.Status == implementationstate.RunActive {
		block := implementationstate.ExecutionBlock{
			BlockedAction:      "cannot safely attribute or restore agent file changes",
			Diagnostic:         cause.Error(),
			Attempts:           []string{"captured the agent-call workspace delta and could not safely recover it"},
			RequiredUserAction: "inspect and repair the working copy, then explicitly resume or close the run",
		}
		if err := run.PauseExecutionBlocked(block); err != nil {
			return AgentCallOutcome{}, errors.Join(cause, fmt.Errorf("pause execution-blocked run: %w", err))
		}
	}
	return AgentCallOutcome{Disposition: CallExecutionBlocked, Diagnostic: cause.Error()}, nil
}
