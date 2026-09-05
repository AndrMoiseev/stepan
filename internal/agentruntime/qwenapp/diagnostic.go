package qwenapp

import "errors"

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
	diagnosticToolReadFile
	diagnosticToolWriteFile
	diagnosticToolEdit
	diagnosticToolGlob
	diagnosticToolGrepSearch
	diagnosticToolShell
	diagnosticToolWeb
	diagnosticToolAgent
	diagnosticRegularFile
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
	default:
		return ""
	}
}

type contextualError struct {
	cause   error
	context diagnosticContext
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
