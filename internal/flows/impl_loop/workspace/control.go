package workspace

import (
	"context"
	"errors"

	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// Control is the semantic seam between implementation orchestration
// and working-copy observation. Callers depend on snapshots, differences and
// targeted restoration; only the production adapter knows that Git subprocesses
// implement those operations.
type Control interface {
	Capture(context.Context, string) (git.Snapshot, error)
	Diff(context.Context, string, git.Snapshot, git.Snapshot) (git.Difference, error)
	RestorePaths(context.Context, string, git.Snapshot, git.Snapshot, []string) (git.Snapshot, error)
	EnsureUnchanged(context.Context, string, git.Snapshot) error
	AssignmentDiff(context.Context, string, string) (string, error)
}

// Repository adds the one-time repository discovery and new-run
// preflight capabilities. Orchestration components accept the narrower
// Control interface when they do not need repository discovery.
type Repository interface {
	Control
	RootFinder
	ValidateNewStart(context.Context, string, setting.Configuration) (Identity, error)
}

var (
	// ErrNewStartDirty means a new implementation run cannot take ownership of
	// a working copy that already has uncommitted work.  It is deliberately a
	// new-run precondition: a later resume validates its saved state instead.
	ErrNewStartDirty = errors.New("implementation new start requires a clean working copy")
	ErrDetachedHEAD  = errors.New("implementation requires a checked-out branch")
	ErrMainUnknown   = errors.New("implementation main branch is unknown")
	ErrMainBranch    = errors.New("implementation cannot run on the main branch")
)

// Identity is the immutable Git identity accepted for a new
// implementation run. It contains only information observed from Git; this
// preflight never creates a worktree, branch, or checkout.
type Identity struct {
	Root       string
	Branch     string
	MainBranch string
}

// ErrAssignmentDiff indicates that the workspace review diff could not be built.
var ErrAssignmentDiff = errors.New("cannot build assignment diff")

// RootFinder resolves a path to its canonical working-copy root.
type RootFinder interface {
	FindRoot(context.Context, string) (string, error)
}
