package claudeapp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func canonicalDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("path %q must be absolute", path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.EvalSymlinks(path)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(relative)
}

// canonicalTarget resolves an existing target, or the nearest existing
// ancestor plus the missing suffix. Comparing this result with canonical
// roots makes Windows 8.3 aliases and filesystem links unambiguous.
func canonicalTarget(cwd, supplied string) (string, error) {
	if cwd == "" || !filepath.IsAbs(cwd) || supplied == "" {
		return "", fmt.Errorf("path is invalid")
	}
	candidate := filepath.Clean(supplied)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	candidate = filepath.Clean(candidate)

	existing := candidate
	for {
		info, statErr := os.Stat(existing)
		if statErr == nil {
			canonical, resolveErr := filepath.EvalSymlinks(existing)
			if resolveErr != nil {
				return "", resolveErr
			}
			relative, relErr := filepath.Rel(existing, candidate)
			if relErr != nil || filepath.IsAbs(relative) {
				return "", fmt.Errorf("path cannot be resolved")
			}
			if relative != "." && !info.IsDir() {
				return "", fmt.Errorf("path has a non-directory ancestor")
			}
			return filepath.Clean(filepath.Join(canonical, relative)), nil
		}
		if !os.IsNotExist(statErr) {
			return "", statErr
		}
		if _, linkErr := os.Lstat(existing); linkErr == nil || !os.IsNotExist(linkErr) {
			return "", fmt.Errorf("path contains an unresolved link")
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("path has no existing ancestor")
		}
		existing = parent
	}
}

// resolveWorkspacePath resolves supplied relative paths from the canonical
// workspace and rejects lexical and link-based escapes. The returned absolute
// path is consequently the same path the CLI uses with WithCwd(workspace).
func resolveWorkspacePath(workspace, supplied string) (string, error) {
	canonicalWorkspace, err := canonicalDirectory(workspace)
	if err != nil {
		return "", err
	}
	candidate, err := canonicalTarget(canonicalWorkspace, supplied)
	if err != nil || !pathWithin(canonicalWorkspace, candidate) {
		return "", fmt.Errorf("path is outside workspace or escapes through a link")
	}
	return candidate, nil
}

// resolveAllowedPath resolves a tool path against the workspace (the SDK cwd)
// and permits the one separately configured artifact root. It deliberately
// does not treat the system temp parent as allowed.
func resolveAllowedPath(workspace, artifactRoot, supplied string) (string, error) {
	candidate, err := canonicalTarget(workspace, supplied)
	if err != nil {
		return "", err
	}
	if artifactRoot != "" && pathWithin(artifactRoot, candidate) {
		return candidate, nil
	}
	if !pathWithin(workspace, candidate) {
		return "", fmt.Errorf("path is outside allowed roots")
	}
	return candidate, nil
}

func pathWithinRoot(root, candidate string) error {
	if !pathWithin(root, candidate) {
		return fmt.Errorf("path is outside allowed root")
	}
	return nil
}
