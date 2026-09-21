package impl_loop

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed prompts/common.md prompts/roles/*.md
var embeddedPromptFiles embed.FS

func embeddedRolePrompt(role ResponseRole) (string, error) {
	if _, ok := responseKindsByRole[role]; !ok {
		return "", fmt.Errorf("%w: unsupported role %q", ErrInvalidRoleContext, role)
	}
	return embeddedPrompt("prompts/roles/" + string(role) + ".md")
}

func embeddedPrompt(path string) (string, error) {
	content, err := embeddedPromptFiles.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read implementation prompt %s: %w", path, err)
	}
	prompt := strings.TrimSpace(string(content))
	if prompt == "" {
		return "", fmt.Errorf("implementation prompt %s is empty", path)
	}
	return prompt, nil
}
