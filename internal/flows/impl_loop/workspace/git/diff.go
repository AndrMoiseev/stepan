package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
)

func gitAssignmentDiff(ctx context.Context, repository, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("%w: assignment diff base is required", workspace.ErrAssignmentDiff)
	}
	command := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--no-ext-diff", "--find-renames", base, "--")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: capture assignment diff: %v: %s", workspace.ErrAssignmentDiff, err, strings.TrimSpace(string(output)))
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
		return nil, fmt.Errorf("%w: list untracked assignment files: %v", workspace.ErrAssignmentDiff, err)
	}
	var diff []byte
	for _, raw := range strings.Split(strings.TrimSuffix(string(paths), "\x00"), "\x00") {
		if raw == "" {
			continue
		}
		path, err := safeAssignmentDiffPath(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: untracked assignment path: %v", workspace.ErrAssignmentDiff, err)
		}
		command := exec.CommandContext(ctx, "git", "-C", repository, "diff", "--no-index", "--", "/dev/null", path)
		output, err := command.CombinedOutput()
		// git diff --no-index uses exit code 1 for a normal difference.
		if err != nil {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				return nil, fmt.Errorf("%w: capture untracked file %q: %v: %s", workspace.ErrAssignmentDiff, path, err, strings.TrimSpace(string(output)))
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
