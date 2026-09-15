package impl_loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
)

const bootstrapContextDocumentBytes = 16 * 1024

var bootstrapSensitiveName = regexp.MustCompile(`(?i)(auth(?:orization)?|token|secret|password|api[_-]?key|credential)`) // names, never values

// BuildBootstrapperProjectContext gathers deterministic, read-only bootstrap
// material. It never invokes a program. The document allowlist is deliberately
// small: deeper investigation belongs to an explicit Explorer request.
func BuildBootstrapperProjectContext(repository string, sources implementationconfig.Sources) (BootstrapProjectContext, error) {
	if !filepath.IsAbs(repository) {
		return BootstrapProjectContext{}, fmt.Errorf("bootstrap project context requires an absolute repository")
	}
	context := BootstrapProjectContext{Repository: filepath.Clean(repository)}
	context.UserSettings = sanitizeBootstrapJSON(sources.User)
	context.ProjectSettings = sanitizeBootstrapJSON(sources.Project)
	for _, name := range []string{"README.md", "go.mod", "package.json", "Makefile", "Taskfile.yml", "Taskfile.yaml", "pyproject.toml", "Cargo.toml"} {
		path := filepath.Join(repository, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			context.ProjectFiles = append(context.ProjectFiles, filepath.ToSlash(name))
		}
	}
	var err error
	context.CI, err = bootstrapDocuments(repository, []string{".github/workflows", ".gitlab-ci.yml", ".circleci/config.yml", "azure-pipelines.yml"})
	if err != nil {
		return BootstrapProjectContext{}, err
	}
	context.Scripts, err = bootstrapDocuments(repository, []string{"scripts", "package.json", "Makefile", "Taskfile.yml", "Taskfile.yaml"})
	if err != nil {
		return BootstrapProjectContext{}, err
	}
	return context, nil
}

func bootstrapDocuments(repository string, candidates []string) ([]BootstrapContextDocument, error) {
	documents := make([]BootstrapContextDocument, 0)
	for _, candidate := range candidates {
		path := filepath.Join(repository, candidate)
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read bootstrap context: %w", err)
		}
		if info.IsDir() {
			err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !entry.Type().IsRegular() {
					return nil
				}
				return appendBootstrapDocument(&documents, repository, current)
			})
			if err != nil {
				return nil, fmt.Errorf("read bootstrap context: %w", err)
			}
			continue
		}
		if err := appendBootstrapDocument(&documents, repository, path); err != nil {
			return nil, err
		}
	}
	sort.Slice(documents, func(left, right int) bool { return documents[left].Path < documents[right].Path })
	return documents, nil
}

func appendBootstrapDocument(documents *[]BootstrapContextDocument, repository, path string) error {
	relative, err := contextRelativePath(repository, path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > bootstrapContextDocumentBytes {
		data = append(append([]byte(nil), data[:bootstrapContextDocumentBytes]...), []byte("\n[truncated by bootstrap context limit]\n")...)
	}
	*documents = append(*documents, BootstrapContextDocument{Path: relative, Content: sanitizeBootstrapText(string(data))})
	return nil
}

// sanitizeBootstrapJSON omits secret-bearing JSON members rather than
// replacing their values. This keeps neither old authorization nor a token
// placeholder in an agent request or error path.
func sanitizeBootstrapJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	value = removeBootstrapSecrets(value)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ""
	}
	return string(encoded)
}

func removeBootstrapSecrets(value any) any {
	switch current := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(current))
		for key, item := range current {
			if bootstrapSensitiveName.MatchString(key) {
				continue
			}
			copy[key] = removeBootstrapSecrets(item)
		}
		return copy
	case []any:
		copy := make([]any, len(current))
		for index, item := range current {
			copy[index] = removeBootstrapSecrets(item)
		}
		return copy
	default:
		return current
	}
}

// sanitizeBootstrapText removes conventional assignment values without
// trying to infer credentials from ordinary prose. It is applied only to the
// controller's bounded CI/script excerpts; agents can still ask Explorer for
// a narrowly scoped, read-only fact when necessary.
func sanitizeBootstrapText(value string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		separator := strings.Index(line, "=")
		if separator < 0 {
			separator = strings.Index(line, ":")
		}
		if separator < 0 || !bootstrapSensitiveName.MatchString(line[:separator]) {
			continue
		}
		lines[index] = line[:separator+1] + " [omitted sensitive value]"
	}
	return strings.Join(lines, "\n")
}
