package agentruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrResolvedExecutableNotRegular classifies a PATH result that cannot be used
// as a regular executable file. Adapters may attach provider-safe diagnostics.
var ErrResolvedExecutableNotRegular = errors.New("resolved executable must be a regular file")

// ExecutableName is a validated, provider-neutral executable name. It contains
// no directory component and is suitable for an exact lookup through PATH.
type ExecutableName string

// ParseExecutableName validates an executable name without consulting PATH.
func ParseExecutableName(value string) (ExecutableName, error) {
	if value == "" || strings.TrimSpace(value) != value || value == "." || value == ".." ||
		filepath.IsAbs(value) || filepath.VolumeName(value) != "" || filepath.Base(value) != value ||
		strings.ContainsAny(value, `/\`) {
		return "", errors.New("executable must be a simple PATH name without directory separators")
	}
	return ExecutableName(value), nil
}

// String returns the validated name without resolving it.
func (name ExecutableName) String() string { return string(name) }

// Resolve finds the exact name through PATH and returns a normalized absolute
// regular-file path. It does not probe the executable or inspect its branding.
func (name ExecutableName) Resolve() (string, error) {
	validated, err := ParseExecutableName(string(name))
	if err != nil {
		return "", err
	}
	resolved, err := exec.LookPath(validated.String())
	if err != nil {
		return "", fmt.Errorf("find executable in PATH: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("make resolved executable absolute")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrResolvedExecutableNotRegular
	}
	return filepath.Clean(resolved), nil
}
