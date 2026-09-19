package impl_loop

import (
	"context"

	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

// WorkspaceControl is the semantic seam between implementation orchestration
// and working-copy observation. Callers depend on snapshots, differences and
// targeted restoration; only the production adapter knows that Git subprocesses
// implement those operations.
type WorkspaceControl interface {
	Capture(context.Context, string) (git.Snapshot, error)
	Diff(context.Context, string, git.Snapshot, git.Snapshot) (git.Difference, error)
	RestorePaths(context.Context, string, git.Snapshot, git.Snapshot, []string) (git.Snapshot, error)
	EnsureUnchanged(context.Context, string, git.Snapshot) error
	AssignmentDiff(context.Context, string, string) (string, error)
}

// RepositoryControl adds the one-time repository discovery and new-run
// preflight capabilities. Orchestration components accept the narrower
// WorkspaceControl interface when they do not need repository discovery.
type RepositoryControl interface {
	WorkspaceControl
	FindRoot(context.Context, string) (string, error)
	ValidateNewStart(context.Context, string, implementationconfig.Configuration) (GitWorkspace, error)
}

// GitWorkspaceControl is the production adapter backed by the git
// module and real Git working-copy semantics.
type GitWorkspaceControl struct{}

var _ WorkspaceControl = GitWorkspaceControl{}
var _ RepositoryControl = GitWorkspaceControl{}

func (GitWorkspaceControl) Capture(ctx context.Context, repository string) (git.Snapshot, error) {
	return git.Capture(ctx, repository)
}

func (GitWorkspaceControl) Diff(ctx context.Context, repository string, before, after git.Snapshot) (git.Difference, error) {
	return git.Diff(ctx, repository, before, after)
}

// Compare is an optional narrow capability used by resume to distinguish a
// rules-only edit from a code change. It is intentionally not part of
// WorkspaceControl: ordinary callers need no path comparison and lightweight
// test controls remain small.
func (GitWorkspaceControl) Compare(ctx context.Context, repository string, before, after git.Snapshot) ([]string, error) {
	return git.Compare(ctx, repository, before, after)
}

func (GitWorkspaceControl) RestorePaths(ctx context.Context, repository string, before, current git.Snapshot, paths []string) (git.Snapshot, error) {
	return git.RestorePaths(ctx, repository, before, current, paths)
}

func (GitWorkspaceControl) EnsureUnchanged(ctx context.Context, repository string, expected git.Snapshot) error {
	return git.EnsureUnchanged(ctx, repository, expected)
}

func (GitWorkspaceControl) AssignmentDiff(ctx context.Context, repository, base string) (string, error) {
	return gitAssignmentDiff(ctx, repository, base)
}

func (GitWorkspaceControl) FindRoot(ctx context.Context, start string) (string, error) {
	return gitFindRoot(ctx, start)
}

func (control GitWorkspaceControl) ValidateNewStart(ctx context.Context, start string, configuration implementationconfig.Configuration) (GitWorkspace, error) {
	return gitValidateNewStart(ctx, control, start, configuration)
}

func effectiveWorkspaceControl(control WorkspaceControl) WorkspaceControl {
	if control == nil {
		return GitWorkspaceControl{}
	}
	return control
}
