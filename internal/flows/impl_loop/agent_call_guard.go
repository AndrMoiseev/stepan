package impl_loop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
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
	AgentRoleExecutor     AgentRole = "executor"
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
	Snapshot        gitsnapshot.Snapshot
	InvocationError error
	Diagnostic      string
	ViolationPaths  []string
}

// ObserveAgentCall captures one pre-call state, observes only this call's
// delta, and restores only confirmed prohibited file edits. It never performs
// a broad Git reset. The caller owns durable technical-attempt reservation and
// retries when Disposition is CallRetry.
func ObserveAgentCall(ctx context.Context, repository string, policy AgentCallPolicy, run *implementationstate.Run, journal *runstore.Run, invoke func() error) (AgentCallOutcome, error) {
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
	before, err := gitsnapshot.Capture(ctx, repository)
	if err != nil {
		return blockAgentCall(run, fmt.Errorf("capture before agent call: %w", err))
	}
	invocationErr := invoke()
	after, err := gitsnapshot.Capture(ctx, repository)
	if err != nil {
		return blockAgentCall(run, fmt.Errorf("capture after agent call: %w", err))
	}
	difference, err := gitsnapshot.Diff(ctx, repository, before, after)
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
	restored, restoreErr := gitsnapshot.RestorePaths(ctx, repository, before, after, violations)
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
	if policy.Role != AgentRoleOrchestrator && policy.Role != AgentRoleExecutor {
		return AgentCallPolicy{}, fmt.Errorf("agent call role %q cannot write the workspace", policy.Role)
	}
	if policy.Role == AgentRoleOrchestrator && policy.AllowUnprotected {
		return AgentCallPolicy{}, errors.New("orchestrator must use an explicit task-list path boundary")
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
		if err := run.Pause(executionBlockedPauseReason); err != nil {
			return AgentCallOutcome{}, errors.Join(cause, fmt.Errorf("pause execution-blocked run: %w", err))
		}
	}
	return AgentCallOutcome{Disposition: CallExecutionBlocked, Diagnostic: cause.Error()}, nil
}
