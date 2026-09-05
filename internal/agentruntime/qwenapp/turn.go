package qwenapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

var ErrRepairExhausted = agentruntime.ErrStructuredResponse

type turnRunner struct {
	connection   *Connection
	role         string
	schema       json.RawMessage
	context      sessionPromptContext
	bootstrapped bool
	mu           sync.Mutex
}

func newTurnRunner(connection *Connection, config agentruntime.ThreadConfig) (*turnRunner, error) {
	if connection == nil {
		return nil, errors.New("Qwen connection is required")
	}
	config = config.Clone()
	config.BootstrapInstructions = strings.Clone(config.BootstrapInstructions)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := validateSchemaObject(config.OutputSchema); err != nil {
		return nil, err
	}
	connection.mu.Lock()
	if connection.err != nil || !connection.ready || connection.sessionID == "" {
		err := connection.err
		connection.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrConnectionClosed
	}
	context := newSessionPromptContext(connection.filePolicy)
	connection.mu.Unlock()
	if context.Workspace == "" || config.Workspace != context.Workspace || config.ArtifactRoot != context.ArtifactRoot {
		return nil, errors.New("thread context does not match Qwen connection roots")
	}
	return &turnRunner{
		connection: connection,
		role:       config.BootstrapInstructions,
		schema:     append(json.RawMessage(nil), config.OutputSchema...),
		context:    context,
	}, nil
}

func validateSchemaObject(schema json.RawMessage) error {
	if _, err := strictJSONObject(schema); err != nil {
		return errors.New("output schema must be one JSON object")
	}
	return nil
}

func (runner *turnRunner) run(prompt string) (json.RawMessage, error) {
	if runner == nil || runner.connection == nil {
		return nil, errors.New("invalid Qwen turn runner")
	}
	if prompt == "" {
		return nil, errors.New("turn prompt is required")
	}
	if !runner.mu.TryLock() {
		return nil, agentruntime.ErrTurnInProgress
	}
	defer runner.mu.Unlock()

	first := !runner.bootstrapped
	nextPrompt := strings.Clone(prompt)
	if first {
		nextPrompt = firstPrompt(runner.role, runner.schema, runner.context, prompt)
	}
	for attempt := 0; attempt < agentruntime.DefaultRetryLimit; attempt++ {
		assembler := newResponseAssembler()
		var terminal promptResponse
		var output json.RawMessage
		err := runner.connection.callPrompt(sessionPromptParams{
			SessionID: runner.connection.SessionID(),
			Prompt:    []promptContent{{Type: "text", Text: nextPrompt}},
		}, &terminal, assembler, func() error {
			if terminal.StopReason == "cancelled" {
				return agentruntime.ErrTurnInterrupted
			}
			if terminal.StopReason != "end_turn" {
				return fmt.Errorf("Qwen turn stopped before a final response: %s", safeStopReason(terminal.StopReason))
			}
			var candidateErr error
			output, candidateErr = validateCandidate(runner.schema, assembler.output())
			return candidateErr
		})
		if first {
			// Mark the one-time bootstrap only after the first prompt has reached
			// a terminal outcome. A local busy rejection did not send it.
			runner.bootstrapped = !errors.Is(err, agentruntime.ErrTurnInProgress)
			first = false
		}
		var candidateErr candidateError
		if err == nil {
			return output, nil
		}
		if !errors.As(err, &candidateErr) {
			return nil, err
		}
		if attempt+1 == agentruntime.DefaultRetryLimit {
			return nil, fmt.Errorf("%w: %w after %d responses", ErrProtocol, ErrRepairExhausted, agentruntime.DefaultRetryLimit)
		}
		nextPrompt = repairPrompt(runner.schema, safeCandidateDiagnostic(err))
	}
	panic("unreachable repair loop")
}

func safeStopReason(reason string) string {
	switch reason {
	case "max_tokens", "max_turn_requests", "refusal":
		return reason
	default:
		return "unknown"
	}
}
