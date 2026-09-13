package gitsnapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrRepositoryDiverged = errors.New("REPOSITORY_DIVERGED")
var ErrOutsideBoundary = errors.New("changes outside allowed root")
var ErrSubmoduleCycle = errors.New("submodule worktree cycle")

// SubmoduleCycleError reports a repeated canonical repository root while a
// populated submodule hierarchy is being fingerprinted.
type SubmoduleCycleError struct {
	Root string
}

func (err *SubmoduleCycleError) Error() string {
	return fmt.Sprintf("%v: %s", ErrSubmoduleCycle, err.Root)
}

func (err *SubmoduleCycleError) Unwrap() error { return ErrSubmoduleCycle }

type Snapshot struct {
	HeadOID        string `json:"head_oid"`
	HeadRef        string `json:"head_ref"`
	TreeOID        string `json:"tree_oid"`
	IndexHash      string `json:"index_hash"`
	StatusHash     string `json:"status_hash"`
	SubmodulesHash string `json:"submodules_hash"`
	// worktreeFiles is deliberately not serialized. It is the in-memory,
	// byte-exact pre-operation material needed only for a targeted rollback.
	// TreeOID remains the filtered Git-oriented representation used for diffs.
	worktreeFiles map[string]worktreeFile
}

type worktreeFile struct {
	contents []byte
	mode     os.FileMode
	regular  bool
}

// Difference describes every repository dimension changed during one
// operation. Paths exclude changes already present in the before snapshot;
// submodule changes are reported separately because their internal paths do
// not belong to the superproject tree.
type Difference struct {
	Paths             []string
	HeadChanged       bool
	HeadRefChanged    bool
	IndexChanged      bool
	StatusChanged     bool
	SubmodulesChanged bool
}

// ErrRestoreUnsafe means that a targeted working-tree restoration could not
// be completed without following a link or replacing something other than a
// regular file.  Callers must treat this as an execution blockage, rather
// than falling back to a broad checkout or reset.
var ErrRestoreUnsafe = errors.New("targeted working-tree restoration is unsafe")

type BoundaryError struct {
	Paths []string
}

func (err *BoundaryError) Error() string {
	return fmt.Sprintf("%v: %s", ErrOutsideBoundary, strings.Join(err.Paths, ", "))
}

func (err *BoundaryError) Unwrap() error { return ErrOutsideBoundary }

type repositoryState struct {
	head        string
	headRef     string
	index       []byte
	indexExists bool
	status      []byte
}

type populatedSubmodule struct {
	path string
	root string
}

var (
	findPopulatedSubmodules = populatedSubmodules
	beforeGitCommandHook    func(repository, index string, args []string)
)

func Capture(ctx context.Context, repository string) (Snapshot, error) {
	return capture(ctx, repository, nil)
}

// Compare returns paths changed between two synthetic trees.
func Compare(ctx context.Context, repository string, before, after Snapshot) ([]string, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return nil, err
	}
	if before.HeadOID != after.HeadOID || before.HeadRef != after.HeadRef {
		return nil, ErrRepositoryDiverged
	}
	output, err := git(ctx, root, "", "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", before.TreeOID, after.TreeOID)
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("git diff-tree returned a non-NUL-terminated path list")
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path, err := normalizeRelativePath(string(part))
		if err != nil {
			return nil, fmt.Errorf("invalid changed path %q: %w", part, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// Diff returns file, HEAD, index, and submodule changes attributable to the
// interval between before and after. It permits a changed HEAD so callers can
// describe operations that intentionally create a commit.
func Diff(ctx context.Context, repository string, before, after Snapshot) (Difference, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Difference{}, err
	}
	output, err := git(ctx, root, "", "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", before.TreeOID, after.TreeOID)
	if err != nil {
		return Difference{}, err
	}
	paths, err := parseChangedPaths(output)
	if err != nil {
		return Difference{}, err
	}
	return Difference{
		Paths:             paths,
		HeadChanged:       before.HeadOID != after.HeadOID,
		HeadRefChanged:    before.HeadRef != after.HeadRef,
		IndexChanged:      before.IndexHash != after.IndexHash,
		StatusChanged:     before.StatusHash != after.StatusHash,
		SubmodulesChanged: before.SubmodulesHash != after.SubmodulesHash,
	}, nil
}

// EnsureUnchanged verifies the complete repository fingerprint before the next
// operation. It reports divergence without attempting repair.
func EnsureUnchanged(ctx context.Context, repository string, expected Snapshot) error {
	actual, err := Capture(ctx, repository)
	if err != nil {
		return err
	}
	if !sameGitSnapshot(actual, expected) {
		return ErrRepositoryDiverged
	}
	return nil
}

// RestorePaths restores just paths changed during one observed operation to
// their contents in before.  expectedCurrent is a mandatory compare-and-swap
// observation: if the worktree no longer equals it, nothing is restored and
// ErrRepositoryDiverged is returned.  The function never changes HEAD or the
// real index and deliberately does not offer a whole-worktree restore.
//
// The synthetic tree captured by Capture includes both tracked and untracked
// non-ignored files, so it also preserves a caller's dirty work from before
// the operation.  Ignored files, symlinks, Git metadata, and concurrent
// editors remain outside this first-version recovery boundary.
func RestorePaths(ctx context.Context, repository string, before, expectedCurrent Snapshot, paths []string) (Snapshot, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	current, err := Capture(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	if !sameSnapshot(current, expectedCurrent) {
		return Snapshot{}, ErrRepositoryDiverged
	}

	seen := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		path, err := normalizeRelativePath(candidate)
		if err != nil {
			return Snapshot{}, fmt.Errorf("invalid restoration path %q: %w", candidate, err)
		}
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		entry, known := before.worktreeFiles[path]
		if err := restorePath(root, path, entry, known); err != nil {
			return Snapshot{}, err
		}
	}

	restored, err := Capture(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	if restored.HeadOID != expectedCurrent.HeadOID || restored.HeadRef != expectedCurrent.HeadRef || restored.IndexHash != expectedCurrent.IndexHash || restored.SubmodulesHash != expectedCurrent.SubmodulesHash {
		return Snapshot{}, ErrRepositoryDiverged
	}
	for path := range seen {
		if !sameWorktreeFile(before.worktreeFiles[path], restored.worktreeFiles[path]) {
			return Snapshot{}, ErrRepositoryDiverged
		}
	}
	return restored, nil
}

func restorePath(root, path string, entry worktreeFile, known bool) error {
	if known && !entry.regular {
		return fmt.Errorf("%w: pre-call file %q is not a regular file", ErrRestoreUnsafe, path)
	}
	target, err := safeRepositoryPath(root, path, entry.regular)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: restoration target %q is not a regular file", ErrRestoreUnsafe, path)
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect restoration target %q: %w", path, err)
	}
	if !entry.regular {
		if os.IsNotExist(err) {
			return nil
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("remove restoration target %q: %w", path, err)
		}
		return nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".stepan-restore-*")
	if err != nil {
		return fmt.Errorf("create restoration temporary for %q: %w", path, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(entry.mode); err != nil {
		temporary.Close()
		return fmt.Errorf("set restoration mode for %q: %w", path, err)
	}
	if _, err := temporary.Write(entry.contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write restoration content for %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync restoration content for %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close restoration content for %q: %w", path, err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("publish restoration content for %q: %w", path, err)
	}
	return nil
}

func safeRepositoryPath(root, path string, createParents bool) (string, error) {
	parts := strings.Split(path, "/")
	directory := root
	for _, part := range parts[:len(parts)-1] {
		directory = filepath.Join(directory, part)
		info, err := os.Lstat(directory)
		if os.IsNotExist(err) && createParents {
			if err := os.Mkdir(directory, 0o755); err != nil && !os.IsExist(err) {
				return "", fmt.Errorf("create restoration directory %q: %w", path, err)
			}
			info, err = os.Lstat(directory)
		}
		if err != nil {
			return "", fmt.Errorf("inspect restoration directory %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%w: restoration directory %q is unsafe", ErrRestoreUnsafe, path)
		}
	}
	return filepath.Join(directory, parts[len(parts)-1]), nil
}

func parseChangedPaths(output []byte) ([]string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("git diff-tree returned a non-NUL-terminated path list")
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		path, err := normalizeRelativePath(string(part))
		if err != nil {
			return nil, fmt.Errorf("invalid changed path %q: %w", part, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// CheckBoundary rejects changed paths outside allowedRoot.
func CheckBoundary(changedPaths []string, allowedRoot string) error {
	root, err := normalizeRelativePath(allowedRoot)
	if err != nil {
		return fmt.Errorf("invalid allowed root: %w", err)
	}
	prefix := root + "/"
	outside := make([]string, 0)
	for _, changedPath := range changedPaths {
		path, err := normalizeRelativePath(changedPath)
		if err != nil {
			return fmt.Errorf("invalid changed path %q: %w", changedPath, err)
		}
		if !strings.HasPrefix(path, prefix) {
			outside = append(outside, path)
		}
	}
	if len(outside) != 0 {
		return &BoundaryError{Paths: outside}
	}
	return nil
}

func normalizeRelativePath(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return "", errors.New("path must be relative")
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes repository")
	}
	return filepath.ToSlash(path), nil
}

func capture(ctx context.Context, repository string, betweenCaptures func() error) (Snapshot, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	before, err := captureHierarchy(ctx, root, make(map[string]bool), make(map[string]Snapshot))
	if err != nil {
		return Snapshot{}, err
	}
	if betweenCaptures != nil {
		if err := betweenCaptures(); err != nil {
			return Snapshot{}, err
		}
	}
	after, err := captureHierarchy(ctx, root, make(map[string]bool), make(map[string]Snapshot))
	if err != nil {
		return Snapshot{}, err
	}
	if !sameGitSnapshot(before, after) {
		return Snapshot{}, ErrRepositoryDiverged
	}
	return before, nil
}

// captureHierarchy takes one complete, depth-first fingerprint of a populated
// submodule tree. Capture invokes it twice, so each repository is visited once
// per pass rather than recursively recapturing each descendant at every level.
func captureHierarchy(ctx context.Context, repository string, active map[string]bool, visited map[string]Snapshot) (Snapshot, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	if active[root] {
		return Snapshot{}, &SubmoduleCycleError{Root: root}
	}
	if snapshot, ok := visited[root]; ok {
		return snapshot, nil
	}
	active[root] = true
	defer delete(active, root)

	snapshot, err := captureLocal(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	children, err := findPopulatedSubmodules(ctx, root)
	if err != nil {
		return Snapshot{}, err
	}
	hash := sha256.New()
	for _, child := range children {
		if active[child.root] {
			return Snapshot{}, &SubmoduleCycleError{Root: child.root}
		}
		childSnapshot, err := captureHierarchy(ctx, child.root, active, visited)
		if err != nil {
			return Snapshot{}, fmt.Errorf("capture submodule %q: %w", child.path, err)
		}
		writeSnapshotFingerprint(hash, child.path, childSnapshot)
	}
	if err := verifyLocalState(ctx, root, snapshot); err != nil {
		return Snapshot{}, err
	}
	snapshot.SubmodulesHash = fmt.Sprintf("%x", hash.Sum(nil))
	visited[root] = snapshot
	return snapshot, nil
}

// captureLocal fingerprints one repository without descending into its
// submodules. capture compares two complete hierarchy observations, so every
// local fingerprint participates in both linear passes.
func captureLocal(ctx context.Context, repository string) (Snapshot, error) {
	before, err := readState(ctx, repository)
	if err != nil {
		return Snapshot{}, err
	}
	tempDir, err := os.MkdirTemp("", "stepan-index-")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(tempDir)
	index := filepath.Join(tempDir, "index")
	// Reset stat data so same-size/timestamp changes are hashed again.
	if _, err := git(ctx, repository, index, "read-tree", before.head); err != nil {
		return Snapshot{}, err
	}
	if _, err := git(ctx, repository, index, "add", "-A", "--", "."); err != nil {
		return Snapshot{}, err
	}
	tree, err := git(ctx, repository, index, "write-tree")
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		HeadOID:    before.head,
		HeadRef:    before.headRef,
		TreeOID:    strings.TrimSpace(string(tree)),
		IndexHash:  hashIndex(before.index, before.indexExists),
		StatusHash: hashStatus(before.status),
	}
	snapshot.worktreeFiles, err = captureWorktreeFiles(ctx, repository, snapshot.TreeOID)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// captureWorktreeFiles records direct bytes from the working tree after the
// synthetic tree has established the observable path set. In particular, it
// does not read blobs back through Git: clean filters and line-ending
// conversion may make those bytes differ from what existed before the call.
func captureWorktreeFiles(ctx context.Context, repository, tree string) (map[string]worktreeFile, error) {
	output, err := git(ctx, repository, "", "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}
	files := make(map[string]worktreeFile)
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		metadata, rawPath, found := bytes.Cut(raw, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 {
			return nil, errors.New("git ls-tree returned an invalid entry")
		}
		// A gitlink is represented as a commit rather than a blob. Its dirty
		// state belongs to the recursive submodule fingerprint and it is never
		// a regular superproject worktree file that targeted restoration can
		// safely own.
		if bytes.Equal(fields[1], []byte("commit")) && bytes.Equal(fields[0], []byte("160000")) {
			continue
		}
		if !bytes.Equal(fields[1], []byte("blob")) {
			return nil, errors.New("git ls-tree returned an invalid entry")
		}
		path, err := normalizeRelativePath(string(rawPath))
		if err != nil {
			return nil, fmt.Errorf("invalid observed path %q: %w", rawPath, err)
		}
		if !bytes.Equal(fields[0], []byte("100644")) && !bytes.Equal(fields[0], []byte("100755")) {
			files[path] = worktreeFile{}
			continue
		}
		target, err := safeRepositoryPath(repository, path, false)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return nil, fmt.Errorf("inspect observed file %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: observed file %q is not a regular file", ErrRestoreUnsafe, path)
		}
		contents, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("read observed file %q: %w", path, err)
		}
		files[path] = worktreeFile{contents: contents, mode: info.Mode().Perm(), regular: true}
	}
	return files, nil
}

func sameGitSnapshot(left, right Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.TreeOID == right.TreeOID && left.IndexHash == right.IndexHash && left.StatusHash == right.StatusHash && left.SubmodulesHash == right.SubmodulesHash
}

func sameSnapshot(left, right Snapshot) bool {
	if !sameGitSnapshot(left, right) || len(left.worktreeFiles) != len(right.worktreeFiles) {
		return false
	}
	for path, leftFile := range left.worktreeFiles {
		rightFile, found := right.worktreeFiles[path]
		if !found || !sameWorktreeFile(leftFile, rightFile) {
			return false
		}
	}
	return true
}

func sameWorktreeFile(left, right worktreeFile) bool {
	return left.regular == right.regular && left.mode == right.mode && bytes.Equal(left.contents, right.contents)
}

// verifyLocalState brackets temporary-index and submodule traversal work with
// a second observation of the real repository state. The synthetic index must
// never hide a HEAD, ref, index, or status mutation that races this pass.
func verifyLocalState(ctx context.Context, repository string, snapshot Snapshot) error {
	after, err := readState(ctx, repository)
	if err != nil {
		return err
	}
	if snapshot.HeadOID != after.head || snapshot.HeadRef != after.headRef || snapshot.IndexHash != hashIndex(after.index, after.indexExists) || snapshot.StatusHash != hashStatus(after.status) {
		return ErrRepositoryDiverged
	}
	return nil
}

func hashIndex(index []byte, exists bool) string {
	marker := byte(0)
	if exists {
		marker = 1
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte{marker})
	_, _ = hash.Write(index)
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func hashStatus(status []byte) string {
	hash := sha256.Sum256(status)
	return fmt.Sprintf("%x", hash[:])
}

func repositoryRoot(ctx context.Context, repository string) (string, error) {
	if !filepath.IsAbs(repository) {
		return "", errors.New("repository path must be absolute")
	}
	root, err := git(ctx, repository, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("find Git root from %q: %w", repository, err)
	}
	path := strings.TrimSpace(string(root))
	if path == "" {
		return "", errors.New("git returned an empty repository root")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make Git root absolute: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize Git root: %w", err)
	}
	return filepath.Clean(path), nil
}

// populatedSubmodules lists populated direct submodules. A superproject's
// status only records that a submodule is dirty; hierarchy capture supplies
// the distinct recursive fingerprints.
func populatedSubmodules(ctx context.Context, repository string) ([]populatedSubmodule, error) {
	entries, err := git(ctx, repository, "", "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	children := make([]populatedSubmodule, 0)
	for _, entry := range bytes.Split(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		fields := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(fields) != 2 || !bytes.HasPrefix(fields[0], []byte("160000 ")) {
			continue
		}
		path, err := normalizeRelativePath(string(fields[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid submodule path %q: %w", fields[1], err)
		}
		child := filepath.Join(repository, filepath.FromSlash(path))
		childRoot, err := repositoryRoot(ctx, child)
		if err != nil {
			// An unpopulated submodule has no worktree to fingerprint.
			continue
		}
		canonicalChild, err := filepath.EvalSymlinks(child)
		if err != nil {
			return nil, fmt.Errorf("canonicalize submodule %q: %w", path, err)
		}
		if filepath.Clean(canonicalChild) != childRoot {
			continue
		}
		children = append(children, populatedSubmodule{path: path, root: childRoot})
	}
	return children, nil
}

func writeSnapshotFingerprint(hash interface{ Write([]byte) (int, error) }, path string, snapshot Snapshot) {
	for _, value := range []string{path, snapshot.HeadOID, snapshot.HeadRef, snapshot.TreeOID, snapshot.IndexHash, snapshot.StatusHash, snapshot.SubmodulesHash} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
}

func readState(ctx context.Context, repository string) (repositoryState, error) {
	head, err := git(ctx, repository, "", "rev-parse", "--verify", "HEAD")
	if err != nil {
		return repositoryState{}, err
	}
	headRef, err := git(ctx, repository, "", "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return repositoryState{}, err
	}
	indexPathData, err := git(ctx, repository, "", "rev-parse", "--git-path", "index")
	if err != nil {
		return repositoryState{}, err
	}
	indexPath := strings.TrimSpace(string(indexPathData))
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(repository, indexPath)
	}
	index, err := os.ReadFile(indexPath)
	indexExists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return repositoryState{}, err
	}
	// Status catches worktree state that a synthetic top-level tree cannot
	// represent, notably a dirty nested submodule. Ignored files are omitted:
	// they do not participate in the Git working-copy contract.
	status, err := git(ctx, repository, "", "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return repositoryState{}, err
	}
	return repositoryState{strings.TrimSpace(string(head)), strings.TrimSpace(string(headRef)), index, indexExists, status}, nil
}

func git(ctx context.Context, repository, index string, args ...string) ([]byte, error) {
	if beforeGitCommandHook != nil {
		beforeGitCommandHook(repository, index, args)
	}
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repository
	command.Env = gitEnvironment(index)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func gitEnvironment(index string) []string {
	blocked := map[string]bool{
		"GIT_DIR":                          true,
		"GIT_WORK_TREE":                    true,
		"GIT_COMMON_DIR":                   true,
		"GIT_INDEX_FILE":                   true,
		"GIT_OBJECT_DIRECTORY":             true,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
		"GIT_NAMESPACE":                    true,
		"GIT_REPLACE_REF_BASE":             true,
		"GIT_SHALLOW_FILE":                 true,
		"GIT_CEILING_DIRECTORIES":          true,
		"GIT_OPTIONAL_LOCKS":               true,
	}
	environment := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		environment = append(environment, item)
	}
	environment = append(environment, "GIT_OPTIONAL_LOCKS=0")
	if index != "" {
		environment = append(environment, "GIT_INDEX_FILE="+index)
	}
	return environment
}
