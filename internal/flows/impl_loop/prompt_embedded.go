package impl_loop

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed prompts/common.md prompts/roles/*.md prompts/documents/*.md
var embeddedPromptFiles embed.FS

func embeddedRolePrompt(role ResponseRole) (string, error) {
	if _, ok := responseKindsByRole[role]; !ok {
		return "", fmt.Errorf("%w: unsupported role %q", ErrInvalidRoleContext, role)
	}
	specific, err := embeddedPrompt("prompts/roles/" + string(role) + ".md")
	if err != nil {
		return "", err
	}
	var document string
	switch role {
	case ResponseRoleBriefer:
		document = "brief"
	case ResponseRoleTaskReviewer, ResponseRoleFinalReviewer:
		document = "review"
	default:
		return specific, nil
	}
	format, err := embeddedPrompt("prompts/documents/" + document + ".md")
	if err != nil {
		return "", err
	}
	return specific + "\n\n" + format, nil
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
