package qwenapp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONContract is the process-wide part of the structured response contract.
// The concrete schema and role remain session-scoped and are supplied by the
// first prompt and repair prompts.
const JSONContract = "For every completed turn, return exactly one UTF-8 JSON object that conforms to the Stepan schema supplied in the session prompt. Do not use Markdown fences, prefixes, suffixes, commentary, or any text outside that object."

type promptContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sessionPromptParams struct {
	SessionID string          `json:"sessionId"`
	Prompt    []promptContent `json:"prompt"`
}

type sessionPromptContext struct {
	Workspace    string   `json:"workspace"`
	ArtifactRoot string   `json:"artifactRoot"`
	Readable     []string `json:"readableRoots"`
	Writable     []string `json:"writableRoots"`
}

func newSessionPromptContext(policy filePolicy) sessionPromptContext {
	context := sessionPromptContext{
		Workspace:    strings.Clone(policy.workspaceRoot),
		ArtifactRoot: strings.Clone(policy.writableRoot),
		Readable:     append([]string(nil), policy.readRoots...),
	}
	if policy.writableRoot != "" {
		context.Writable = []string{strings.Clone(policy.writableRoot)}
	} else {
		context.Writable = []string{}
	}
	return context
}

func firstPrompt(role string, schema json.RawMessage, context sessionPromptContext, input string) string {
	contextJSON, _ := json.Marshal(context)
	return fmt.Sprintf("Stepan role instructions:\n%s\n\nStepan immutable session context:\n%s\n\nStepan output schema (exact JSON Schema):\n%s\n\nUser request:\n%s",
		role, contextJSON, schema, input)
}

func repairPrompt(schema json.RawMessage, diagnostic string) string {
	return fmt.Sprintf("Your previous response could not be accepted (%s). Return a corrected response as exactly one JSON object and no other text. It must conform to this exact JSON Schema:\n%s",
		diagnostic, schema)
}
