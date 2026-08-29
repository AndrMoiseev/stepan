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

// resolveWorkspacePath resolves supplied relative paths from the canonical
// workspace and rejects lexical and link-based escapes. The returned absolute
// path is consequently the same path the CLI uses with WithCwd(workspace).
func resolveWorkspacePath(workspace, supplied string) (string, error) {
	if supplied == "" {
		return "", fmt.Errorf("path is required")
	}
	candidate := filepath.Clean(supplied)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	if !pathWithin(workspace, candidate) {
		return "", fmt.Errorf("path is outside workspace")
	}
	existing := candidate
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("no existing path ancestor")
		}
		existing = parent
	}
	canonicalExisting, err := filepath.EvalSymlinks(existing)
	if err != nil || canonicalExisting != existing || !pathWithin(workspace, canonicalExisting) {
		return "", fmt.Errorf("path escapes workspace through a link")
	}
	if _, err := os.Lstat(candidate); err == nil {
		canonicalCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil || canonicalCandidate != candidate || !pathWithin(workspace, canonicalCandidate) {
			return "", fmt.Errorf("path escapes workspace through a link")
		}
	}
	return candidate, nil
}

// resolveAllowedPath resolves a tool path against the workspace (the SDK cwd)
// and permits the one separately configured artifact root. It deliberately
// does not treat the system temp parent as allowed.
func resolveAllowedPath(workspace, artifactRoot, supplied string) (string, error) {
	if supplied == "" {
		return "", fmt.Errorf("path is required")
	}
	candidate := filepath.Clean(supplied)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	root := workspace
	if artifactRoot != "" && pathWithin(artifactRoot, candidate) {
		root = artifactRoot
	} else if !pathWithin(workspace, candidate) {
		return "", fmt.Errorf("path is outside allowed roots")
	}
	existing := candidate
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("no existing path ancestor")
		}
		existing = parent
	}
	canonicalExisting, err := filepath.EvalSymlinks(existing)
	if err != nil || !pathWithin(root, canonicalExisting) {
		return "", fmt.Errorf("path escapes allowed root through a link")
	}
	if _, err := os.Lstat(candidate); err == nil {
		canonicalCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(root, canonicalCandidate) {
			return "", fmt.Errorf("path escapes allowed root through a link")
		}
	}
	return candidate, nil
}

func pathWithinRoot(root, candidate string) error {
	if !pathWithin(root, candidate) {
		return fmt.Errorf("path is outside allowed root")
	}
	return nil
}
