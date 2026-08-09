package codexexec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxJSONLLineBytes = 16 << 20

type Analysis struct {
	ValidJSONL    bool
	FailureClass  string
	SessionID     *string
	TerminalEvent *string
	Usage         json.RawMessage
	UnknownTypes  []string
}

func ParseJSONL(path string) (Analysis, error) {
	file, err := os.Open(path)
	if err != nil {
		return Analysis{}, err
	}
	defer file.Close()

	analysis := Analysis{ValidJSONL: true}
	sessions := make(map[string]struct{})
	terminals := make(map[string]struct{})
	reader := bufio.NewReaderSize(file, 64<<10)
	for {
		line, newline, tooLong, err := readLine(reader, MaxJSONLLineBytes)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Analysis{}, err
		}
		if tooLong {
			analysis.ValidJSONL = false
			setFailure(&analysis, "jsonl_line_too_long")
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal(line, &event); err != nil || event == nil {
			analysis.ValidJSONL = false
			if !newline {
				setFailure(&analysis, "truncated_jsonl")
			} else {
				setFailure(&analysis, "invalid_jsonl")
			}
			continue
		}
		var eventType string
		if err := json.Unmarshal(event["type"], &eventType); err != nil || eventType == "" {
			analysis.ValidJSONL = false
			setFailure(&analysis, "invalid_jsonl_event")
			continue
		}
		switch eventType {
		case "thread.started":
			var id string
			if err := json.Unmarshal(event["thread_id"], &id); err != nil || strings.TrimSpace(id) == "" {
				setFailure(&analysis, "invalid_thread_started")
				continue
			}
			sessions[id] = struct{}{}
		case "turn.completed", "turn.failed":
			terminals[eventType] = struct{}{}
			if usage := event["usage"]; len(usage) != 0 {
				analysis.Usage = append(analysis.Usage[:0], usage...)
			}
		case "turn.started", "item.started", "item.updated", "item.completed", "error":
		default:
			analysis.UnknownTypes = append(analysis.UnknownTypes, eventType)
		}
	}

	if len(sessions) == 1 {
		for id := range sessions {
			analysis.SessionID = &id
		}
	} else if len(sessions) == 0 {
		setFailure(&analysis, "missing_session_id")
	} else {
		setFailure(&analysis, "conflicting_session_ids")
	}
	if len(terminals) == 1 {
		for terminal := range terminals {
			analysis.TerminalEvent = &terminal
			if terminal == "turn.failed" {
				setFailure(&analysis, "terminal_failure")
			}
		}
	} else if len(terminals) == 0 {
		setFailure(&analysis, "missing_terminal_event")
	} else {
		setFailure(&analysis, "conflicting_terminal_events")
	}
	return analysis, nil
}

func ValidateFinalOutput(path, expectedNonce string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var value struct {
		Result *string `json:"result"`
		Nonce  *string `json:"nonce"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("final output contains trailing data")
	}
	if value.Result == nil || *value.Result != "ok" {
		return errors.New("final output result must be ok")
	}
	if value.Nonce == nil || *value.Nonce == "" || *value.Nonce != expectedNonce {
		return errors.New("final output nonce does not match")
	}
	return nil
}

func classify(result *Result, cfg Config) {
	analysis, err := ParseJSONL(filepath.Join(cfg.ArtifactDir, "stdout.jsonl"))
	if err != nil {
		result.FailureClass = "output_read_failed"
		if result.Detail == "" {
			result.Detail = err.Error()
		}
		return
	}
	result.StdoutValidJSONL = analysis.ValidJSONL
	result.SessionID = analysis.SessionID
	result.TerminalEvent = analysis.TerminalEvent
	result.Usage = analysis.Usage
	finalErr := ValidateFinalOutput(filepath.Join(cfg.ArtifactDir, "last-message.json"), cfg.Nonce)
	result.FinalOutputValid = finalErr == nil

	if result.ProcessExitCode == nil {
		result.FailureClass = "process_not_started"
	} else if *result.ProcessExitCode != 0 {
		result.FailureClass = "process_failure"
	} else if result.FailureClass == "" {
		if analysis.FailureClass != "" {
			result.FailureClass = analysis.FailureClass
		} else if finalErr != nil {
			result.FailureClass = "invalid_final_output"
		} else {
			result.Outcome = Pass
		}
	}
	if result.Detail == "" && result.FailureClass != "" {
		result.Detail = result.FailureClass
	}
}

func setFailure(analysis *Analysis, class string) {
	if analysis.FailureClass == "" {
		analysis.FailureClass = class
	}
}

func readLine(reader *bufio.Reader, limit int) ([]byte, bool, bool, error) {
	line := make([]byte, 0, min(limit, 64<<10))
	tooLong := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(fragment) > limit+1 {
				line = nil
				tooLong = true
			} else {
				line = append(line, fragment...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, false, err
		}
		if errors.Is(err, io.EOF) && len(fragment) == 0 && len(line) == 0 && !tooLong {
			return nil, false, false, io.EOF
		}
		newline := err == nil
		if !tooLong && newline {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
		}
		if !tooLong && len(line) > limit {
			line = nil
			tooLong = true
		}
		return line, newline, tooLong, nil
	}
}
