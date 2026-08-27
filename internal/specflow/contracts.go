package specflow

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Status string

const (
	StatusWritten       Status = "WRITTEN"
	StatusAnswered      Status = "ANSWERED"
	StatusNeedsInput    Status = "NEEDS_INPUT"
	StatusReadyToUpdate Status = "READY_TO_UPDATE"
	StatusUpdated       Status = "UPDATED"
)

type Result struct {
	Status  Status
	Message string
	SpecID  string
}

// promptFiles keeps the built-in project prompt templates close to the flow.
// They are Markdown files so a project can copy and customize them without
// editing Go prompt composition.
//
//go:embed prompts/*.md
var promptFiles embed.FS

func InitialPrompt(brief, absoluteFeaturesDirectory string) string {
	return renderPrompt(
		promptPart{"initial-system.md", map[string]string{"features_directory": absoluteFeaturesDirectory}},
		promptPart{"initial-user.md", map[string]string{"brief": brief}},
	)
}

func ChangePrompt(request string) string {
	return renderPrompt(
		promptPart{"change-system.md", nil},
		promptPart{"change-user.md", map[string]string{"change_request": request}},
	)
}

func UpdatePrompt(absoluteSpecDirectory string) string {
	return renderPrompt(promptPart{"update-system.md", map[string]string{"spec_directory": absoluteSpecDirectory}})
}

func QuestionPrompt(question string) string {
	return renderPrompt(
		promptPart{"question-system.md", nil},
		promptPart{"question-user.md", map[string]string{"question": question}},
	)
}

type promptPart struct {
	name   string
	values map[string]string
}

func renderPrompt(templates ...promptPart) string {
	parts := make([]string, 0, len(templates))
	for _, template := range templates {
		content, err := promptFiles.ReadFile("prompts/" + template.name)
		if err != nil {
			panic("read embedded prompt " + template.name + ": " + err.Error())
		}
		text := string(content)
		for placeholder, value := range template.values {
			text = strings.ReplaceAll(text, "{{"+placeholder+"}}", value)
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.Join(parts, "\n\n")
}

func InitialSchema() json.RawMessage  { return json.RawMessage(initialSchema) }
func ChangeSchema() json.RawMessage   { return json.RawMessage(changeSchema) }
func UpdateSchema() json.RawMessage   { return json.RawMessage(updateSchema) }
func QuestionSchema() json.RawMessage { return json.RawMessage(questionSchema) }

// FlowEnvelopeSchema is the immutable union accepted by a long-lived Claude
// client. Individual stages keep using their narrower schemas and decoders.
func FlowEnvelopeSchema() json.RawMessage { return append(json.RawMessage(nil), flowEnvelopeSchema...) }

const initialSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["WRITTEN"]},"feature_id":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9-]+$"}},"required":["status","feature_id"],"additionalProperties":false}`
const changeSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["NEEDS_INPUT","READY_TO_UPDATE"]},"message":{"type":"string"}},"required":["status","message"],"additionalProperties":false}`
const updateSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["UPDATED"]}},"required":["status"],"additionalProperties":false}`
const questionSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"status":{"type":"string","enum":["ANSWERED"]},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false}`
const flowEnvelopeSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[{"type":"object","properties":{"status":{"const":"WRITTEN"},"feature_id":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9-]+$"}},"required":["status","feature_id"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"ANSWERED"},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"NEEDS_INPUT"},"message":{"type":"string","minLength":1}},"required":["status","message"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"READY_TO_UPDATE"}},"required":["status"],"additionalProperties":false},{"type":"object","properties":{"status":{"const":"UPDATED"}},"required":["status"],"additionalProperties":false}]}`

func DecodeInitialResult(data []byte) (Result, error) {
	return decodeResult(data, StatusWritten)
}

func DecodeChangeResult(data []byte) (Result, error) {
	return decodeResult(data, StatusNeedsInput, StatusReadyToUpdate)
}

func DecodeUpdateResult(data []byte) (Result, error) {
	return decodeResult(data, StatusUpdated)
}

func DecodeQuestionResult(data []byte) (Result, error) {
	return decodeResult(data, StatusAnswered)
}

func DecodeFlowEnvelope(data []byte) (Result, error) {
	return decodeResult(data, StatusWritten, StatusAnswered, StatusNeedsInput, StatusReadyToUpdate, StatusUpdated)
}

func decodeResult(data []byte, allowed ...Status) (Result, error) {
	var envelope struct {
		Status    Status          `json:"status"`
		Message   json.RawMessage `json:"message"`
		FeatureID json.RawMessage `json:"feature_id"`
		SpecID    json.RawMessage `json:"spec_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Result{}, fmt.Errorf("decode structured result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Result{}, fmt.Errorf("decode structured result: trailing JSON")
	}
	if !containsStatus(allowed, envelope.Status) {
		return Result{}, fmt.Errorf("invalid status %q", envelope.Status)
	}

	result := Result{Status: envelope.Status}
	switch result.Status {
	case StatusNeedsInput, StatusAnswered:
		if len(envelope.Message) == 0 {
			return Result{}, fmt.Errorf("status %s requires a non-empty message", result.Status)
		}
		if err := json.Unmarshal(envelope.Message, &result.Message); err != nil || strings.TrimSpace(result.Message) == "" {
			return Result{}, fmt.Errorf("status %s requires a non-empty message", result.Status)
		}
		if len(envelope.FeatureID) != 0 || len(envelope.SpecID) != 0 {
			var unused string
			identifier := envelope.FeatureID
			if len(identifier) == 0 {
				identifier = envelope.SpecID
			}
			if err := json.Unmarshal(identifier, &unused); err != nil || unused != "" {
				return Result{}, fmt.Errorf("status %s requires an empty feature_id", result.Status)
			}
		}
	case StatusWritten:
		if len(envelope.FeatureID) == 0 {
			return Result{}, fmt.Errorf("status %s requires feature_id", result.Status)
		}
		if err := json.Unmarshal(envelope.FeatureID, &result.SpecID); err != nil {
			return Result{}, fmt.Errorf("status %s requires a string feature_id", result.Status)
		}
		if err := ValidateSpecID(result.SpecID); err != nil {
			return Result{}, fmt.Errorf("status %s: %w", result.Status, err)
		}
		if len(envelope.Message) != 0 || len(envelope.SpecID) != 0 {
			return Result{}, fmt.Errorf("status %s forbids message and spec_id", result.Status)
		}
	case StatusReadyToUpdate:
		if len(envelope.FeatureID) != 0 || len(envelope.SpecID) != 0 {
			return Result{}, fmt.Errorf("status %s forbids feature_id", result.Status)
		}
		if len(envelope.Message) != 0 {
			var unused string
			if err := json.Unmarshal(envelope.Message, &unused); err != nil || unused != "" {
				return Result{}, fmt.Errorf("status %s requires an empty message", result.Status)
			}
		}
	default:
		if len(envelope.Message) != 0 || len(envelope.FeatureID) != 0 || len(envelope.SpecID) != 0 {
			return Result{}, fmt.Errorf("status %s forbids message and feature_id", result.Status)
		}
	}
	return result, nil
}

func containsStatus(allowed []Status, status Status) bool {
	for _, candidate := range allowed {
		if candidate == status {
			return true
		}
	}
	return false
}
