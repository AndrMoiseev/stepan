package qwenapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// ResolveExecutable resolves the authoritative simple PATH name without
// probing version, basename, or branding.
func ResolveExecutable(name string) (string, error) {
	if name == "" {
		name = defaultExecutable
	}
	executableName, err := agentruntime.ParseExecutableName(name)
	if err != nil {
		return "", fmt.Errorf("Qwen executable: %w", err)
	}
	resolved, err := executableName.Resolve()
	if err != nil {
		failure := fmt.Errorf("resolve Qwen executable %q: %w", name, err)
		if errors.Is(err, agentruntime.ErrResolvedExecutableNotRegular) {
			return "", withDiagnosticContext(failure, diagnosticRegularFile)
		}
		return "", failure
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
	command := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	command.Env = withoutGitContext(os.Environ())
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("Qwen workspace is not a Git root: %w", err)
	}
	reported := strings.TrimSpace(string(output))
	reportedRoot, err := canonicalDirectory(reported, "reported Git root")
	if err != nil {
		return "", fmt.Errorf("verify Qwen Git root: %w", err)
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("stat Qwen Git root: %w", err)
	}
	reportedInfo, err := os.Stat(reportedRoot)
	if err != nil {
		return "", fmt.Errorf("stat reported Git root: %w", err)
	}
	if !os.SameFile(rootInfo, reportedInfo) {
		return "", errors.New("Qwen workspace must be the Git root, not a nested directory")
	}
	return root, nil
}

func withoutGitContext(environment []string) []string {
	blocked := map[string]struct{}{
		"GIT_DIR": {}, "GIT_WORK_TREE": {}, "GIT_COMMON_DIR": {},
		"GIT_CEILING_DIRECTORIES": {}, "GIT_DISCOVERY_ACROSS_FILESYSTEM": {},
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, denied := blocked[strings.ToUpper(name)]; denied {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// canonicalTarget resolves a path exactly as a filesystem operation rooted at
// cwd would. Existing targets are resolved directly. For a target that does
// not exist yet, its nearest existing ancestor is resolved first and the
// missing suffix is appended to that canonical ancestor. This catches a
// symlink or Windows junction in every existing component without requiring
// the final file to exist.
func canonicalTarget(cwd, supplied string) (string, error) {
	if cwd == "" || !filepath.IsAbs(cwd) || supplied == "" {
		return "", errors.New("filesystem target is invalid")
	}
	candidate := filepath.Clean(supplied)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", errors.New("filesystem target is invalid")
	}
	candidate = filepath.Clean(candidate)

	existing := candidate
	for {
		info, statErr := os.Stat(existing)
		if statErr == nil {
			canonical, resolveErr := canonicalExistingPath(existing)
			if resolveErr != nil {
				return "", errors.New("filesystem target cannot be resolved")
			}
			relative, relErr := filepath.Rel(existing, candidate)
			if relErr != nil || filepath.IsAbs(relative) {
				return "", errors.New("filesystem target cannot be resolved")
			}
			if relative != "." && !info.IsDir() {
				return "", errors.New("filesystem target has a non-directory ancestor")
			}
			return filepath.Clean(filepath.Join(canonical, relative)), nil
		}
		if !os.IsNotExist(statErr) {
			return "", errors.New("filesystem target cannot be inspected")
		}
		// A dangling link is an existing, ambiguous boundary. Treat it as a
		// denial rather than walking past it as though it were a new name.
		if _, linkErr := os.Lstat(existing); linkErr == nil || !os.IsNotExist(linkErr) {
			return "", errors.New("filesystem target contains an unresolved link")
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", errors.New("filesystem target has no existing ancestor")
		}
		existing = parent
	}
}

func canonicalTargetWithin(cwd, root, supplied string) (string, error) {
	if root == "" {
		return "", ErrPermissionDenied
	}
	canonicalRoot, err := canonicalDirectory(root, "filesystem permission root")
	if err != nil {
		return "", ErrPermissionDenied
	}
	target, err := canonicalTarget(cwd, supplied)
	if err != nil || target == canonicalRoot || !pathWithin(canonicalRoot, target) {
		return "", ErrPermissionDenied
	}
	if info, statErr := os.Stat(target); statErr == nil && info.IsDir() {
		return "", ErrPermissionDenied
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", ErrPermissionDenied
	}
	return target, nil
}

func validateDistinctRoots(workspace, artifact string) error {
	if pathWithin(workspace, artifact) || pathWithin(artifact, workspace) {
		return errors.New("Qwen artifact root must not overlap the Git workspace")
	}
	return nil
}
