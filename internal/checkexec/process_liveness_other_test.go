//go:build !windows && !darwin

package checkexec

import "testing"

func assertProcessStopped(t *testing.T, _ int) {
	t.Helper()
	t.Skip("process-tree containment is supported by this check on Windows and macOS")
}
