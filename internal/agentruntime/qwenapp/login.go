package qwenapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// RunInteractiveLogin gives the selected CLI direct ownership of the terminal.
// It deliberately runs without ACP or Stepan's restricted model arguments: the
// CLI owns its authentication protocol and persists the resulting credentials.
func RunInteractiveLogin(ctx context.Context, executable, workspace string, stdin io.Reader, stdout, stderr io.Writer) error {
	if ctx == nil {
		return fmt.Errorf("%w: interactive login context is required", ErrConfiguration)
	}
	if stdin == nil || stdout == nil || stderr == nil {
		return fmt.Errorf("%w: interactive login terminal streams are required", ErrConfiguration)
	}
	resolved, err := ResolveExecutable(executable)
	if err != nil {
		return fmt.Errorf("%w: resolve interactive login executable: %w", ErrConfiguration, err)
	}
	root, err := canonicalGitRoot(workspace)
	if err != nil {
		return fmt.Errorf("%w: resolve interactive login workspace: %w", ErrConfiguration, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	command := exec.CommandContext(ctx, resolved)
	command.Dir = root
	command.Env = os.Environ()
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if cause := ctx.Err(); cause != nil {
			return cause
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
			return fmt.Errorf("Qwen interactive login via %q failed with exit code %d: %w", executable, exitErr.ProcessState.ExitCode(), errors.Join(ErrStartup, err))
		}
		return fmt.Errorf("Qwen interactive login via %q failed: %w", executable, errors.Join(ErrStartup, err))
	}
	return nil
}
