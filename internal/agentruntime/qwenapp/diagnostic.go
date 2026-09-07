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
	diagnosticStructuredResponseSyntax
	diagnosticStructuredResponseSchema
	diagnosticUnclassifiedProtocol
	diagnosticSessionUpdatePayloadShape
	diagnosticSessionUpdateDiscriminatorMissing
	diagnosticSessionUpdateDiscriminatorType
	diagnosticSessionUpdateDiscriminatorEmpty
	diagnosticSessionUpdateEventEnvelope
	diagnosticSessionUpdateSnakeCaseDiscriminator
	diagnosticSessionUpdateTypeDiscriminator
	diagnosticSessionUpdateNestedUpdate
	diagnosticSessionUpdateHandler
	diagnosticAgentRequestFSWriteTextFile
	diagnosticAgentRequestTerminalCreate
	diagnosticAgentRequestTerminalOutput
	diagnosticAgentRequestTerminalWaitForExit
	diagnosticAgentRequestTerminalKill
	diagnosticAgentRequestTerminalRelease
	diagnosticAgentExtensionRequest
	diagnosticAgentRequestUnknownMethod
	diagnosticPermissionRequestEnvelope
	diagnosticPermissionRequestToolCall
	diagnosticPermissionRequestLifecycle
	diagnosticPermissionRequestCapacity
	diagnosticPermissionRequestHandler
	diagnosticReadTextFileRequestEnvelope
	diagnosticReadTextFileRequestLifecycle
	diagnosticReadTextFileRequestCapacity
	diagnosticReadTextFileRequestHandler
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
	case diagnosticStructuredResponseSyntax:
		return "structured response syntax"
	case diagnosticStructuredResponseSchema:
		return "structured response schema"
	case diagnosticUnclassifiedProtocol:
		return "unclassified protocol"
	case diagnosticSessionUpdatePayloadShape:
		return "session/update payload shape"
	case diagnosticSessionUpdateDiscriminatorMissing:
		return "session/update discriminator missing"
	case diagnosticSessionUpdateDiscriminatorType:
		return "session/update discriminator type"
	case diagnosticSessionUpdateDiscriminatorEmpty:
		return "session/update discriminator empty"
	case diagnosticSessionUpdateEventEnvelope:
		return "session/update event envelope"
	case diagnosticSessionUpdateSnakeCaseDiscriminator:
		return "session/update snake_case discriminator"
	case diagnosticSessionUpdateTypeDiscriminator:
		return "session/update type discriminator"
	case diagnosticSessionUpdateNestedUpdate:
		return "session/update nested update"
	case diagnosticSessionUpdateHandler:
		return "session/update handler"
	case diagnosticAgentRequestFSWriteTextFile:
		return "agent request fs/write_text_file"
	case diagnosticAgentRequestTerminalCreate:
		return "agent request terminal/create"
	case diagnosticAgentRequestTerminalOutput:
		return "agent request terminal/output"
	case diagnosticAgentRequestTerminalWaitForExit:
		return "agent request terminal/wait_for_exit"
	case diagnosticAgentRequestTerminalKill:
		return "agent request terminal/kill"
	case diagnosticAgentRequestTerminalRelease:
		return "agent request terminal/release"
	case diagnosticAgentExtensionRequest:
		return "agent extension request"
	case diagnosticAgentRequestUnknownMethod:
		return "agent request unknown method"
	case diagnosticPermissionRequestEnvelope:
		return "permission request envelope"
	case diagnosticPermissionRequestToolCall:
		return "permission request toolCall"
	case diagnosticPermissionRequestLifecycle:
		return "permission request lifecycle"
	case diagnosticPermissionRequestCapacity:
		return "permission request capacity"
	case diagnosticPermissionRequestHandler:
		return "permission request handler"
	case diagnosticReadTextFileRequestEnvelope:
		return "fs/read_text_file request envelope"
	case diagnosticReadTextFileRequestLifecycle:
		return "fs/read_text_file request lifecycle"
	case diagnosticReadTextFileRequestCapacity:
		return "fs/read_text_file request capacity"
	case diagnosticReadTextFileRequestHandler:
		return "fs/read_text_file request handler"
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

func agentRequestMethodDiagnosticContext(method string) diagnosticContext {
	switch method {
	case "fs/write_text_file":
		return diagnosticAgentRequestFSWriteTextFile
	case "terminal/create":
		return diagnosticAgentRequestTerminalCreate
	case "terminal/output":
		return diagnosticAgentRequestTerminalOutput
	case "terminal/wait_for_exit":
		return diagnosticAgentRequestTerminalWaitForExit
	case "terminal/kill":
		return diagnosticAgentRequestTerminalKill
	case "terminal/release":
		return diagnosticAgentRequestTerminalRelease
	default:
		if strings.HasPrefix(method, "_") {
			return diagnosticAgentExtensionRequest
		}
		return diagnosticAgentRequestUnknownMethod
	}
}
