package impl_loop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AndrMoiseev/stepan/internal/flows/impl_loop/checkexec"
	implstate "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/state"
	runstore "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/store"
	testfs "github.com/AndrMoiseev/stepan/internal/flows/impl_loop/workspace/testfs"
	"github.com/AndrMoiseev/stepan/internal/setting"
)

func TestBoundedCheckDiagnosticsPreservesUnicodeEdgesAndSmallOutput(t *testing.T) {
	if got := BoundedCheckDiagnostics([]byte("начало α"), []byte(" конец β")); got != "начало α конец β" {
		t.Fatalf("small diagnostics = %q", got)
	}

	stdout := []byte("НАЧАЛО-😀" + strings.Repeat("Ж", MaxCheckDiagnosticsRunes))
	stderr := []byte(strings.Repeat("界", MaxCheckDiagnosticsRunes) + "-КОНЕЦ-🦊")
	got := BoundedCheckDiagnostics(stdout, stderr)
	if count := utf8.RuneCountInString(got); count != MaxCheckDiagnosticsRunes {
		t.Fatalf("diagnostic rune count = %d, want %d", count, MaxCheckDiagnosticsRunes)
	}
	for _, want := range []string{"НАЧАЛО-😀", "-КОНЕЦ-🦊", omittedCheckOutput} {
		if !strings.Contains(got, want) {
			t.Fatalf("bounded diagnostics omit %q", want)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatalf("bounded diagnostics are not valid UTF-8: %q", got)
	}
	if invalid := BoundedCheckDiagnostics([]byte{'a', 0xff, 0xfe}, []byte{0xff, 'z'}); invalid != "a��z" {
		t.Fatalf("invalid-byte diagnostics = %q", invalid)
	}
}

func TestCheckResultPublisherKeepsFullLogsOutsideMachineState(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	store, err := runstore.NewTransient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-logs")
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewCheckResultPublisherWithControl(run, testfs.New(), repository, "check-operation")
	if err != nil {
		t.Fatal(err)
	}
	stdout := append([]byte("stdout: начало "), bytes.Repeat([]byte("😀"), 11000)...)
	stdout = append(stdout, []byte(" UNIQUE_FULL_STDOUT_MIDDLE ")...)
	stdout = append(stdout, bytes.Repeat([]byte("😀"), 11000)...)
	stderr := bytes.Repeat([]byte("界"), 11000)
	stderr = append(stderr, []byte(" UNIQUE_FULL_STDERR_MIDDLE ")...)
	stderr = append(stderr, bytes.Repeat([]byte("界"), 11000)...)
	stderr = append(stderr, []byte(" stderr: конец")...)
	report, err := publisher.ReportCheck(context.Background(), "unicode-check", checkexec.Command{
		Program: "tool with spaces", Args: []string{"α β", "literal;not-a-shell"}, Env: map[string]string{"SECRET": "must-not-appear"}, CWD: repository,
	}, checkexec.Result{ExitCode: 17, Failure: checkexec.FailureExit, Stdout: stdout, Stderr: stderr}, 123*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if report.Name != "unicode-check" || report.ExitCode != 17 || report.Failure != checkexec.FailureExit || report.Duration != 123*time.Millisecond {
		t.Fatalf("agent report identity = %#v", report)
	}
	if report.Command != `"tool with spaces" "α β" "literal;not-a-shell"` || strings.Contains(report.Command, "SECRET") {
		t.Fatalf("rendered command = %q", report.Command)
	}
	if count := utf8.RuneCountInString(report.Diagnostics); count != MaxCheckDiagnosticsRunes {
		t.Fatalf("bounded report diagnostic rune count = %d", count)
	}
	if !strings.Contains(report.Diagnostics, "stdout: начало") || !strings.Contains(report.Diagnostics, "stderr: конец") || !strings.Contains(report.Diagnostics, omittedCheckOutput) {
		t.Fatal("bounded report diagnostics did not retain both ends and omission marker")
	}
	for _, log := range []CheckLog{report.Stdout, report.Stderr} {
		if log.Reference.ID == "" || log.Reference.Digest == "" || log.Path == "" {
			t.Fatalf("incomplete log reference: %#v", log)
		}
		if _, err := os.Stat(log.Path); err != nil {
			t.Fatalf("published log is not readable at %q: %v", log.Path, err)
		}
		if err := run.VerifyReference(log.Reference); err != nil {
			t.Fatalf("published log reference is not durable: %v", err)
		}
	}
	if got, err := run.Read(report.Stdout.Reference); err != nil || !bytes.Equal(got, stdout) {
		t.Fatalf("stdout full artifact = %d bytes, error %v", len(got), err)
	}
	if got, err := run.Read(report.Stderr.Reference); err != nil || !bytes.Equal(got, stderr) {
		t.Fatalf("stderr full artifact = %d bytes, error %v", len(got), err)
	}
	if got, err := os.ReadFile(report.Stdout.Path); err != nil || !bytes.Equal(got, stdout) {
		t.Fatalf("stdout direct artifact read = %d bytes, error %v", len(got), err)
	}
	if got, err := os.ReadFile(report.Stderr.Path); err != nil || !bytes.Equal(got, stderr) {
		t.Fatalf("stderr direct artifact read = %d bytes, error %v", len(got), err)
	}
	if report.CheckedState.Reference.ID == "" || report.CheckedState.HeadOID == "" || report.CheckedState.TreeOID == "" || report.CheckedState.IndexHash == "" || report.CheckedState.StatusHash == "" {
		t.Fatalf("checked state = %#v", report.CheckedState)
	}
	stateData, err := run.Read(report.CheckedState.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stateData, []byte(report.CheckedState.TreeOID)) {
		t.Fatalf("state artifact does not retain snapshot: %s", stateData)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, fullOnly := range [][]byte{[]byte("UNIQUE_FULL_STDOUT_MIDDLE"), []byte("UNIQUE_FULL_STDERR_MIDDLE")} {
		if bytes.Contains(encoded, fullOnly) {
			t.Fatal("agent-facing result embeds full command output")
		}
	}
	if !bytes.Contains(encoded, []byte(report.Stdout.Reference.Digest)) || !bytes.Contains(encoded, []byte(report.Stderr.Reference.Digest)) {
		t.Fatalf("agent-facing result omits full-log hashes: %s", encoded)
	}

	assertCheckEvidenceCanBeRecordedWithoutOutput(t, run, report, stdout, stderr)
}

func TestRunRequestedChecksWithReporterAttachesPresentationAndMeasuresDuration(t *testing.T) {
	repository := newFilesystemWorkspace(t)
	store, err := runstore.NewTransient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create("run-reporter")
	if err != nil {
		t.Fatal(err)
	}
	reporter, err := NewCheckResultPublisherWithControl(run, testfs.New(), repository, "request-operation")
	if err != nil {
		t.Fatal(err)
	}
	selection := testCheckSelection(nil)
	selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
	runner := CheckRunnerFunc(func(context.Context, checkexec.Command) (checkexec.Result, error) {
		time.Sleep(time.Millisecond)
		return checkexec.Result{ExitCode: 0, Stdout: []byte("complete output")}, nil
	})
	set, err := RunRequestedChecksWithReporter(context.Background(), selection, []string{"test_auth"}, runner, reporter)
	if err != nil {
		t.Fatal(err)
	}
	result := set.Results[0]
	if result.Presentation == nil || result.Duration <= 0 || result.Presentation.Name != "test_auth" {
		t.Fatalf("reported check = %#v", result)
	}
	if got, err := run.Read(result.Presentation.Stdout.Reference); err != nil || string(got) != "complete output" {
		t.Fatalf("persisted runner stdout = %q, %v", got, err)
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"Result"`)) || !bytes.Contains(encoded, []byte(result.Presentation.Stdout.Reference.Digest)) {
		t.Fatalf("serialized check result is not a bounded presentation: %s", encoded)
	}
}

func TestRunRequestedChecksPersistsCanceledCommandEvidenceOutsideInvocationContext(t *testing.T) {
	for _, test := range []struct {
		name    string
		context func(*testing.T) (context.Context, func())
		failure checkexec.FailureKind
		err     error
	}{
		{
			name: "canceled",
			context: func(t *testing.T) (context.Context, func()) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, cancel
			},
			failure: checkexec.FailureCanceled,
			err:     context.Canceled,
		},
		{
			name: "deadline",
			context: func(t *testing.T) (context.Context, func()) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
				return ctx, cancel
			},
			failure: checkexec.FailureTimeout,
			err:     context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newFilesystemWorkspace(t)
			store, err := runstore.NewTransient(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run, err := store.Create(implstate.RunID("run-" + test.name))
			if err != nil {
				t.Fatal(err)
			}
			reporter, err := NewCheckResultPublisherWithControl(run, testfs.New(), repository, "canceled-result")
			if err != nil {
				t.Fatal(err)
			}
			selection := testCheckSelection(nil)
			selection.Checks["test_auth"] = implementationCheck(repository, "test_auth")
			selection.Checks["lint"] = implementationCheck(repository, "lint")
			invocationContext, stopInvocation := test.context(t)
			defer stopInvocation()
			calls := 0
			runner := CheckRunnerFunc(func(ctx context.Context, _ checkexec.Command) (checkexec.Result, error) {
				calls++
				if test.failure == checkexec.FailureCanceled {
					stopInvocation()
				}
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
					t.Fatal("invocation context did not cancel the command")
				}
				return checkexec.Result{ExitCode: -1, Failure: test.failure, Stdout: []byte("partial stdout " + test.name), Stderr: []byte("partial stderr " + test.name)}, test.err
			})

			set, err := RunRequestedChecksWithReporter(invocationContext, selection, []string{"test_auth", "lint"}, runner, reporter)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || set.Results[0].Status != CheckFailed || set.Results[1].Status != CheckNotRun {
				t.Fatalf("canceled set did not fail fast: calls=%d results=%#v", calls, set.Results)
			}
			presentation := set.Results[0].Presentation
			if presentation == nil || presentation.Failure != test.failure || presentation.ExitCode != -1 || presentation.CheckedState.Reference.ID == "" {
				t.Fatalf("canceled command presentation = %#v", presentation)
			}
			if got, err := run.Read(presentation.Stdout.Reference); err != nil || string(got) != "partial stdout "+test.name {
				t.Fatalf("retained canceled stdout = %q, %v", got, err)
			}
			if got, err := run.Read(presentation.Stderr.Reference); err != nil || string(got) != "partial stderr "+test.name {
				t.Fatalf("retained canceled stderr = %q, %v", got, err)
			}
			if _, err := run.Read(presentation.CheckedState.Reference); err != nil {
				t.Fatalf("retained canceled checked state: %v", err)
			}
		})
	}
}

func implementationCheck(repository, name string) setting.SelectedCheck {
	return setting.SelectedCheck{Name: name, Kind: setting.CheckKindTests, Command: setting.CheckCommand{Program: name, Args: []string{"all"}}, CWD: repository, TimeoutSeconds: 600, Available: true}
}

func assertCheckEvidenceCanBeRecordedWithoutOutput(t *testing.T, run *runstore.Run, report CheckPresentation, stdout, stderr []byte) {
	t.Helper()
	publish := func(id implstate.EvidenceID) implstate.EvidenceRef {
		reference, err := run.Publish(id, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		return reference
	}
	baseline, specification, tasks, configuration := publish("baseline"), publish("specification"), publish("tasks"), publish("configuration")
	model, err := implstate.NewRun(implstate.RunIdentity{ID: run.ID(), Change: "change", Repository: "repository", WorkCopy: "repository", Branch: "feature", BaselineCommit: "base", BaselineState: baseline, Specification: specification, TaskList: tasks, Configuration: configuration}, []implstate.Task{{ID: "task", Order: 0, Title: "task"}})
	if err != nil {
		t.Fatal(err)
	}
	basis := implstate.AcceptanceBasis{Specification: specification, Configuration: configuration}
	if err := model.AddRunOperation(implstate.Operation{ID: "initial-baseline", Kind: implstate.OperationCheck, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.StartRunAttempt("initial-baseline"); err != nil {
		t.Fatal(err)
	}
	if err := model.AddRunResult(implstate.OperationResult{ID: "initial-baseline-result", OperationID: "initial-baseline", Status: implstate.ResultSucceeded, State: model.CurrentState, Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if err := model.RecordInitialBaselinePass("initial-baseline", "initial-baseline-result"); err != nil {
		t.Fatal(err)
	}
	if err := model.StartAssignment("assignment", []implstate.TaskID{"task"}); err != nil {
		t.Fatal(err)
	}
	brief := publish("brief")
	if err := model.AddBriefVersion("assignment", implstate.BriefVersion{ID: "brief", Number: 1, Document: brief}); err != nil {
		t.Fatal(err)
	}
	if err := model.AddOperation("assignment", implstate.Operation{ID: "check", Kind: implstate.OperationCheck, BriefID: "brief", Basis: basis}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.StartAssignmentAttempt("assignment", "check"); err != nil {
		t.Fatal(err)
	}
	if err := model.AddResult("assignment", implstate.OperationResult{ID: "result", OperationID: "check", Status: implstate.ResultFailed, State: report.CheckedState.Reference, Basis: basis, Evidence: []implstate.EvidenceRef{report.Stdout.Reference, report.Stderr.Reference}}); err != nil {
		t.Fatal(err)
	}
	state, err := runstore.OpenState(run)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.Record(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(state.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, fullOnly := range [][]byte{[]byte("UNIQUE_FULL_STDOUT_MIDDLE"), []byte("UNIQUE_FULL_STDERR_MIDDLE")} {
		if bytes.Contains(journal, fullOnly) {
			t.Fatal("machine-state JSONL embeds full command output")
		}
	}
}
