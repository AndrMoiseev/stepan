package nessyapp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

type responseAssembler struct {
	turn      turnIdentity
	messageID string
	hasID     bool
	noID      bool
	closed    bool
	buffer    bytes.Buffer
}

func newResponseAssembler() *responseAssembler { return &responseAssembler{} }

type turnIdentity struct {
	sessionID string
	promptID  string
}

func (identity turnIdentity) valid() bool { return identity.sessionID != "" && identity.promptID != "" }

func (assembler *responseAssembler) bind(identity turnIdentity) error {
	if assembler == nil || assembler.turn.valid() || !identity.valid() {
		return fmt.Errorf("%w: invalid response assembler binding", ErrProtocol)
	}
	assembler.turn = identity
	return nil
}

func (assembler *responseAssembler) observe(identity turnIdentity, raw json.RawMessage) error {
	if assembler == nil || assembler.turn != identity || !identity.valid() || assembler.closed {
		return fmt.Errorf("%w: foreign or late assistant content", ErrProtocol)
	}
	var update struct {
		Kind      string          `json:"sessionUpdate"`
		MessageID *string         `json:"messageId,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
	}
	if json.Unmarshal(raw, &update) != nil || update.Kind == "" {
		return fmt.Errorf("%w: malformed assistant content", ErrProtocol)
	}
	if update.Kind != "agent_message_chunk" {
		return nil
	}
	var content struct {
		Type string  `json:"type"`
		Text *string `json:"text,omitempty"`
	}
	if json.Unmarshal(update.Content, &content) != nil || content.Type == "" {
		return fmt.Errorf("%w: malformed assistant content", ErrProtocol)
	}
	if !knownContentType(content.Type) {
		return fmt.Errorf("%w: unknown assistant content type", ErrProtocol)
	}
	if update.MessageID != nil {
		if *update.MessageID == "" || assembler.noID || assembler.hasID && assembler.messageID != *update.MessageID {
			return fmt.Errorf("%w: multiple or ambiguous assistant messages", ErrProtocol)
		}
		assembler.hasID = true
		assembler.messageID = *update.MessageID
	} else {
		if assembler.hasID {
			return fmt.Errorf("%w: multiple or ambiguous assistant messages", ErrProtocol)
		}
		assembler.noID = true
	}
	if content.Type != "text" {
		return nil
	}
	if content.Text == nil || !utf8.ValidString(*content.Text) {
		return fmt.Errorf("%w: malformed assistant text", ErrProtocol)
	}
	if assembler.buffer.Len()+len(*content.Text) > maxACPMessageBytes {
		return fmt.Errorf("%w: oversized assistant response", ErrProtocol)
	}
	_, _ = assembler.buffer.WriteString(*content.Text)
	return nil
}

func (assembler *responseAssembler) terminal(identity turnIdentity) error {
	if assembler == nil || assembler.turn != identity || !identity.valid() || assembler.closed {
		return fmt.Errorf("%w: duplicate or foreign assistant terminal", ErrProtocol)
	}
	assembler.closed = true
	return nil
}

func (assembler *responseAssembler) output() json.RawMessage {
	if assembler == nil || !assembler.closed {
		return nil
	}
	return append(json.RawMessage(nil), assembler.buffer.Bytes()...)
}

type candidateError struct{ schema bool }

func (err candidateError) Error() string {
	if err.schema {
		return "response does not match the required schema"
	}
	return "response is not exactly one JSON object"
}

func validateCandidate(schema, candidate json.RawMessage) (json.RawMessage, error) {
	data, err := strictJSONObject(candidate)
	if err != nil {
		return nil, candidateError{}
	}
	if err := agentruntime.ValidateOutput(schema, data); err != nil {
		return nil, candidateError{schema: true}
	}
	return append(json.RawMessage(nil), data...), nil
}

func strictJSONObject(raw []byte) (json.RawMessage, error) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 || data[0] != '{' || !utf8.Valid(data) || validateJSONObject(data) != nil {
		return nil, errors.New("value must be exactly one UTF-8 JSON object")
	}
	return append(json.RawMessage(nil), data...), nil
}

func safeCandidateDiagnostic(err error) string {
	var candidate candidateError
	if errors.As(err, &candidate) && candidate.schema {
		return "the JSON object did not match the required schema"
	}
	return "the response was not exactly one valid JSON object"
}
