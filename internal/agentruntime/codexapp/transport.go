package codexapp

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

const MaxMessageBytes = 16 << 20

var (
	ErrInvalidJSONL       = errors.New("invalid JSONL")
	ErrTruncatedJSONL     = errors.New("truncated JSONL line")
	ErrJSONLLineTooLong   = errors.New("JSONL line exceeds 16 MiB")
	ErrInvalidEnvelope    = errors.New("invalid JSON-RPC envelope")
	ErrDuplicateRequestID = errors.New("duplicate request ID")
	ErrOrphanResponse     = errors.New("response has no pending request")
	ErrDuplicateResponse  = errors.New("request already has a response")
)

type MessageKind uint8

const (
	Request MessageKind = iota + 1
	Response
	Notification
)

// ID is the string-or-int64 request identifier allowed by the generated schema.
// Its key is type tagged so, for example, 1 and "1" never correlate.
type ID struct {
	raw json.RawMessage
	key string
}

func StringID(value string) ID {
	raw, _ := json.Marshal(value)
	return ID{raw: raw, key: "s:" + value}
}

func IntID(value int64) ID {
	text := strconv.FormatInt(value, 10)
	return ID{raw: json.RawMessage(text), key: "i:" + text}
}

func (id ID) Key() string { return id.key }

func (id ID) MarshalJSON() ([]byte, error) {
	if id.key == "" {
		return nil, errors.New("empty request ID")
	}
	return append([]byte(nil), id.raw...), nil
}

type RPCError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type Message struct {
	Kind   MessageKind
	Method string
	ID     ID
	Params json.RawMessage
	Result json.RawMessage
	Error  *RPCError
	Raw    json.RawMessage
}

// Decoder reads the stdio transport: one non-empty UTF-8 JSON object per line.
type Decoder struct {
	reader *bufio.Reader
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReaderSize(reader, 64<<10)}
}

func (decoder *Decoder) Decode() (Message, error) {
	for {
		line, err := decoder.readLine()
		if err != nil {
			return Message{}, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		return ParseMessage(line)
	}
}

func (decoder *Decoder) readLine() ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	tooLong := false
	for {
		fragment, err := decoder.reader.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(fragment) > MaxMessageBytes+2 {
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
				return nil, ErrJSONLLineTooLong
			}
			if len(line) == 0 {
				return nil, io.EOF
			}
			if len(bytes.TrimSpace(line)) == 0 {
				return line, nil
			}
			return nil, ErrTruncatedJSONL
		}
		if tooLong {
			return nil, ErrJSONLLineTooLong
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > MaxMessageBytes {
			return nil, ErrJSONLLineTooLong
		}
		return line, nil
	}
}

func ParseMessage(line []byte) (Message, error) {
	if !utf8.Valid(line) {
		return Message{}, fmt.Errorf("%w: line is not UTF-8", ErrInvalidJSONL)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil || fields == nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidJSONL, err)
	}

	message := Message{Raw: append(json.RawMessage(nil), line...)}
	methodRaw, hasMethod := fields["method"]
	idRaw, hasID := fields["id"]
	result, hasResult := fields["result"]
	errorRaw, hasError := fields["error"]

	if hasMethod {
		if err := json.Unmarshal(methodRaw, &message.Method); err != nil || message.Method == "" {
			return Message{}, fmt.Errorf("%w: method must be a non-empty string", ErrInvalidEnvelope)
		}
		params, hasParams := fields["params"]
		if hasResult || hasError {
			return Message{}, fmt.Errorf("%w: method messages cannot contain result or error", ErrInvalidEnvelope)
		}
		if hasParams {
			message.Params = append(json.RawMessage(nil), params...)
		}
		if !hasID {
			message.Kind = Notification
			return message, nil
		}
		if !hasParams {
			return Message{}, fmt.Errorf("%w: request requires params", ErrInvalidEnvelope)
		}
		id, err := parseID(idRaw)
		if err != nil {
			return Message{}, err
		}
		message.Kind, message.ID = Request, id
		return message, nil
	}

	if !hasID || hasResult == hasError {
		return Message{}, fmt.Errorf("%w: response requires id and exactly one of result or error", ErrInvalidEnvelope)
	}
	id, err := parseID(idRaw)
	if err != nil {
		return Message{}, err
	}
	message.Kind, message.ID = Response, id
	if hasResult {
		message.Result = append(json.RawMessage(nil), result...)
		return message, nil
	}
	var decodedError struct {
		Code    *int64          `json:"code"`
		Message *string         `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(errorRaw, &decodedError); err != nil || decodedError.Code == nil || decodedError.Message == nil || *decodedError.Message == "" {
		return Message{}, fmt.Errorf("%w: invalid error response", ErrInvalidEnvelope)
	}
	message.Error = &RPCError{Code: *decodedError.Code, Message: *decodedError.Message, Data: decodedError.Data}
	return message, nil
}

func parseID(raw json.RawMessage) (ID, error) {
	if len(raw) == 0 {
		return ID{}, fmt.Errorf("%w: missing request ID", ErrInvalidEnvelope)
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return ID{}, fmt.Errorf("%w: invalid string request ID", ErrInvalidEnvelope)
		}
		return StringID(value), nil
	}
	text := string(raw)
	if strings.ContainsAny(text, ".eE") {
		return ID{}, fmt.Errorf("%w: request ID must be string or int64", ErrInvalidEnvelope)
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return ID{}, fmt.Errorf("%w: request ID must be string or int64", ErrInvalidEnvelope)
	}
	return IntID(value), nil
}

// Writer serializes complete JSONL messages so concurrent callers cannot mix bytes.
type Writer struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewWriter(writer io.Writer) *Writer { return &Writer{writer: writer} }

func (writer *Writer) Write(value any) error {
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writer.write(line)
}

func (writer *Writer) write(line []byte) error {
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

type correlation struct {
	mu     sync.Mutex
	local  map[string]bool
	remote map[string]bool
}

func (state *correlation) register(table *map[string]bool, id ID) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if *table == nil {
		*table = make(map[string]bool)
	}
	if _, exists := (*table)[id.Key()]; exists {
		return ErrDuplicateRequestID
	}
	(*table)[id.Key()] = false
	return nil
}

func (state *correlation) resolve(table *map[string]bool, id ID) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	done, exists := (*table)[id.Key()]
	if !exists {
		return ErrOrphanResponse
	}
	if done {
		return ErrDuplicateResponse
	}
	(*table)[id.Key()] = true
	return nil
}

// Transport combines framing, serialized writes, and request correlation.
// Process creation and lifecycle deliberately stay outside this package layer.
type Transport struct {
	decoder     *Decoder
	writer      *Writer
	correlation correlation
}

func NewTransport(reader io.Reader, writer io.Writer) *Transport {
	return &Transport{decoder: NewDecoder(reader), writer: NewWriter(writer)}
}

func (transport *Transport) Read() (Message, error) {
	message, err := transport.decoder.Decode()
	if err != nil {
		return Message{}, err
	}
	switch message.Kind {
	case Request:
		err = transport.correlation.register(&transport.correlation.remote, message.ID)
	case Response:
		err = transport.correlation.resolve(&transport.correlation.local, message.ID)
	}
	if err != nil {
		return Message{}, err
	}
	return message, nil
}

func (transport *Transport) SendRequest(id ID, method string, params any) error {
	if method == "" {
		return fmt.Errorf("%w: empty method", ErrInvalidEnvelope)
	}
	if params == nil {
		params = struct{}{}
	}
	line, err := json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
		ID     ID     `json:"id"`
	}{method, params, id})
	if err != nil {
		return err
	}
	if err := transport.correlation.register(&transport.correlation.local, id); err != nil {
		return err
	}
	return transport.writer.write(line)
}

func (transport *Transport) SendNotification(method string, params any) error {
	if method == "" {
		return fmt.Errorf("%w: empty method", ErrInvalidEnvelope)
	}
	if params == nil {
		params = struct{}{}
	}
	return transport.writer.Write(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{method, params})
}

func (transport *Transport) SendResult(id ID, result any) error {
	line, err := json.Marshal(struct {
		ID     ID  `json:"id"`
		Result any `json:"result"`
	}{id, result})
	if err != nil {
		return err
	}
	if err := transport.correlation.resolve(&transport.correlation.remote, id); err != nil {
		return err
	}
	return transport.writer.write(line)
}

func (transport *Transport) SendError(id ID, rpcError RPCError) error {
	if rpcError.Message == "" {
		return fmt.Errorf("%w: empty error message", ErrInvalidEnvelope)
	}
	line, err := json.Marshal(struct {
		ID    ID       `json:"id"`
		Error RPCError `json:"error"`
	}{id, rpcError})
	if err != nil {
		return err
	}
	if err := transport.correlation.resolve(&transport.correlation.remote, id); err != nil {
		return err
	}
	return transport.writer.write(line)
}
