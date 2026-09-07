package qwenapp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// diagnosticContext is a closed set of non-sensitive adapter facts that may
// cross the user-facing runtime boundary. It cannot carry provider text,
// paths, prompts, responses, environment values, or file content.
type diagnosticContext uint8

const (
	diagnosticNone diagnosticContext = iota
	diagnosticAgentCapabilities
	diagnosticPromptCapabilities
	diagnosticSessionCapabilities
	diagnosticProtocolVersion
	diagnosticStartupRoot
	diagnosticToolInventory
	diagnosticInitializeLifecycle
	diagnosticSessionLifecycle
	diagnosticPromptLifecycle
	diagnosticToolReadFile
	diagnosticToolWriteFile
	diagnosticToolEdit
	diagnosticToolGlob
	diagnosticToolGrepSearch
	diagnosticToolShell
	diagnosticToolWeb
	diagnosticToolAgent
	diagnosticRegularFile
	diagnosticACPTransport
	diagnosticJSONRPCEnvelope
	diagnosticJSONRPCCorrelation
	diagnosticPromptTerminal
	diagnosticNotificationMethod
	diagnosticSessionUpdateEnvelope
	diagnosticSessionUpdatePayload
	diagnosticSessionUpdateKind
	diagnosticSessionUpdateContent
	diagnosticSessionUpdateLifecycle
	diagnosticToolCallUpdate
	diagnosticAssistantContent
	diagnosticAgentRequest
)

func (context diagnosticContext) String() string {
	switch context {
	case diagnosticAgentCapabilities:
		return "agentCapabilities"
	case diagnosticPromptCapabilities:
		return "promptCapabilities"
	case diagnosticSessionCapabilities:
		return "sessionCapabilities"
	case diagnosticProtocolVersion:
		return "protocolVersion"
	case diagnosticStartupRoot:
		return "startup-root contract"
	case diagnosticToolInventory:
		return "tool inventory"
	case diagnosticInitializeLifecycle:
		return "initialize lifecycle"
	case diagnosticSessionLifecycle:
		return "session/new lifecycle"
	case diagnosticPromptLifecycle:
		return "session/prompt lifecycle"
	case diagnosticToolReadFile:
		return "read_file"
	case diagnosticToolWriteFile:
		return "write_file"
	case diagnosticToolEdit:
		return "edit"
	case diagnosticToolGlob:
		return "glob"
	case diagnosticToolGrepSearch:
		return "grep_search"
	case diagnosticToolShell:
		return "shell"
	case diagnosticToolWeb:
		return "web"
	case diagnosticToolAgent:
		return "agent"
	case diagnosticRegularFile:
		return "regular file"
	case diagnosticACPTransport:
		return "ACP transport"
	case diagnosticJSONRPCEnvelope:
		return "JSON-RPC envelope"
	case diagnosticJSONRPCCorrelation:
		return "JSON-RPC correlation"
	case diagnosticPromptTerminal:
		return "session/prompt terminal"
	case diagnosticNotificationMethod:
		return "notification method"
	case diagnosticSessionUpdateEnvelope:
		return "session/update envelope"
	case diagnosticSessionUpdatePayload:
		return "session/update payload"
	case diagnosticSessionUpdateKind:
		return "session/update kind"
	case diagnosticSessionUpdateContent:
		return "session/update content"
	case diagnosticSessionUpdateLifecycle:
		return "session/update lifecycle"
	case diagnosticToolCallUpdate:
		return "tool-call update"
	case diagnosticAssistantContent:
		return "assistant content"
	case diagnosticAgentRequest:
		return "agent request"
	default:
		return ""
	}
}

type contextualError struct {
	cause   error
	context diagnosticContext
}

type processDiagnosticClass uint8

const (
	processDiagnosticUnclassified processDiagnosticClass = iota
	processDiagnosticEmpty
	processDiagnosticAuthentication
	processDiagnosticLaunchDenied
	processDiagnosticUnsupportedOption
	processDiagnosticMissingDependency
)

func (class processDiagnosticClass) String() string {
	switch class {
	case processDiagnosticEmpty:
		return "empty"
	case processDiagnosticAuthentication:
		return "authentication"
	case processDiagnosticLaunchDenied:
		return "process launch denied"
	case processDiagnosticUnsupportedOption:
		return "unsupported startup option"
	case processDiagnosticMissingDependency:
		return "missing runtime dependency"
	default:
		return "unclassified"
	}
}

type processExitDiagnostic struct {
	executable string
	exitCode   *int
	stderr     processDiagnosticClass
	phase      diagnosticContext
}

type processExitError struct {
	cause      error
	diagnostic processExitDiagnostic
}

func (err processExitError) Error() string { return err.cause.Error() }
func (err processExitError) Unwrap() error { return err.cause }

func withProcessExitDiagnostic(cause error, diagnostic processExitDiagnostic) error {
	if cause == nil || !errors.Is(cause, agentruntime.ErrRuntimeExited) {
		return cause
	}
	return processExitError{cause: cause, diagnostic: diagnostic}
}

type rpcDiagnosticError struct {
	cause error
	code  int64
	class processDiagnosticClass
}

func (err rpcDiagnosticError) Error() string { return err.cause.Error() }
func (err rpcDiagnosticError) Unwrap() error { return err.cause }

func withRPCDiagnostic(cause error, code int64, message string) error {
	if cause == nil {
		return nil
	}
	return rpcDiagnosticError{cause: cause, code: code, class: classifyProcessDiagnostic(message)}
}

func errorRPCDiagnostic(err error) string {
	var rpcErr rpcDiagnosticError
	if !errors.As(err, &rpcErr) {
		return ""
	}
	return fmt.Sprintf("ACP error code %d, provider error class %s", rpcErr.code, rpcErr.class.String())
}

func errorProcessExitDiagnostic(err error) string {
	var processErr processExitError
	if !errors.As(err, &processErr) {
		return ""
	}
	parts := make([]string, 0, 4)
	if executable := safeExecutableDiagnostic(processErr.diagnostic.executable); executable != "" {
		parts = append(parts, "executable "+strconv.Quote(executable))
	}
	if processErr.diagnostic.exitCode != nil {
		parts = append(parts, fmt.Sprintf("exit code %d", *processErr.diagnostic.exitCode))
	}
	if phase := processErr.diagnostic.phase.String(); phase != "" {
		parts = append(parts, "during "+phase)
	}
	parts = append(parts, "stderr class "+processErr.diagnostic.stderr.String())
	return strings.Join(parts, ", ")
}

func safeExecutableDiagnostic(value string) string {
	if len(value) == 0 || len(value) > 255 {
		return ""
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return ""
		}
	}
	return value
}

func classifyProcessDiagnostic(value string) processDiagnosticClass {
	normalized := strings.ToLower(value)
	if strings.TrimSpace(normalized) == "" {
		return processDiagnosticEmpty
	}
	for _, signal := range []string{"authentication required", "authenticate first", "not authenticated", "not logged in", "unauthorized", "api key"} {
		if strings.Contains(normalized, signal) {
			return processDiagnosticAuthentication
		}
	}
	for _, signal := range []string{"spawn eperm", "failed to relaunch", "permission denied", "access is denied"} {
		if strings.Contains(normalized, signal) {
			return processDiagnosticLaunchDenied
		}
	}
	for _, signal := range []string{"unknown option", "unknown argument", "unrecognized option", "unrecognized argument", "invalid option"} {
		if strings.Contains(normalized, signal) {
			return processDiagnosticUnsupportedOption
		}
	}
	for _, signal := range []string{"cannot find module", "module not found", "missing module", "no such file or directory"} {
		if strings.Contains(normalized, signal) {
			return processDiagnosticMissingDependency
		}
	}
	return processDiagnosticUnclassified
}

func (err contextualError) Error() string { return err.cause.Error() }
func (err contextualError) Unwrap() error { return err.cause }

func withDiagnosticContext(cause error, context diagnosticContext) error {
	if cause == nil || context.String() == "" {
		return cause
	}
	return contextualError{cause: cause, context: context}
}

func errorDiagnosticContext(err error) string {
	var contextual contextualError
	if errors.As(err, &contextual) {
		return contextual.context.String()
	}
	return ""
}

func toolDiagnosticContext(name string) diagnosticContext {
	switch safeToolName(name) {
	case "read_file":
		return diagnosticToolReadFile
	case "write_file":
		return diagnosticToolWriteFile
	case "edit":
		return diagnosticToolEdit
	case "glob":
		return diagnosticToolGlob
	case "grep_search":
		return diagnosticToolGrepSearch
	case "shell":
		return diagnosticToolShell
	case "web":
		return diagnosticToolWeb
	case "agent":
		return diagnosticToolAgent
	default:
		return diagnosticNone
	}
}
