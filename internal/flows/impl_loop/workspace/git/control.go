package git

import (
	"context"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

// Control is the production adapter backed by the git
// module and real Git working-copy semantics.
type Control struct{}

var (
	_ workspace.Control    = Control{}
	_ workspace.Repository = Control{}
)

func (Control) Capture(ctx context.Context, repository string) (git.Snapshot, error) {
	return git.Capture(ctx, repository)
}

func (Control) Diff(ctx context.Context, repository string, before, after git.Snapshot) (git.Difference, error) {
	return git.Diff(ctx, repository, before, after)
}

// Compare is an optional narrow capability used by resume to distinguish a
// rules-only edit from a code change. It is intentionally not part of
// workspace.Control: ordinary callers need no path comparison and lightweight
// test controls remain small.
func (Control) Compare(ctx context.Context, repository string, before, after git.Snapshot) ([]string, error) {
	return git.Compare(ctx, repository, before, after)
}

func (Control) RestorePaths(ctx context.Context, repository string, before, current git.Snapshot, paths []string) (git.Snapshot, error) {
	return git.RestorePaths(ctx, repository, before, current, paths)
}

func (Control) EnsureUnchanged(ctx context.Context, repository string, expected git.Snapshot) error {
	return git.EnsureUnchanged(ctx, repository, expected)
}

func (Control) AssignmentDiff(ctx context.Context, repository, base string) (string, error) {
	return gitAssignmentDiff(ctx, repository, base)
}

func (Control) FindRoot(ctx context.Context, start string) (string, error) {
	return gitFindRoot(ctx, start)
}

func (control Control) ValidateNewStart(ctx context.Context, start string, configuration setting.Configuration) (workspace.Identity, error) {
	return gitValidateNewStart(ctx, control, start, configuration)
}
