package implementationconfig

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// RulesFileValidation identifies a rules file and its containing rules root
// after a complete, fresh validation pass. It deliberately keeps no snapshot
// or version of the documents.
type RulesFileValidation struct {
	File string
	Root string
}

// ValidateRulesFile resolves and validates the optional project rules_file.
// It scans every Markdown document below the rules file's directory on every
// call, including documents that cannot be reached from the entry document.
func (configuration Configuration) ValidateRulesFile(repositoryRoot string) (RulesFileValidation, error) {
	rulesFile, err := configuration.ResolveRulesFile(repositoryRoot)
	if err != nil {
		return RulesFileValidation{}, err
	}
	if rulesFile == "" {
		return RulesFileValidation{}, nil
	}

	repository, err := canonicalRulesDirectory(repositoryRoot, "repository root")
	if err != nil {
		return RulesFileValidation{}, configurationError("project", "rules_file: "+err.Error())
	}
	rulesRoot, err := canonicalRulesDirectory(filepath.Dir(rulesFile), "rules_file directory")
	if err != nil {
		return RulesFileValidation{}, configurationError("project", "rules_file: "+err.Error())
	}
	if !rulesPathWithin(repository, rulesRoot) {
		return RulesFileValidation{}, configurationError("project", "rules_file directory is outside the repository")
	}

	rulesFile, err = canonicalReadableRulesFile(rulesFile)
	if err != nil {
		return RulesFileValidation{}, configurationError("project", "rules_file: "+err.Error())
	}
	if !isMarkdownPath(rulesFile) {
		return RulesFileValidation{}, configurationError("project", "rules_file must name a Markdown (.md) file")
	}
	if !rulesPathWithin(rulesRoot, rulesFile) {
		return RulesFileValidation{}, configurationError("project", "rules_file escapes its rules directory through a symbolic link")
	}

	validator := rulesValidator{repository: repository, rulesRoot: rulesRoot}
	if err := validator.scanDirectory(rulesRoot); err != nil {
		return RulesFileValidation{}, configurationError("project", "rules_file: "+err.Error())
	}
	return RulesFileValidation{File: rulesFile, Root: rulesRoot}, nil
}

type rulesValidator struct {
	repository string
	rulesRoot  string
	visited    map[string]struct{}
}

func (validator *rulesValidator) scanDirectory(directory string) error {
	canonical, err := canonicalRulesDirectory(directory, "rules directory")
	if err != nil {
		return err
	}
	if !rulesPathWithin(validator.rulesRoot, canonical) {
		return fmt.Errorf("rules directory %q escapes the rules root through a symbolic link", directory)
	}
	if validator.visited == nil {
		validator.visited = make(map[string]struct{})
	}
	if _, ok := validator.visited[rulesPathKey(canonical)]; ok {
		return nil
	}
	validator.visited[rulesPathKey(canonical)] = struct{}{}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read rules directory %q: %w", directory, err)
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect rules entry %q: %w", path, err)
		}
		if info.IsDir() {
			if err := validator.scanDirectory(path); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() || !isMarkdownPath(path) {
			continue
		}
		if err := validator.validateDocument(path); err != nil {
			return err
		}
	}
	return nil
}

func (validator *rulesValidator) validateDocument(path string) error {
	canonical, err := canonicalReadableRulesFile(path)
	if err != nil {
		return fmt.Errorf("read rules document %q: %w", path, err)
	}
	if !rulesPathWithin(validator.rulesRoot, canonical) {
		return fmt.Errorf("rules document %q escapes the rules root through a symbolic link", path)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read rules document %q: %w", path, err)
	}

	document := goldmark.DefaultParser().Parse(text.NewReader(contents), parser.WithContext(parser.NewContext()))
	return ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var destination []byte
		switch link := node.(type) {
		case *ast.Link:
			destination = link.Destination
		case *ast.Image:
			destination = link.Destination
		case *ast.AutoLink:
			if link.AutoLinkType == ast.AutoLinkEmail {
				return ast.WalkContinue, nil
			}
			destination = link.URL(contents)
		default:
			return ast.WalkContinue, nil
		}
		if err := validator.validateDestination(path, string(destination)); err != nil {
			return ast.WalkStop, err
		}
		return ast.WalkContinue, nil
	})
}

func (validator *rulesValidator) validateDestination(document, destination string) error {
	target, local, err := localRulesLinkTarget(document, destination)
	if err != nil {
		return fmt.Errorf("invalid link %q in %q: %w", destination, document, err)
	}
	if !local {
		return nil
	}
	canonical, err := canonicalReadableRulesFile(target)
	if err != nil {
		return fmt.Errorf("local link %q in %q: %w", destination, document, err)
	}
	allowedRoot := validator.repository
	if isMarkdownPath(target) || isMarkdownPath(canonical) {
		allowedRoot = validator.rulesRoot
	}
	if !rulesPathWithin(allowedRoot, canonical) {
		return fmt.Errorf("local link %q in %q points outside its permitted root", destination, document)
	}
	return nil
}

func localRulesLinkTarget(document, destination string) (string, bool, error) {
	destination = string(util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations([]byte(destination)))))
	if strings.HasPrefix(destination, "//") {
		return "", false, nil
	}
	if filepath.IsAbs(destination) || filepath.VolumeName(destination) != "" {
		path, err := absoluteRulesLinkPath(destination)
		if err != nil {
			return "", false, err
		}
		return path, true, nil
	}
	parsed, err := url.Parse(destination)
	if err != nil {
		return "", false, err
	}
	if parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, "file") {
		return "", false, nil
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		if strings.EqualFold(parsed.Scheme, "file") {
			return "", false, fmt.Errorf("non-local file URL")
		}
		return "", false, nil
	}
	path := parsed.Path
	if path == "" {
		path = filepath.Base(document)
	}
	if strings.EqualFold(parsed.Scheme, "file") {
		if !filepath.IsAbs(filepath.FromSlash(path)) {
			return "", false, fmt.Errorf("file URL must be absolute")
		}
		return filepath.FromSlash(path), true, nil
	}
	return filepath.Join(filepath.Dir(document), filepath.FromSlash(path)), true, nil
}

func absoluteRulesLinkPath(destination string) (string, error) {
	path, _, _ := strings.Cut(destination, "#")
	path, _, _ = strings.Cut(path, "?")
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(decoded), nil
}

func canonicalRulesDirectory(path, description string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute path", description)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", description, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", description)
	}
	canonical, err := canonicalExistingRulesPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s links: %w", description, err)
	}
	return filepath.Clean(canonical), nil
}

func canonicalReadableRulesFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("target is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	_, readErr := file.Read(make([]byte, 1))
	closeErr := file.Close()
	if readErr != nil && readErr != io.EOF {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	canonical, err := canonicalExistingRulesPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func isMarkdownPath(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}

func rulesPathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func rulesPathKey(path string) string {
	if filepath.Separator == '\\' {
		return strings.ToLower(path)
	}
	return path
}
