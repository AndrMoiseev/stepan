package workspace

import (
	"context"

	"github.com/AndrMoiseev/stepan/internal/git"
)

// CommitObservation contains facts read from the commit Git actually made.
// The controller completes machine task state only when every fact agrees with
// the persisted intent.
type CommitObservation struct {
	CommitID     string
	ParentCommit string
	Tree         string
	Message      string
	// Worktree is captured after Git has completed the commit and all normal
	// hooks. It is required to distinguish a matching commit from a hook that
	// left additional or differently staged work behind.
	Worktree git.Snapshot
}

// Committer is the narrow mutation seam for one assignment commit. The
// production implementation stages the current working copy (accepted code
// plus the orchestrator's informational mark) and makes exactly one commit.
// Intent preparation deliberately is not a method here: all external Git work
// must happen after the full intent reaches durable state.
type Committer interface {
	Commit(context.Context, string, string) (CommitObservation, error)
}

// CommitObserver is the read-only Git seam used before retrying a durable
// pending commit operation.
type CommitObserver interface {
	Observe(context.Context, string) (CommitObservation, error)
}
