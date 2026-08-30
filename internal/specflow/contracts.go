package specflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type FeatureIDResult struct {
	FeatureID string `json:"feature_id"`
}
type Kind string

const (
	KindMessage  Kind = "message"
	KindArtifact Kind = "artifact"
	// KindDraft is accepted only as a transition aid for the intent-only flow.
	// DecodeEnvelope always normalizes it to KindArtifact.
	KindDraft Kind = "draft"
)

type DecisionAuthor string

const (
	DecisionUser  DecisionAuthor = "user"
	DecisionAgent DecisionAuthor = "agent"
)

type Decision struct {
	Author       DecisionAuthor `json:"author"`
	Decision     string         `json:"decision"`
	Rationale    string         `json:"rationale"`
	Alternatives []string       `json:"alternatives"`
	Supersedes   []int          `json:"supersedes"`
}
type Envelope struct {
	Kind      Kind       `json:"kind"`
	Message   string     `json:"message"`
	Decisions []Decision `json:"decisions"`
}

const featureIDSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"feature_id":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$"}},"required":["feature_id"],"additionalProperties":false}`
const decisionSchema = `{"type":"object","properties":{"author":{"enum":["user","agent"]},"decision":{"type":"string","minLength":1},"rationale":{"type":"string","minLength":1},"alternatives":{"type":"array","items":{"type":"string"}},"supersedes":{"type":"array","items":{"type":"integer","minimum":1}}},"required":["author","decision","rationale","alternatives","supersedes"],"additionalProperties":false}`

// Codex App Server rejects union keywords in response schemas. The flat schema
// requires every transport property; DecodeEnvelope applies kind-specific
// domain invariants after the provider returns its JSON object.
const dialogueSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"kind":{"enum":["message","artifact"]},"message":{"type":"string"},"decisions":{"type":"array","items":` + decisionSchema + `}},"required":["kind","message","decisions"],"additionalProperties":false}`

func FeatureIDSchema() json.RawMessage { return json.RawMessage(featureIDSchema) }
func DialogueSchema() json.RawMessage  { return json.RawMessage(dialogueSchema) }

// FlowEnvelopeSchema is the wider immutable schema required by runtimes whose
// SDK configures JSON Schema once per client (Claude). Specflow still validates
// each thread against its narrow FeatureIDSchema or DialogueSchema.
func FlowEnvelopeSchema() json.RawMessage {
	// Claude configures one schema per client. Keep all local references rooted
	// in this document: embedding dialogueSchema directly would leave its
	// #/$defs/decision reference pointing at a non-existent outer definition.
	return json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","oneOf":[` + featureIDSchema + `,` + dialogueSchema + `],"$defs":{"decision":` + decisionSchema + `}}`)
}

func DecodeFeatureID(data []byte) (FeatureIDResult, error) {
	var value struct {
		FeatureID string `json:"feature_id"`
	}
	if err := decodeExact(data, &value); err != nil {
		return FeatureIDResult{}, err
	}
	if err := ValidateFeatureID(value.FeatureID); err != nil {
		return FeatureIDResult{}, err
	}
	return FeatureIDResult(value), nil
}
func DecodeEnvelope(data []byte) (Envelope, error) {
	var raw struct {
		Kind      Kind        `json:"kind"`
		Message   *string     `json:"message"`
		Decisions *[]Decision `json:"decisions"`
	}
	if err := decodeExact(data, &raw); err != nil {
		return Envelope{}, err
	}
	if raw.Decisions == nil {
		return Envelope{}, fmt.Errorf("decisions is required")
	}
	value := Envelope{Kind: raw.Kind, Decisions: *raw.Decisions}
	switch raw.Kind {
	case KindMessage:
		if raw.Message == nil || strings.TrimSpace(*raw.Message) == "" {
			return Envelope{}, fmt.Errorf("message kind requires a non-empty message")
		}
		value.Message = *raw.Message
	case KindArtifact:
		if raw.Message == nil {
			return Envelope{}, fmt.Errorf("artifact kind requires message transport placeholder")
		}
		if *raw.Message != "" {
			return Envelope{}, fmt.Errorf("artifact kind requires an empty message transport placeholder")
		}
		value.Kind = KindArtifact
		value.Message = ""
	case KindDraft:
		// Temporary compatibility for artifacts emitted by the original intent
		// prompt. New provider schemas cannot emit this shape.
		if raw.Message != nil && *raw.Message != "" {
			return Envelope{}, fmt.Errorf("legacy draft kind forbids message")
		}
		value.Kind = KindArtifact
		value.Message = ""
	default:
		return Envelope{}, fmt.Errorf("unknown kind %q", raw.Kind)
	}
	for i, decision := range value.Decisions {
		if err := validateDecision(decision); err != nil {
			return Envelope{}, fmt.Errorf("decision %d: %w", i+1, err)
		}
	}
	return value, nil
}
func decodeExact(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode structured result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode structured result: trailing JSON")
	}
	return nil
}
func validateDecision(value Decision) error {
	if value.Author != DecisionUser && value.Author != DecisionAgent {
		return fmt.Errorf("invalid author %q", value.Author)
	}
	if strings.TrimSpace(value.Decision) == "" || strings.TrimSpace(value.Rationale) == "" {
		return fmt.Errorf("decision and rationale must not be empty")
	}
	if value.Alternatives == nil || value.Supersedes == nil {
		return fmt.Errorf("alternatives and supersedes are required")
	}
	return nil
}

func BootstrapPrompt(brief, artifactRoot string) string {
	runtimeContext := "Artifact root: " + artifactRoot + "\n\nFeature brief:\n" + brief
	prompt, err := NewEmbeddedPromptCatalog().Compose(RoleIntentAuthor, runtimeContext)
	if err != nil {
		panic("compose embedded intent prompt: " + err.Error())
	}
	return prompt
}

func FeatureIDPrompt(brief string) string {
	return "Generate only the JSON object required by the configured schema. " +
		"Choose a short lowercase hyphenated feature_id that reflects the semantic intent of this feature brief, not generic words such as feature, change, or update.\n\n" +
		"FEATURE BRIEF:\n" + brief
}
