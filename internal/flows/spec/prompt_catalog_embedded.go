package specflow

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed prompts/system/*.md prompts/roles/*.md prompts/capabilities/*/*.md
var embeddedPromptFiles embed.FS

type embeddedPromptCatalog struct {
	files fs.FS
}

// NewEmbeddedPromptCatalog returns the immutable defaults. The concrete
// adapter stays private so callers only depend on PromptCatalog.
func NewEmbeddedPromptCatalog() PromptCatalog {
	return &embeddedPromptCatalog{files: embeddedPromptFiles}
}

func (c *embeddedPromptCatalog) Resolve(id PromptID) (string, error) {
	if !id.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidPromptID, id)
	}
	data, err := fs.ReadFile(c.files, "prompts/"+id.String()+".md")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrPromptNotFound, id)
		}
		return "", fmt.Errorf("resolve prompt %s: %w", id, err)
	}
	fragment := strings.TrimSpace(string(data))
	if fragment == "" {
		return "", fmt.Errorf("resolve prompt %s: empty embedded fragment", id)
	}
	return fragment, nil
}

func (c *embeddedPromptCatalog) Compose(role Role, runtimeContext string) (string, error) {
	return composePrompt(c, role, runtimeContext)
}
