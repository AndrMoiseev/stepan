package nessyapp

import (
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

func TestClassifyProcessDiagnosticUsesClosedSafeClasses(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  processDiagnosticClass
	}{
		{name: "empty", value: " \r\n", want: processDiagnosticEmpty},
		{name: "authentication", value: "Authentication required: secret provider text", want: processDiagnosticAuthentication},
		{name: "launch denied", value: "Fatal: spawn EPERM at a private path", want: processDiagnosticLaunchDenied},
		{name: "unsupported option", value: "unknown option --private-value", want: processDiagnosticUnsupportedOption},
		{name: "missing dependency", value: "Cannot find module at a private path", want: processDiagnosticMissingDependency},
		{name: "unclassified", value: "arbitrary credential-body provider output", want: processDiagnosticUnclassified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyProcessDiagnostic(test.value); got != test.want {
				t.Fatalf("class = %q, want %q", got.String(), test.want.String())
			}
		})
	}
}

func TestProcessExitDiagnosticOmitsUnsafeExecutableName(t *testing.T) {
	code := 7
	diagnostic := processExitDiagnostic{
		executable: "corporate-nessy\ncredential-body-do-not-echo",
		exitCode:   &code,
		stderr:     processDiagnosticUnclassified,
		phase:      diagnosticInitializeLifecycle,
	}
	message := errorProcessExitDiagnostic(withProcessExitDiagnostic(agentruntime.ErrRuntimeExited, diagnostic))
	if message != "exit code 7, during initialize lifecycle, stderr class unclassified" {
		t.Fatalf("safe diagnostic = %q", message)
	}
}
