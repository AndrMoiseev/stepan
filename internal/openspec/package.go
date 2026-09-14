package openspec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrInvalidChange identifies a change name that cannot name one direct
	// child of openspec/changes.
	ErrInvalidChange = errors.New("invalid OpenSpec change")

	// ErrDocumentUnavailable identifies a required OpenSpec document that
	// cannot be read. The wrapped operating-system error remains available to
	// callers through errors.Is.
	ErrDocumentUnavailable = errors.New("OpenSpec document unavailable")
)

// Document is one loaded OpenSpec Markdown document. Path is relative to the
// repository root with slash separators; Version is the SHA-256 digest of its
// exact bytes.
type Document struct {
	Path    string
	Content string
	Version string
}

// Package is the complete immutable document input for one selected change.
// ChangeSpecs are the delta requirements supplied by the change. MainSpecs
// remain separate so a briefer can choose which established behavior is
// relevant without confusing it with the change's own requirements.
type Package struct {
	Change      string
	Proposal    Document
	Design      Document
	Tasks       Document
	ChangeSpecs []Document
	MainSpecs   []Document
	Version     string
}

// Load reads the complete document package for change from repositoryRoot.
// It deliberately loads documents concretely rather than introducing a
// specification-adapter abstraction. Task Markdown is kept as a document;
// interpreting its hierarchy is the orchestrator's later responsibility.
func Load(repositoryRoot, change string) (Package, error) {
	root, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return Package{}, fmt.Errorf("resolve repository root: %w", err)
	}
	if err := validateChange(change); err != nil {
		return Package{}, err
	}

	changeRoot := filepath.Join(root, "openspec", "changes", change)
	proposal, err := readDocument(root, filepath.Join(changeRoot, "proposal.md"))
	if err != nil {
		return Package{}, err
	}
	design, err := readDocument(root, filepath.Join(changeRoot, "design.md"))
	if err != nil {
		return Package{}, err
	}
	tasks, err := readDocument(root, filepath.Join(changeRoot, "tasks.md"))
	if err != nil {
		return Package{}, err
	}
	changeSpecs, err := readMarkdownDocuments(root, filepath.Join(changeRoot, "specs"))
	if err != nil {
		return Package{}, err
	}
	mainSpecs, err := readMarkdownDocuments(root, filepath.Join(root, "openspec", "specs"))
	if err != nil {
		return Package{}, err
	}

	pkg := Package{
		Change:      change,
		Proposal:    proposal,
		Design:      design,
		Tasks:       tasks,
		ChangeSpecs: changeSpecs,
		MainSpecs:   mainSpecs,
	}
	pkg.Version = packageVersion(pkg)
	return pkg, nil
}

// Documents returns every document that contributed to Version in stable
// package order. The returned slice is independent of the package's slices.
func (p Package) Documents() []Document {
	documents := make([]Document, 0, 3+len(p.ChangeSpecs)+len(p.MainSpecs))
	documents = append(documents, p.Proposal, p.Design)
	documents = append(documents, p.ChangeSpecs...)
	documents = append(documents, p.Tasks)
	documents = append(documents, p.MainSpecs...)
	return documents
}

// CompleteSpecification renders the complete package for roles that need to
// reason about requirements. Main specifications are explicitly labelled and
// retained as individual documents so the briefer can assess relevance rather
// than treating every main spec as an unconditional delta requirement.
func (p Package) CompleteSpecification() string {
	var rendered strings.Builder
	writeDocument(&rendered, "Change proposal", p.Proposal)
	writeDocument(&rendered, "Change design", p.Design)
	for _, document := range p.ChangeSpecs {
		writeDocument(&rendered, "Change specification", document)
	}
	writeDocument(&rendered, "Source task list", p.Tasks)
	for _, document := range p.MainSpecs {
		writeDocument(&rendered, "Main specification (select if relevant)", document)
	}
	return strings.TrimSpace(rendered.String())
}

func validateChange(change string) error {
	if strings.TrimSpace(change) == "" || filepath.Base(change) != change || change == "." || change == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidChange, change)
	}
	return nil
}

func readMarkdownDocuments(repositoryRoot, directory string) ([]Document, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return nil, unavailable(directory, err)
	}
	if !info.IsDir() {
		return nil, unavailable(directory, errors.New("expected directory"))
	}

	var paths []string
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", path)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, unavailable(directory, err)
	}
	sort.Strings(paths)

	documents := make([]Document, 0, len(paths))
	for _, path := range paths {
		document, err := readDocument(repositoryRoot, path)
		if err != nil {
			return nil, err
		}
		documents = append(documents, document)
	}
	return documents, nil
}

func readDocument(repositoryRoot, path string) (Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Document{}, unavailable(path, err)
	}
	if !info.Mode().IsRegular() {
		return Document{}, unavailable(path, errors.New("expected regular file"))
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Document{}, unavailable(path, err)
	}
	relative, err := filepath.Rel(repositoryRoot, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Document{}, fmt.Errorf("document %q is outside repository root %q", path, repositoryRoot)
	}
	digest := sha256.Sum256(contents)
	return Document{Path: filepath.ToSlash(relative), Content: string(contents), Version: hex.EncodeToString(digest[:])}, nil
}

func unavailable(path string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrDocumentUnavailable, path, err)
}

func packageVersion(pkg Package) string {
	hash := sha256.New()
	writeVersionPart(hash, "change", pkg.Change, "")
	writeVersionPart(hash, "proposal", pkg.Proposal.Path, pkg.Proposal.Version)
	writeVersionPart(hash, "design", pkg.Design.Path, pkg.Design.Version)
	for _, document := range pkg.ChangeSpecs {
		writeVersionPart(hash, "change-spec", document.Path, document.Version)
	}
	writeVersionPart(hash, "tasks", pkg.Tasks.Path, pkg.Tasks.Version)
	for _, document := range pkg.MainSpecs {
		writeVersionPart(hash, "main-spec", document.Path, document.Version)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeVersionPart(hash interface{ Write([]byte) (int, error) }, kind, path, version string) {
	for _, part := range []string{kind, path, version} {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
}

func writeDocument(rendered *strings.Builder, kind string, document Document) {
	fmt.Fprintf(rendered, "# %s: %s\n\n%s\n\n", kind, document.Path, document.Content)
}
