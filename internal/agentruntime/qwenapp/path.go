package qwenapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveExecutable resolves a simple PATH name or validates an authoritative
// absolute regular-file path without probing version, basename, or branding.
func ResolveExecutable(name string) (string, error) {
	if name == "" {
		name = defaultExecutable
	}
	if filepath.IsAbs(name) {
		info, err := os.Stat(name)
		if err != nil {
			return "", fmt.Errorf("stat Qwen executable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("Qwen executable must be a regular file")
		}
		return filepath.Clean(name), nil
	}
	if filepath.Base(name) != name {
		return "", errors.New("Qwen executable must be an absolute path or a PATH name")
	}
	resolved, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("resolve Qwen executable %q: %w", name, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("make Qwen executable absolute: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("resolved Qwen executable must be a regular file")
	}
	return filepath.Clean(resolved), nil
}

func canonicalDirectory(path, description string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", description)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", description, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s must be a directory", description)
	}
	canonical, err := canonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s links: %w", description, err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("make %s absolute: %w", description, err)
	}
	return filepath.Clean(canonical), nil
}

func canonicalGitRoot(path string) (string, error) {
	root, err := canonicalDirectory(path, "Qwen workspace")
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", fmt.Errorf("Qwen workspace is not a Git root: %w", err)
	}
	return root, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func validateDistinctRoots(workspace, artifact string) error {
	if pathWithin(workspace, artifact) || pathWithin(artifact, workspace) {
		return errors.New("Qwen artifact root must not overlap the Git workspace")
	}
	return nil
}
