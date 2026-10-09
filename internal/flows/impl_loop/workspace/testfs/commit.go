package testfs

import (
	"context"
	"fmt"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
)

var (
	_ workspace.Committer      = (*Control)(nil)
	_ workspace.CommitObserver = (*Control)(nil)
)

// Commit records an in-memory commit of all ordinary files. It does not model
// hooks, staging conflicts, branches, or Git object IDs.
func (w *Control) Commit(ctx context.Context, repository, message string) (workspace.CommitObservation, error) {
	snapshot, err := w.Capture(ctx, repository)
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	parent := snapshot.HeadOID
	snapshot.HeadOID = fmt.Sprintf("test-head-%d", w.sequence)
	snapshot.IndexHash = snapshot.TreeOID
	observation := workspace.CommitObservation{CommitID: snapshot.HeadOID, ParentCommit: parent, Tree: snapshot.TreeOID, Message: strings.TrimRight(message, "\r\n"), Worktree: snapshot}
	w.heads[repository] = observation
	w.commits[observation.CommitID] = observation.Tree
	return observation, nil
}

// Observe returns the last simulated commit together with the current files.
func (w *Control) Observe(ctx context.Context, repository string) (workspace.CommitObservation, error) {
	snapshot, err := w.Capture(ctx, repository)
	if err != nil {
		return workspace.CommitObservation{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	observed := w.heads[repository]
	observed.Worktree = snapshot
	return observed, nil
}

// AssignmentDiff describes ordinary text changes since a simulated commit.
// Its full-file hunks are deterministic; they do not emulate Git's hunk layout,
// rename detection, binary patch format, filters, or ignore rules.
func (w *Control) AssignmentDiff(ctx context.Context, repository, base string) (string, error) {
	current, err := w.Capture(ctx, repository)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	tree, ok := w.commits[base]
	before, after := w.states[tree], w.states[current.TreeOID]
	w.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: unknown base %q", workspace.ErrAssignmentDiff, base)
	}
	var result strings.Builder
	for _, path := range changedFilesystemPaths(before, after) {
		left, existed := before[path]
		right, exists := after[path]
		fmt.Fprintf(&result, "diff --git a/%s b/%s\n", path, path)
		if !existed {
			result.WriteString("new file mode 100644\n")
		}
		if !exists {
			result.WriteString("deleted file mode 100644\n")
		}
		fmt.Fprintf(&result, "--- a/%s\n+++ b/%s\n", path, path)
		for _, side := range []struct {
			prefix string
			data   []byte
		}{{"-", left.contents}, {"+", right.contents}} {
			if len(side.data) == 0 {
				continue
			}
			for _, line := range strings.Split(strings.TrimSuffix(string(side.data), "\n"), "\n") {
				result.WriteString(side.prefix + line + "\n")
			}
		}
	}
	if result.Len() == 0 {
		return "(no working-tree changes)", nil
	}
	return result.String(), nil
}
