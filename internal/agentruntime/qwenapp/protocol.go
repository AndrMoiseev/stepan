package qwenapp

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

const acpProtocolVersion = 1

var (
	// ErrProtocol classifies an ambiguous or corrupt ACP stream. It deliberately
	// carries no wire payload; callers may safely show the error to users.
	ErrProtocol = agentruntime.ErrRuntimeProtocol
	// ErrIncompatible classifies a well-formed ACP peer that cannot satisfy the
	// mandatory Stepan contract.
	ErrIncompatible = agentruntime.ErrRuntimeIncompatible
	// ErrConnectionClosed is returned to every operation after the first fatal
	// connection error.
	ErrConnectionClosed = errors.New("agent connection closed")
	// ErrTurnInProgress rejects a second prompt locally while the first prompt's
	// terminal response is still being decoded and committed.
	ErrTurnInProgress = agentruntime.ErrTurnInProgress
	// ErrPermissionDenied classifies a filesystem operation rejected by the
	// active-turn policy. It aliases the provider-neutral runtime category and
	// never includes the requested path or file content.
	ErrPermissionDenied = agentruntime.ErrPermissionDenied
)

type messageKind uint8

const (
	requestMessage messageKind = iota + 1
	responseMessage
	notificationMessage
)

// requestID retains the JSON type in its correlation key: 1 and "1" are
// deliberately distinct IDs.
type requestID struct {
	raw json.RawMessage
	key string
}

func stringID(value string) requestID {
	raw, _ := json.Marshal(value)
	return requestID{raw: raw, key: "s:" + value}
}

func integerID(value int64) requestID {
	text := strconv.FormatInt(value, 10)
	return requestID{raw: json.RawMessage(text), key: "i:" + text}
}

func validAgentIdentity(value string) bool {
	return value != "" && len(value) <= maxCorrelationIDBytes
}

func (id requestID) MarshalJSON() ([]byte, error) {
	if id.key == "" {
		return nil, errors.New("empty request ID")
	}
	return append([]byte(nil), id.raw...), nil
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type message struct {
	kind   messageKind
	method string
	id     requestID
	params json.RawMessage
	result json.RawMessage
	err    *rpcError
}

type implementationInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type clientCapabilities struct {
	FS fileSystemCapabilities `json:"fs"`
}

type fileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type initializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities clientCapabilities `json:"clientCapabilities"`
	ClientInfo         implementationInfo `json:"clientInfo"`
}

type initializeResponse struct {
	ProtocolVersion   int                 `json:"protocolVersion"`
	AgentCapabilities *agentCapabilities  `json:"agentCapabilities"`
	AgentInfo         *implementationInfo `json:"agentInfo,omitempty"`
	Meta              json.RawMessage     `json:"_meta,omitempty"`
}

type agentCapabilities struct {
	PromptCapabilities  *promptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities *sessionCapabilities `json:"sessionCapabilities,omitempty"`
	Meta                json.RawMessage      `json:"_meta,omitempty"`
}

type promptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// additionalDirectories is intentionally represented only for inspection. It
// is never required and never appears in a request sent by this package.
type sessionCapabilities struct {
	AdditionalDirectories json.RawMessage `json:"additionalDirectories,omitempty"`
}

type newSessionParams struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type newSessionResponse struct {
	SessionID string          `json:"sessionId"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type sessionParams struct {
	SessionID string `json:"sessionId"`
}

type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type promptResponse struct {
	StopReason string `json:"stopReason"`
}

var allowedToolNames = [...]string{"edit", "glob", "grep_search", "read_file", "write_file"}

func validateToolInventory(names []string) error {
	got := append([]string(nil), names...)
	sort.Strings(got)
	want := allowedToolNames[:]
	for index := 1; index < len(got); index++ {
		if got[index] == got[index-1] {
			return withDiagnosticContext(fmt.Errorf("%w: duplicate tool %s", ErrIncompatible, safeToolName(got[index])), toolDiagnosticContext(got[index]))
		}
	}
	wanted := make(map[string]struct{}, len(want))
	for _, name := range want {
		wanted[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(got))
	for _, name := range got {
		seen[name] = struct{}{}
		if _, ok := wanted[name]; !ok {
			return withDiagnosticContext(fmt.Errorf("%w: unexpected tool %s", ErrIncompatible, safeToolName(name)), toolDiagnosticContext(name))
		}
	}
	for _, name := range want {
		if _, ok := seen[name]; !ok {
			return withDiagnosticContext(fmt.Errorf("%w: missing required tool %s", ErrIncompatible, name), toolDiagnosticContext(name))
		}
	}
	return nil
}

func safeToolName(name string) string {
	switch name {
	case "read_file", "write_file", "edit", "glob", "grep_search", "shell", "web", "agent":
		return name
	default:
		return "unknown"
	}
}
