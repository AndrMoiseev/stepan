package impl_loop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

// ErrAssignmentCommit identifies an invalid controller-owned commit step.
var ErrAssignmentCommit = errors.New("invalid assignment commit")

// CommitPreparation is the Git state staged for the one local commit. It is
// persisted before Git creates that commit, allowing later recovery to compare
// the operation against an observed commit rather than retrying blindly.
type CommitPreparation struct {
	ParentCommit string
	Tree         string
}

// CommitObservation contains facts read from the commit Git actually made.
// The controller completes machine task state only when every fact agrees with
// the persisted intent.
type CommitObservation struct {
	CommitID     string
	ParentCommit string
	Tree         string
	Message      string
}

// CommitControl is the narrow mutation seam for one assignment commit. The
// production implementation stages the current working copy (accepted code
// plus the orchestrator's informational mark) and makes exactly one commit.
// Tests use a fake and never start Git.
type CommitControl interface {
	Prepare(context.Context, string) (CommitPreparation, error)
	Commit(context.Context, string, string) (CommitObservation, error)
}

// GitCommitControl performs the local Git commands for an assignment commit.
// It deliberately does not disable hooks.
type GitCommitControl struct{}

var _ CommitControl = GitCommitControl{}

func (GitCommitControl) Prepare(ctx context.Context, repository string) (CommitPreparation, error) {
	parent, err := runGitMutation(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return CommitPreparation{}, err
	}
	index, err := os.CreateTemp("", "stepan-commit-index-*")
	if err != nil {
		return CommitPreparation{}, fmt.Errorf("create temporary Git index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		return CommitPreparation{}, fmt.Errorf("close temporary Git index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		return CommitPreparation{}, fmt.Errorf("initialize temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)
	environment := append(gitMutationEnvironment(), "GIT_INDEX_FILE="+indexPath)
	if _, err := runGitMutationWithEnvironment(ctx, repository, environment, "read-tree", "HEAD"); err != nil {
		return CommitPreparation{}, err
	}
	if _, err := runGitMutationWithEnvironment(ctx, repository, environment, "add", "--all"); err != nil {
		return CommitPreparation{}, err
	}
	tree, err := runGitMutationWithEnvironment(ctx, repository, environment, "write-tree")
	if err != nil {
		return CommitPreparation{}, err
	}
	prepared := CommitPreparation{ParentCommit: strings.TrimSpace(string(parent)), Tree: strings.TrimSpace(string(tree))}
	if prepared.ParentCommit == "" || prepared.Tree == "" {
		return CommitPreparation{}, errors.New("Git returned an empty parent or tree")
	}
	return prepared, nil
}

func (GitCommitControl) Commit(ctx context.Context, repository, message string) (CommitObservation, error) {
	if _, err := runGitMutation(ctx, repository, "add", "--all"); err != nil {
		return CommitObservation{}, err
	}
	if _, err := runGitMutation(ctx, repository, "commit", "-m", message); err != nil {
		return CommitObservation{}, err
	}
	commitID, err := runGitMutation(ctx, repository, "rev-parse", "HEAD")
	if err != nil {
		return CommitObservation{}, err
	}
	parent, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^")
	if err != nil {
		return CommitObservation{}, err
	}
	tree, err := runGitMutation(ctx, repository, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return CommitObservation{}, err
	}
	observedMessage, err := runGitMutation(ctx, repository, "show", "-s", "--format=%B", "HEAD")
	if err != nil {
		return CommitObservation{}, err
	}
	return CommitObservation{
		CommitID: strings.TrimSpace(string(commitID)), ParentCommit: strings.TrimSpace(string(parent)),
		Tree: strings.TrimSpace(string(tree)), Message: strings.TrimRight(string(observedMessage), "\r\n"),
	}, nil
}

// CommitAcceptedAssignmentInput is owned entirely by the controller. Response
// is the current implementation_ready response: a later repair replaces its
// message naturally, and no agent is asked merely to draft a commit message.
type CommitAcceptedAssignmentInput struct {
	Run          *implementationstate.Run
	StateStore   *runstore.StateStore
	Repository   string
	AssignmentID implementationstate.AssignmentID
	OperationID  implementationstate.OperationID
	Response     AgentResponse
	Control      CommitControl
}

type CommitAcceptedAssignmentResult struct {
	Intent implementationstate.CommitIntent
	Commit implementationstate.CommitEvidence
}

// CommitAcceptedAssignment stages accepted code and its informational
// tasks.md mark together, persists the exact intent, then creates one local
// commit. It never calls an agent and it never infers completion from Markdown.
func CommitAcceptedAssignment(ctx context.Context, input CommitAcceptedAssignmentInput) (CommitAcceptedAssignmentResult, error) {
	if err := validateCommitAcceptedAssignmentInput(input); err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	message, err := messageForImplementationCommit(input.Run, input.AssignmentID, input.OperationID, input.Response)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, err
	}
	control := input.Control
	if control == nil {
		control = GitCommitControl{}
	}
	preparation, err := control.Prepare(ctx, input.Repository)
	if err != nil {
		return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: prepare Git commit: %v", ErrAssignmentCommit, err)
	}
	intent := implementationstate.CommitIntent{OperationID: input.OperationID, ParentCommit: preparation.ParentCommit, Tree: preparation.Tree, Message: message}
	if err := input.Run.SetPendingCommitIntent(input.AssignmentID, intent); err != nil {
		return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: record commit intent: %v", ErrAssignmentCommit, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return CommitAcceptedAssignmentResult{}, fmt.Errorf("%w: persist commit intent: %v", ErrAssignmentCommit, err)
	}
	observed, err := control.Commit(ctx, input.Repository, message)
	if err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: create Git commit: %v", ErrAssignmentCommit, err)
	}
	commit := implementationstate.CommitEvidence{OperationID: input.OperationID, CommitID: observed.CommitID, ParentCommit: observed.ParentCommit, Tree: observed.Tree, Message: observed.Message, State: input.Run.CurrentState, Basis: implementationstate.AcceptanceBasis{Specification: input.Run.Identity.Specification, Configuration: input.Run.Identity.Configuration}}
	if err := input.Run.CommitAssignment(input.AssignmentID, commit); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent}, fmt.Errorf("%w: verify Git commit: %v", ErrAssignmentCommit, err)
	}
	if _, err := input.StateStore.Record(context.WithoutCancel(ctx), input.Run); err != nil {
		return CommitAcceptedAssignmentResult{Intent: intent, Commit: commit}, fmt.Errorf("%w: persist completed assignment: %v", ErrAssignmentCommit, err)
	}
	return CommitAcceptedAssignmentResult{Intent: intent, Commit: commit}, nil
}

func validateCommitAcceptedAssignmentInput(input CommitAcceptedAssignmentInput) error {
	if input.Run == nil || input.StateStore == nil || strings.TrimSpace(input.Repository) == "" || input.AssignmentID == "" || input.OperationID == "" {
		return fmt.Errorf("%w: run, state store, repository, assignment, and operation are required", ErrAssignmentCommit)
	}
	return nil
}

func messageForImplementationCommit(run *implementationstate.Run, assignmentID implementationstate.AssignmentID, operationID implementationstate.OperationID, response AgentResponse) (string, error) {
	if run == nil || response.Kind != ResponseImplementationReady || response.Message == nil || strings.TrimSpace(*response.Message) == "" {
		return "", fmt.Errorf("%w: implementation_ready with a non-empty message is required", ErrAssignmentCommit)
	}
	assignment := assignmentByID(run, assignmentID)
	if assignment == nil || assignment.Status != implementationstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil || !responseMatchesAcceptedAssignment(run, *assignment, response) {
		return "", fmt.Errorf("%w: implementation response is not bound to the accepted assignment", ErrAssignmentCommit)
	}
	body := strings.TrimSpace(*response.Message)
	return body + "\n\nStepan-Run: " + string(run.Identity.ID) + "\nStepan-Assignment: " + string(assignmentID) + "\nStepan-Operation: " + string(operationID), nil
}

func assignmentByID(run *implementationstate.Run, id implementationstate.AssignmentID) *implementationstate.Assignment {
	for index := range run.Assignments {
		if run.Assignments[index].ID == id {
			return &run.Assignments[index]
		}
	}
	return nil
}

func responseMatchesAcceptedAssignment(run *implementationstate.Run, assignment implementationstate.Assignment, response AgentResponse) bool {
	return response.Binding.RunID == run.Identity.ID && response.Binding.AssignmentID == assignment.ID && response.Binding.BriefID == assignment.Acceptance.BriefID && response.Binding.Specification == run.Identity.Specification && response.Binding.Configuration == run.Identity.Configuration && response.Binding.TaskList == run.Identity.TaskList && strings.TrimSpace(response.Binding.CallID) != ""
}

func runGitMutation(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	return runGitMutationWithEnvironment(ctx, directory, gitMutationEnvironment(), arguments...)
}

func runGitMutationWithEnvironment(ctx context.Context, directory string, environment []string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// gitMutationEnvironment has the same repository-selection hardening as the
// read-only helper but intentionally omits GIT_OPTIONAL_LOCKS=0: index writes
// are required for a real commit.
func gitMutationEnvironment() []string {
	blocked := map[string]bool{
		"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_COMMON_DIR": true, "GIT_INDEX_FILE": true,
		"GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
		"GIT_REPLACE_REF_BASE": true, "GIT_SHALLOW_FILE": true, "GIT_CEILING_DIRECTORIES": true,
		"GIT_OPTIONAL_LOCKS": true,
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if blocked[key] || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GIT_CONFIG_PARAMETERS" {
			continue
		}
		environment = append(environment, item)
	}
	return environment
}
