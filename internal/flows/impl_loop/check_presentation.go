package impl_loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	workcopy "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace"
	gitworkspace "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/git"
)

// MaxCheckDiagnosticsRunes is the maximum complete-command diagnostic sent to
// an agent. Full stdout and stderr remain immutable run artifacts instead.
const MaxCheckDiagnosticsRunes = 20000

const omittedCheckOutput = "\n… [output omitted; see complete stdout/stderr logs] …\n"

// CheckLog identifies one byte-exact, readable full command-output artifact.
// Path is an absolute path in the run's external files directory; Reference
// carries the ID and digest needed to verify it before reading.
type CheckLog struct {
	Reference implstate.EvidenceRef
	Path      string
}

// CheckedState is the Git identity/fingerprint observed immediately after a
// check. The same small JSON record is retained as Evidence so later
// acceptance code can compare the exact checked state without storing output
// in machine state.
type CheckedState struct {
	Reference      implstate.EvidenceRef
	HeadOID        string
	HeadRef        string
	TreeOID        string
	IndexHash      string
	StatusHash     string
	SubmodulesHash string
}

// CheckPresentation is the result safe to return to an agent. It contains no
// full output bytes and renders only program and arguments, never command
// environment values.
type CheckPresentation struct {
	Name         string
	Command      string
	ExitCode     int
	Failure      checkexec.FailureKind
	Duration     time.Duration
	Diagnostics  string
	Stdout       CheckLog
	Stderr       CheckLog
	CheckedState CheckedState
}

// CheckResultPublisher implements CheckResultReporter. A controller creates
// one publisher per durable check result, with a unique result evidence ID.
// Each invocation publishes all artifacts before returning their
// references, so a later StateStore.Record can safely reference them.
type CheckResultPublisher struct {
	run        *runstore.Run
	repository string
	workspace  workcopy.Control
	resultID   implstate.EvidenceID
	next       uint64
}

// NewCheckResultPublisher returns a reporter that writes full check output to
// one run. repository is the Git worktree whose state the check observes;
// resultID must be unique for this completed check-set attempt, including a
// retry after a process restart.
func NewCheckResultPublisher(run *runstore.Run, repository string, resultID implstate.EvidenceID) (*CheckResultPublisher, error) {
	return NewCheckResultPublisherWithControl(run, gitworkspace.Control{}, repository, resultID)
}

// NewCheckResultPublisherWithControl publishes a checked state observed
// through the workspace seam.
func NewCheckResultPublisherWithControl(run *runstore.Run, workspace workcopy.Control, repository string, resultID implstate.EvidenceID) (*CheckResultPublisher, error) {
	if run == nil {
		return nil, fmt.Errorf("check result publisher requires a run")
	}
	if strings.TrimSpace(repository) == "" {
		return nil, fmt.Errorf("check result publisher requires a repository")
	}
	if strings.TrimSpace(string(resultID)) == "" {
		return nil, fmt.Errorf("check result publisher requires a result evidence ID")
	}
	return &CheckResultPublisher{run: run, repository: repository, workspace: effectiveWorkspaceControl(workspace), resultID: resultID}, nil
}

// ReportCheck captures the post-command Git fingerprint and publishes the
// stdout, stderr, and fingerprint files before returning the agent result.
// A failure leaves no usable presentation, preventing a caller from writing
// an event that references an unpublished artifact.
func (p *CheckResultPublisher) ReportCheck(ctx context.Context, name string, command checkexec.Command, result checkexec.Result, duration time.Duration) (CheckPresentation, error) {
	if p == nil || p.run == nil {
		return CheckPresentation{}, fmt.Errorf("check result publisher requires a run")
	}
	if strings.TrimSpace(name) == "" {
		return CheckPresentation{}, fmt.Errorf("check result publisher requires a check name")
	}
	snapshot, err := p.workspace.Capture(ctx, p.repository)
	if err != nil {
		return CheckPresentation{}, fmt.Errorf("capture checked state: %w", err)
	}

	p.next++
	prefix := fmt.Sprintf("%s-check-%d-%s", p.resultID, p.next, name)
	stdout, err := p.publishLog(implstate.EvidenceID(prefix+"-stdout"), result.Stdout)
	if err != nil {
		return CheckPresentation{}, fmt.Errorf("publish stdout: %w", err)
	}
	stderr, err := p.publishLog(implstate.EvidenceID(prefix+"-stderr"), result.Stderr)
	if err != nil {
		return CheckPresentation{}, fmt.Errorf("publish stderr: %w", err)
	}
	stateData, err := json.Marshal(snapshot)
	if err != nil {
		return CheckPresentation{}, fmt.Errorf("encode checked state: %w", err)
	}
	stateRef, err := p.run.Publish(implstate.EvidenceID(prefix+"-state"), stateData)
	if err != nil {
		return CheckPresentation{}, fmt.Errorf("publish checked state: %w", err)
	}
	return CheckPresentation{
		Name:        name,
		Command:     RenderCheckCommand(command),
		ExitCode:    result.ExitCode,
		Failure:     result.Failure,
		Duration:    duration,
		Diagnostics: BoundedCheckDiagnostics(result.Stdout, result.Stderr),
		Stdout:      stdout,
		Stderr:      stderr,
		CheckedState: CheckedState{
			Reference:      stateRef,
			HeadOID:        snapshot.HeadOID,
			HeadRef:        snapshot.HeadRef,
			TreeOID:        snapshot.TreeOID,
			IndexHash:      snapshot.IndexHash,
			StatusHash:     snapshot.StatusHash,
			SubmodulesHash: snapshot.SubmodulesHash,
		},
	}, nil
}

func (p *CheckResultPublisher) publishLog(id implstate.EvidenceID, data []byte) (CheckLog, error) {
	reference, err := p.run.Publish(id, data)
	if err != nil {
		return CheckLog{}, err
	}
	path, err := p.run.ArtifactPath(reference)
	if err != nil {
		return CheckLog{}, err
	}
	return CheckLog{Reference: reference, Path: path}, nil
}

// RenderCheckCommand renders direct program/argument invocation for display.
// It intentionally excludes CWD and environment values so a configured secret
// cannot be disclosed as a diagnostic merely by presenting a check result.
func RenderCheckCommand(command checkexec.Command) string {
	parts := make([]string, 0, len(command.Args)+1)
	parts = append(parts, strconv.Quote(command.Program))
	for _, argument := range command.Args {
		parts = append(parts, strconv.Quote(argument))
	}
	return strings.Join(parts, " ")
}

// BoundedCheckDiagnostics converts invalid bytes to a deterministic Unicode
// replacement, combines stdout then stderr, and preserves both ends of output
// without ever splitting a Unicode code point.
func BoundedCheckDiagnostics(stdout, stderr []byte) string {
	combined := strings.ToValidUTF8(string(stdout), "�") + strings.ToValidUTF8(string(stderr), "�")
	if utf8.RuneCountInString(combined) <= MaxCheckDiagnosticsRunes {
		return combined
	}
	omissionRunes := utf8.RuneCountInString(omittedCheckOutput)
	keep := MaxCheckDiagnosticsRunes - omissionRunes
	leading := keep / 2
	trailing := keep - leading
	runes := []rune(combined)
	return string(runes[:leading]) + omittedCheckOutput + string(runes[len(runes)-trailing:])
}

// CheckStateArtifactPath exposes the durable state artifact for diagnostics or
// future freshness checks without allowing callers to derive arbitrary run
// paths. It is intentionally separate from agent log paths.
func CheckStateArtifactPath(run *runstore.Run, state CheckedState) (string, error) {
	if run == nil {
		return "", fmt.Errorf("check state artifact requires a run")
	}
	return run.ArtifactPath(state.Reference)
}
