package qwenapp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxACPMessageBytes = 16 << 20

var (
	errInvalidNDJSON      = errors.New("invalid ACP NDJSON")
	errTruncatedNDJSON    = errors.New("truncated ACP NDJSON line")
	errNDJSONLineTooLong  = errors.New("ACP NDJSON line exceeds 16 MiB")
	errInvalidEnvelope    = errors.New("invalid JSON-RPC envelope")
	errDuplicateRequestID = errors.New("duplicate request ID")
	errOrphanResponse     = errors.New("response has no pending request")
	errDuplicateResponse  = errors.New("request already has a response")
)

type lineDecoder struct {
	reader *bufio.Reader
}

func newLineDecoder(reader io.Reader) *lineDecoder {
	return &lineDecoder{reader: bufio.NewReaderSize(reader, 64<<10)}
}

func (decoder *lineDecoder) decode() (message, error) {
	line, err := decoder.readLine()
	if err != nil {
		return message{}, err
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return message{}, fmt.Errorf("%w: empty line", errInvalidNDJSON)
	}
	return parseMessage(line)
}

func (decoder *lineDecoder) readLine() ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	tooLong := false
	for {
		fragment, err := decoder.reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(fragment) > maxACPMessageBytes+2 {
				tooLong = true
				line = nil
			} else {
				line = append(line, fragment...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if errors.Is(err, io.EOF) {
			if tooLong {
				return nil, errNDJSONLineTooLong
			}
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errTruncatedNDJSON
		}
		if tooLong {
			return nil, errNDJSONLineTooLong
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > maxACPMessageBytes {
			return nil, errNDJSONLineTooLong
		}
		return line, nil
	}
}

func parseMessage(line []byte) (message, error) {
	if !utf8.Valid(line) {
		return message{}, fmt.Errorf("%w: line is not UTF-8", errInvalidNDJSON)
	}
	if err := validateJSONObject(line); err != nil {
		return message{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		return message{}, fmt.Errorf("%w: malformed object", errInvalidNDJSON)
	}
	for name := range fields {
		switch name {
		case "jsonrpc", "id", "method", "params", "result", "error":
		default:
			return message{}, fmt.Errorf("%w: unknown envelope member", errInvalidEnvelope)
		}
	}
	var version string
	if raw, ok := fields["jsonrpc"]; !ok || json.Unmarshal(raw, &version) != nil || version != "2.0" {
		return message{}, fmt.Errorf("%w: jsonrpc must be 2.0", errInvalidEnvelope)
	}

	methodRaw, hasMethod := fields["method"]
	idRaw, hasID := fields["id"]
	result, hasResult := fields["result"]
	errorRaw, hasError := fields["error"]
	params, hasParams := fields["params"]
	parsed := message{}
	if hasMethod {
		if json.Unmarshal(methodRaw, &parsed.method) != nil || parsed.method == "" {
			return message{}, fmt.Errorf("%w: method must be a non-empty string", errInvalidEnvelope)
		}
		if hasResult || hasError || !hasParams {
			return message{}, fmt.Errorf("%w: method message requires params and forbids result/error", errInvalidEnvelope)
		}
		parsed.params = append(json.RawMessage(nil), params...)
		if !hasID {
			parsed.kind = notificationMessage
			return parsed, nil
		}
		id, err := parseRequestID(idRaw)
		if err != nil {
			return message{}, err
		}
		parsed.kind, parsed.id = requestMessage, id
		return parsed, nil
	}
	if hasParams || !hasID || hasResult == hasError {
		return message{}, fmt.Errorf("%w: response requires id and exactly one of result/error", errInvalidEnvelope)
	}
	id, err := parseRequestID(idRaw)
	if err != nil {
		return message{}, err
	}
	parsed.kind, parsed.id = responseMessage, id
	if hasResult {
		parsed.result = append(json.RawMessage(nil), result...)
		return parsed, nil
	}
	var decoded struct {
		Code    *int64          `json:"code"`
		Message *string         `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(errorRaw, &decoded) != nil || decoded.Code == nil || decoded.Message == nil || *decoded.Message == "" {
		return message{}, fmt.Errorf("%w: malformed error response", errInvalidEnvelope)
	}
	parsed.err = &rpcError{Code: *decoded.Code, Message: *decoded.Message, Data: decoded.Data}
	return parsed, nil
}

func validateJSONObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateJSONValue(decoder, true); err != nil {
		return fmt.Errorf("%w: malformed or ambiguous JSON", errInvalidNDJSON)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing transport data", errInvalidNDJSON)
	}
	return nil
}

func validateJSONValue(decoder *json.Decoder, root bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		if root {
			return errors.New("root is not an object")
		}
		return nil
	}
	if root && delimiter != '{' {
		return errors.New("root is not an object")
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return errors.New("duplicate object key")
			}
			seen[name] = struct{}{}
			if err := validateJSONValue(decoder, false); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := validateJSONValue(decoder, false); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected delimiter")
	}
	return nil
}

func parseRequestID(raw json.RawMessage) (requestID, error) {
	if len(raw) == 0 {
		return requestID{}, fmt.Errorf("%w: missing request ID", errInvalidEnvelope)
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return requestID{}, fmt.Errorf("%w: invalid string request ID", errInvalidEnvelope)
		}
		return stringID(value), nil
	}
	text := string(raw)
	if strings.ContainsAny(text, ".eE") {
		return requestID{}, fmt.Errorf("%w: request ID must be string or int64", errInvalidEnvelope)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return requestID{}, fmt.Errorf("%w: request ID must be string or int64", errInvalidEnvelope)
	}
	return integerID(value), nil
}

type lineWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (writer *lineWriter) write(value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(line) > maxACPMessageBytes {
		return errNDJSONLineTooLong
	}
	line = append(line, '\n')
	writer.mu.Lock()
	defer writer.mu.Unlock()
	for len(line) > 0 {
		written, err := writer.writer.Write(line)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		line = line[written:]
	}
	return nil
}

type correlationTable struct {
	entries map[string]bool
}

func (table *correlationTable) register(id requestID) error {
	if table.entries == nil {
		table.entries = make(map[string]bool)
	}
	if _, exists := table.entries[id.key]; exists {
		return errDuplicateRequestID
	}
	table.entries[id.key] = false
	return nil
}

func (table *correlationTable) resolve(id requestID) error {
	done, exists := table.entries[id.key]
	if !exists {
		return errOrphanResponse
	}
	if done {
		return errDuplicateResponse
	}
	table.entries[id.key] = true
	return nil
}

type transport struct {
	decoder *lineDecoder
	writer  *lineWriter

	mu     sync.Mutex
	local  correlationTable
	remote correlationTable
}

func newTransport(reader io.Reader, writer io.Writer) *transport {
	return &transport{decoder: newLineDecoder(reader), writer: &lineWriter{writer: writer}}
}

func (transport *transport) read() (message, error) {
	parsed, err := transport.decoder.decode()
	if err != nil {
		return message{}, err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	switch parsed.kind {
	case requestMessage:
		err = transport.remote.register(parsed.id)
	case responseMessage:
		err = transport.local.resolve(parsed.id)
	}
	if err != nil {
		return message{}, err
	}
	return parsed, nil
}

func (transport *transport) sendRequest(id requestID, method string, params any) error {
	if method == "" {
		return fmt.Errorf("%w: empty method", errInvalidEnvelope)
	}
	if params == nil {
		params = struct{}{}
	}
	transport.mu.Lock()
	if err := transport.local.register(id); err != nil {
		transport.mu.Unlock()
		return err
	}
	transport.mu.Unlock()
	return transport.writer.write(struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      requestID `json:"id"`
		Method  string    `json:"method"`
		Params  any       `json:"params"`
	}{"2.0", id, method, params})
}

func (transport *transport) sendNotification(method string, params any) error {
	if method == "" {
		return fmt.Errorf("%w: empty method", errInvalidEnvelope)
	}
	if params == nil {
		params = struct{}{}
	}
	return transport.writer.write(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}

func (transport *transport) sendResult(id requestID, result any) error {
	transport.mu.Lock()
	if err := transport.remote.resolve(id); err != nil {
		transport.mu.Unlock()
		return err
	}
	transport.mu.Unlock()
	return transport.writer.write(struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      requestID `json:"id"`
		Result  any       `json:"result"`
	}{"2.0", id, result})
}

func (transport *transport) sendError(id requestID, code int64, text string) error {
	if text == "" {
		return fmt.Errorf("%w: empty error message", errInvalidEnvelope)
	}
	transport.mu.Lock()
	if err := transport.remote.resolve(id); err != nil {
		transport.mu.Unlock()
		return err
	}
	transport.mu.Unlock()
	return transport.writer.write(struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      requestID `json:"id"`
		Error   rpcError  `json:"error"`
	}{"2.0", id, rpcError{Code: code, Message: text}})
}
