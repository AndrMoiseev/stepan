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

// resolvePath rejects lexical and symlink escapes, including a non-existing
// target whose nearest existing ancestor is a symlink outside root.
func resolvePath(root, supplied string) (string, error) {
	if supplied == "" {
		return "", fmt.Errorf("path is required")
	}
	candidate := filepath.Clean(supplied)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	if !pathWithin(root, candidate) {
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
	if err != nil || !pathWithin(root, canonicalExisting) {
		return "", fmt.Errorf("path escapes workspace through a link")
	}
	if _, err := os.Lstat(candidate); err == nil {
		canonicalCandidate, err := filepath.EvalSymlinks(candidate)
		if err != nil || !pathWithin(root, canonicalCandidate) {
			return "", fmt.Errorf("path escapes workspace through a link")
		}
	}
	return candidate, nil
}
