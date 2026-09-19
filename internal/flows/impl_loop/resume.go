package impl_loop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/git"
	"github.com/AndrMoiseev/stepan/internal/implementationconfig"
	"github.com/AndrMoiseev/stepan/internal/implementationstate"
	"github.com/AndrMoiseev/stepan/internal/openspec"
	"github.com/AndrMoiseev/stepan/internal/runstore"
)

var (
	// ErrResumeReconciliation means /resume left the run paused because a
	// remediable input could not be reconciled. The wrapped error is safe to
	// show as the diagnostic retained in the execution block.
	ErrResumeReconciliation = errors.New("implementation resume reconciliation failed")
	// ErrResumeScopeChanged means current full OpenSpec requirements no longer
	// match the saved run. This is terminal: the user must define a new scope.
	ErrResumeScopeChanged = errors.New("implementation resume scope changed")
)

// ResumeInput supplies the controller-owned resources for one explicit
// /resume. The required-check runner is deliberately controller-owned: after
// reconciliation every resume must establish fresh mandatory evidence before
// any session can be created or other work can continue.
type ResumeInput struct {
	Run        *implementationstate.Run
	StateStore *runstore.StateStore
	Journal    *runstore.Run
	Repository string
	Workspace  WorkspaceControl
	Runner     CheckRunner
	// UserControl makes the resume check set subject to the same pause/stop
	// boundary as every other configured command.
	UserControl    *UserRunControl
	ProtectedPaths []string

	// Factories are preflighted against the newly loaded profiles. Supplying no
	// factories is useful to callers that reconcile inputs before their runtime
	// wiring exists; a production controller supplies its configured factories.
	Factories map[string]RuntimeFactory
	// ConfigurationLoader is an optional deterministic seam for callers that
	// own settings discovery. The default reloads both standard settings files.
	ConfigurationLoader func(string) (implementationconfig.Configuration, error)
	// ClassifySpecificationChange is the controller's deterministic product
	// decision for a changed complete specification. It must distinguish an
	// update compatible with this run from one that requires fresh scope.
	// Without it a changed specification is held on a diagnostic pause rather
	// than silently assuming either outcome.
	ClassifySpecificationChange func(previous, current []byte) (SpecificationChange, error)

	// SessionOwner is optional. When supplied with SessionBase, a changed
	// effective configuration closes the old owner and installs a fresh one so
	// no provider conversation keeps an old profile in its bootstrap context.
	// A non-nil pointer to a nil owner denotes a restarted process and likewise
	// receives a fresh owner after the required resume checks pass.
	SessionOwner **SessionOwner
	SessionBase  agentruntime.ThreadConfig
}

// SpecificationChange is the controller-provided result of comparing the
// previous complete OpenSpec package to the newly loaded package.
type SpecificationChange struct {
	RequiresNewScope bool
}

// ResumeResult describes the reconciled inputs. Manual changes are observed
// and retained; no resume path resets, checks out, or overwrites the worktree.
type ResumeResult struct {
	Configuration         implementationconfig.Configuration
	Checks                implementationconfig.CheckSelection
	Rules                 implementationconfig.RulesFileValidation
	Prepared              PreparedRuntimes
	WorkspaceChanged      bool
	ConfigurationChanged  bool
	SessionsRecreated     bool
	ResumeChecks          CheckSet
	ResumeCheckDiagnostic string
	ResumeCheckEvidence   implementationstate.EvidenceRef
}

// Resume reconciles an already-paused run with its working copy, complete
// specification and effective configuration. Rules are always freshly
// validated but deliberately excluded from version comparisons: their content
// alone cannot invalidate acceptance. On a malformed configuration, rules, or
// unverifiable saved workspace fingerprint the run remains paused with a
// durable execution-block diagnostic. A controller-classified scope change
// closes the run; a compatible specification refreshes durable inputs.
// Successful reconciliation runs the complete required-check gate before any
// session is recreated or other work may continue. A failed gate leaves the
// run paused with durable diagnostics and never dispatches an implementer.
func Resume(ctx context.Context, input ResumeInput) (ResumeResult, error) {
	if err := validateResumeInput(input); err != nil {
		return ResumeResult{}, err
	}
	candidate, err := resumeCandidate(input.Run)
	if err != nil {
		return ResumeResult{}, err
	}

	pkg, err := openspec.Load(input.Repository, input.Run.Identity.Change)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "reload the complete OpenSpec specification", err)
	}
	specification, err := resumeSpecification(pkg)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "encode the complete OpenSpec specification", err)
	}
	specificationChanged, err := referencePayloadChanged(input.Journal, input.Run.Identity.Specification, specification)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "verify saved OpenSpec specification", err)
	}
	if specificationChanged {
		if input.ClassifySpecificationChange == nil {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "classify changed complete OpenSpec specification", errors.New("controller did not classify whether the specification change requires new scope"))
		}
		previousSpecification, readErr := input.Journal.Read(input.Run.Identity.Specification)
		if readErr != nil {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "read saved complete OpenSpec specification", readErr)
		}
		change, classifyErr := input.ClassifySpecificationChange(previousSpecification, specification)
		if classifyErr != nil {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "classify changed complete OpenSpec specification", classifyErr)
		}
		if change.RequiresNewScope {
			if err := candidate.Close("complete OpenSpec specification changed and requires a new implementation scope"); err != nil {
				return ResumeResult{}, err
			}
			if err := persistResumeCandidate(ctx, input, candidate); err != nil {
				return ResumeResult{}, err
			}
			return ResumeResult{}, ErrResumeScopeChanged
		}
	}

	configuration, checks, rules, prepared, err := loadResumeConfiguration(input)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "reload implementation configuration and rules", err)
	}
	configurationBytes, err := canonicalResumeConfiguration(configuration)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "encode effective implementation configuration", err)
	}
	configurationChanged, err := referencePayloadChanged(input.Journal, input.Run.Identity.Configuration, configurationBytes)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "verify saved implementation configuration", err)
	}

	expected, err := savedWorkspaceSnapshot(input.Journal, input.Run.CurrentState)
	if err != nil {
		return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "read saved working-copy fingerprint", err)
	}
	workspace := effectiveWorkspaceControl(input.Workspace)
	workspaceErr := workspace.EnsureUnchanged(ctx, input.Repository, expected)
	workspaceChanged := workspaceErr != nil
	var observed implementationstate.EvidenceRef
	var refreshedPendingCommit implementationstate.AssignmentID
	var refreshedPendingTree string
	if workspaceChanged {
		if !errors.Is(workspaceErr, git.ErrRepositoryDiverged) {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "verify working-copy fingerprint", workspaceErr)
		}
		actual, captureErr := workspace.Capture(ctx, input.Repository)
		if captureErr != nil {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "capture manually changed working copy", captureErr)
		}
		stateData, marshalErr := json.Marshal(actual)
		if marshalErr != nil {
			return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "encode manually changed working-copy fingerprint", marshalErr)
		}
		pendingCommit := pendingCommitWorkspace(input.Run, expected, actual)
		reflectedTasks, reflection := reflectedTasksThenRulesWorkspace(ctx, workspace, input.Repository, input.Run, input.Journal, expected, actual, rules)
		if reflectedTasks {
			refreshedPendingCommit = pendingCommitForReflection(input.Run, reflection)
			refreshedPendingTree = actual.TreeOID
		}
		rulesOnly := rulesOnlyWorkspaceChange(ctx, workspace, input.Repository, expected, actual, rules)
		if !pendingCommit && !reflectedTasks && !rulesOnly {
			if err := verifyResumeGitControl(expected, actual); err != nil {
				return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "verify branch, HEAD, and index before accepting manual changes", err)
			}
		}
		if pendingCommit || reflectedTasks || rulesOnly {
			// Rules are read afresh but never versioned. Their content is not a
			// code-state input and therefore cannot reopen an accepted assignment.
			workspaceChanged = false
		} else {
			observed, err = publishResumeEvidence(input.Journal, "resume-workspace", stateData)
			if err != nil {
				return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "preserve manually changed working-copy fingerprint", err)
			}
		}
	}

	// A user /stop may cancel a slow configuration reload. Do not let that
	// stale resume candidate reactivate a run after its controlling driver has
	// joined the canceled operation and closed it.
	if err := ctx.Err(); err != nil {
		return ResumeResult{}, err
	}
	if err := candidate.Resume(); err != nil {
		return ResumeResult{}, err
	}
	if refreshedPendingCommit != "" {
		if err := candidate.RefreshPendingCommitIntentTree(refreshedPendingCommit, refreshedPendingTree); err != nil {
			return ResumeResult{}, err
		}
	}
	if workspaceChanged {
		if err := candidate.ObserveCodeState(observed); err != nil {
			return ResumeResult{}, err
		}
	}
	if specificationChanged || configurationChanged {
		specificationRef := candidate.Identity.Specification
		if specificationChanged {
			specificationRef, err = publishResumeEvidence(input.Journal, "resume-specification", specification)
			if err != nil {
				return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "preserve compatible complete OpenSpec specification", err)
			}
		}
		configurationRef := candidate.Identity.Configuration
		if configurationChanged {
			configurationRef, publishErr := publishResumeEvidence(input.Journal, "resume-configuration", configurationBytes)
			if publishErr != nil {
				return ResumeResult{}, persistResumeBlock(ctx, input, candidate, "preserve effective implementation configuration", publishErr)
			}
			if err := candidate.RefreshAcceptanceInputs(specificationRef, configurationRef); err != nil {
				return ResumeResult{}, err
			}
		} else if err := candidate.RefreshAcceptanceInputs(specificationRef, configurationRef); err != nil {
			return ResumeResult{}, err
		}
	}
	if err := persistResumeCandidate(ctx, input, candidate); err != nil {
		return ResumeResult{}, err
	}

	result := ResumeResult{Configuration: configuration, Checks: checks, Rules: rules, Prepared: prepared, WorkspaceChanged: workspaceChanged, ConfigurationChanged: configurationChanged}
	resumeChecks, err := runResumeRequiredChecks(ctx, input, checks)
	result.ResumeChecks = resumeChecks.Set
	result.ResumeCheckDiagnostic = resumeChecks.Diagnostic
	result.ResumeCheckEvidence = resumeChecks.Evidence
	if err != nil {
		return result, err
	}
	if input.Run.Status != implementationstate.RunActive {
		return result, nil
	}
	// A nil owner slot means this is a new controller process. Even when the
	// effective configuration is byte-for-byte unchanged, provider sessions
	// cannot be resumed from their old threads or histories. Install a fresh
	// owner only after the uncounted resume gate succeeds; role dispatch then
	// reconstructs each needed bootstrap from durable data.
	recreateSessions := configurationChanged || (input.SessionOwner != nil && *input.SessionOwner == nil)
	if input.SessionOwner != nil && *input.SessionOwner != nil && !(*input.SessionOwner).MatchesPrepared(prepared) {
		// A prior resume may have made configuration B durable but stopped at
		// the mandatory-check gate before replacing sessions created for A.
		// Re-check the live owner on every successful gate so it can never
		// continue with the old bootstrap profile.
		recreateSessions = true
	}
	if recreateSessions && input.SessionOwner != nil {
		owner, recreateErr := NewSessionOwner(prepared, input.SessionBase)
		if recreateErr != nil {
			return ResumeResult{}, recreateSessionsFailure(ctx, input, owner, recreateErr)
		}
		if *input.SessionOwner != nil {
			if closeErr := (*input.SessionOwner).Close(); closeErr != nil {
				_ = owner.Close()
				return ResumeResult{}, recreateSessionsFailure(ctx, input, nil, closeErr)
			}
		}
		*input.SessionOwner = owner
		result.SessionsRecreated = true
	}
	return result, nil
}

func validateResumeInput(input ResumeInput) error {
	if input.Run == nil || input.StateStore == nil || input.Journal == nil || input.Runner == nil || strings.TrimSpace(input.Repository) == "" {
		return errors.New("implementation resume requires run, state store, journal, repository, and check runner")
	}
	if input.Run.Status != implementationstate.RunPaused {
		return fmt.Errorf("%w: only a paused run can resume", implementationstate.ErrInvalidTransition)
	}
	if input.SessionOwner != nil && !filepath.IsAbs(input.SessionBase.Workspace) {
		return errors.New("implementation resume session base requires an absolute workspace")
	}
	return nil
}

func resumeCandidate(run *implementationstate.Run) (*implementationstate.Run, error) {
	event, err := implementationstate.NewRunStateEvent(1, run)
	if err != nil {
		return nil, err
	}
	return event.State, nil
}

func loadResumeConfiguration(input ResumeInput) (implementationconfig.Configuration, implementationconfig.CheckSelection, implementationconfig.RulesFileValidation, PreparedRuntimes, error) {
	var configuration implementationconfig.Configuration
	var err error
	if input.ConfigurationLoader != nil {
		configuration, err = input.ConfigurationLoader(input.Repository)
	} else {
		sources, loadErr := implementationconfig.Load(input.Repository)
		if loadErr == nil {
			configuration, loadErr = implementationconfig.Merge(sources)
		}
		err = loadErr
	}
	if err != nil {
		return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
	}
	if err := configuration.ValidateLoopRoles(); err != nil {
		return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
	}
	for _, role := range []string{
		implementationconfig.RoleOrchestrator,
		implementationconfig.RoleBriefer,
		implementationconfig.RoleImplementer,
		implementationconfig.RoleTaskReviewer,
		implementationconfig.RoleExplorer,
		implementationconfig.RoleFinalReviewer,
	} {
		if _, err := configuration.ResolveRoleProfile(role); err != nil {
			return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
		}
	}
	checks, err := configuration.SelectHostChecks()
	if err != nil {
		return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
	}
	rules, err := configuration.ValidateRulesFile(input.Repository)
	if err != nil {
		return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
	}
	prepared := PreparedRuntimes{}
	if input.Factories != nil {
		prepared, err = PrepareRuntimes(configuration, input.Factories)
		if err != nil {
			return implementationconfig.Configuration{}, implementationconfig.CheckSelection{}, implementationconfig.RulesFileValidation{}, PreparedRuntimes{}, err
		}
	}
	return configuration, checks, rules, prepared, nil
}

type resumeWorkspaceComparer interface {
	Compare(context.Context, string, git.Snapshot, git.Snapshot) ([]string, error)
}

func rulesOnlyWorkspaceChange(ctx context.Context, workspace WorkspaceControl, repository string, before, after git.Snapshot, rules implementationconfig.RulesFileValidation) bool {
	if rules.DocumentPaths == "" || before.HeadOID != after.HeadOID || before.HeadRef != after.HeadRef || before.SubmodulesHash != after.SubmodulesHash {
		return false
	}
	comparer, ok := workspace.(resumeWorkspaceComparer)
	if !ok {
		return false
	}
	paths, err := comparer.Compare(ctx, repository, before, after)
	if err != nil || len(paths) == 0 {
		return false
	}
	ruleDocuments := strings.Split(rules.DocumentPaths, "\x00")
	documents := make(map[string]struct{}, len(ruleDocuments))
	for _, document := range ruleDocuments {
		relative, err := filepath.Rel(repository, document)
		if err != nil || filepath.IsAbs(relative) {
			return false
		}
		documents[filepath.ToSlash(filepath.Clean(relative))] = struct{}{}
	}
	for _, path := range paths {
		path = filepath.ToSlash(filepath.Clean(path))
		if _, ok := documents[path]; !ok {
			return false
		}
	}
	return true
}

func verifyResumeGitControl(expected, actual git.Snapshot) error {
	if actual.HeadRef == "" {
		return errors.New("working copy is detached from its implementation branch")
	}
	if actual.HeadRef != expected.HeadRef {
		return fmt.Errorf("working-copy branch changed from %q to %q", expected.HeadRef, actual.HeadRef)
	}
	if actual.HeadOID != expected.HeadOID {
		return fmt.Errorf("working-copy HEAD changed from %q to %q", expected.HeadOID, actual.HeadOID)
	}
	if actual.IndexHash != expected.IndexHash {
		return errors.New("working-copy index changed outside a proven pending commit")
	}
	if actual.SubmodulesHash != expected.SubmodulesHash {
		return errors.New("working-copy submodules changed")
	}
	return nil
}

func pendingCommitWorkspace(run *implementationstate.Run, expected, actual git.Snapshot) bool {
	// git add --all happens before a hook can reject the commit. The index may
	// therefore differ from the pre-staging snapshot, but the synthetic tree is
	// still the exact durable intent and a retry stages that same tree again.
	if run == nil || actual.HeadRef != expected.HeadRef || actual.HeadOID != expected.HeadOID || actual.SubmodulesHash != expected.SubmodulesHash {
		return false
	}
	for _, assignment := range run.Assignments {
		if assignment.Status != implementationstate.AssignmentAcceptedAwaitingCommit || assignment.Acceptance == nil {
			continue
		}
		intent := assignment.Acceptance.PendingCommit
		if intent.OperationID != "" && actual.HeadOID == intent.ParentCommit && actual.TreeOID == intent.Tree {
			return true
		}
	}
	return false
}

func reflectedTasksThenRulesWorkspace(ctx context.Context, workspace WorkspaceControl, repository string, run *implementationstate.Run, journal *runstore.Run, expected, actual git.Snapshot, rules implementationconfig.RulesFileValidation) (bool, git.Snapshot) {
	if run == nil || journal == nil || actual.HeadOID != expected.HeadOID || actual.HeadRef != expected.HeadRef || actual.IndexHash != expected.IndexHash || actual.SubmodulesHash != expected.SubmodulesHash {
		// A hook refusal may stage the pending tree. The second delta below is
		// still constrained by its durable reflection snapshot.
		if run == nil || journal == nil || actual.HeadOID != expected.HeadOID || actual.HeadRef != expected.HeadRef || actual.SubmodulesHash != expected.SubmodulesHash {
			return false, git.Snapshot{}
		}
	}
	tasksPath, err := selectedChangeTasksPath(run.Identity.Change)
	if err != nil {
		return false, git.Snapshot{}
	}
	comparer, ok := workspace.(resumeWorkspaceComparer)
	if !ok {
		return false, git.Snapshot{}
	}
	for _, operation := range run.RunOperations {
		if operation.Kind != implementationstate.OperationAgent || operation.Description != "reflect accepted task progress in tasks.md" {
			continue
		}
		for _, result := range run.RunResults {
			if result.OperationID != operation.ID || result.Status != implementationstate.ResultSucceeded || len(result.Evidence) != 1 {
				continue
			}
			data, err := journal.Read(result.Evidence[0])
			if err != nil {
				continue
			}
			var reflected git.Snapshot
			if json.Unmarshal(data, &reflected) != nil || !sameResumeGitControl(reflected, expected) {
				continue
			}
			paths, compareErr := comparer.Compare(ctx, repository, expected, reflected)
			if compareErr != nil || len(paths) != 1 || filepath.ToSlash(filepath.Clean(paths[0])) != tasksPath {
				continue
			}
			if sameResumeGitSnapshot(reflected, actual) || rulesOnlyWorkspaceChange(ctx, workspace, repository, reflected, actual, rules) {
				return true, reflected
			}
		}
	}
	return false, git.Snapshot{}
}

func pendingCommitForReflection(run *implementationstate.Run, reflection git.Snapshot) implementationstate.AssignmentID {
	if run == nil {
		return ""
	}
	for _, assignment := range run.Assignments {
		if assignment.Status == implementationstate.AssignmentAcceptedAwaitingCommit && assignment.Acceptance != nil {
			intent := assignment.Acceptance.PendingCommit
			if intent.OperationID != "" && intent.ParentCommit == reflection.HeadOID && intent.Tree == reflection.TreeOID {
				return assignment.ID
			}
		}
	}
	return ""
}

func sameResumeGitSnapshot(left, right git.Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.TreeOID == right.TreeOID && left.IndexHash == right.IndexHash && left.StatusHash == right.StatusHash && left.SubmodulesHash == right.SubmodulesHash
}

func sameResumeGitControl(left, right git.Snapshot) bool {
	return left.HeadOID == right.HeadOID && left.HeadRef == right.HeadRef && left.IndexHash == right.IndexHash && left.SubmodulesHash == right.SubmodulesHash
}

func resumeSpecification(pkg openspec.Package) ([]byte, error) {
	type document struct{ Path, Version, Content string }
	documents := make([]document, 0, 2+len(pkg.ChangeSpecs)+len(pkg.MainSpecs))
	for _, item := range append(append([]openspec.Document{pkg.Proposal, pkg.Design}, pkg.ChangeSpecs...), pkg.MainSpecs...) {
		documents = append(documents, document{Path: item.Path, Version: item.Version, Content: item.Content})
	}
	return json.Marshal(documents)
}

func canonicalResumeConfiguration(configuration implementationconfig.Configuration) ([]byte, error) {
	raw, err := json.Marshal(configuration)
	if err != nil {
		return nil, err
	}
	var canonical bytes.Buffer
	if err := json.Compact(&canonical, raw); err != nil {
		return nil, err
	}
	return canonical.Bytes(), nil
}

func referencePayloadChanged(journal *runstore.Run, reference implementationstate.EvidenceRef, current []byte) (bool, error) {
	previous, err := journal.Read(reference)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(previous, current), nil
}

func savedWorkspaceSnapshot(journal *runstore.Run, reference implementationstate.EvidenceRef) (git.Snapshot, error) {
	data, err := journal.Read(reference)
	if err != nil {
		return git.Snapshot{}, err
	}
	var snapshot git.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return git.Snapshot{}, fmt.Errorf("decode workspace fingerprint: %w", err)
	}
	if snapshot.HeadOID == "" || snapshot.TreeOID == "" || snapshot.IndexHash == "" || snapshot.StatusHash == "" || snapshot.SubmodulesHash == "" {
		return git.Snapshot{}, errors.New("saved workspace fingerprint is incomplete")
	}
	return snapshot, nil
}

func publishResumeEvidence(journal *runstore.Run, prefix string, data []byte) (implementationstate.EvidenceRef, error) {
	return journal.Publish(implementationstate.EvidenceID(prefix+"-"+digestBytes(data)), data)
}

func digestBytes(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func persistResumeCandidate(ctx context.Context, input ResumeInput, candidate *implementationstate.Run) error {
	event, err := input.StateStore.Record(context.WithoutCancel(ctx), candidate)
	if event.Sequence != 0 {
		*input.Run = *candidate
	}
	if err != nil {
		return fmt.Errorf("persist resume reconciliation: %w", err)
	}
	return nil
}

func persistResumeBlock(ctx context.Context, input ResumeInput, candidate *implementationstate.Run, action string, cause error) error {
	block, err := ExecutionBlockForUserRemediation(action, cause.Error(), []string{"reloaded current resume inputs without changing the working copy"}, "repair the reported input, then explicitly resume or close the run")
	if err != nil {
		return errors.Join(ErrResumeReconciliation, cause, err)
	}
	if err := candidate.UpdatePausedExecutionBlock(block); err != nil {
		return errors.Join(ErrResumeReconciliation, cause, err)
	}
	if err := persistResumeCandidate(ctx, input, candidate); err != nil {
		return errors.Join(ErrResumeReconciliation, cause, err)
	}
	return errors.Join(ErrResumeReconciliation, cause)
}

func recreateSessionsFailure(ctx context.Context, input ResumeInput, owner *SessionOwner, cause error) error {
	if owner != nil {
		_ = owner.Close()
	}
	// Session recreation happens after a successful durable reconciliation. A
	// failed replacement therefore returns the run to a durable paused state;
	// it never leaves a silently active run with stale conversations.
	candidate, err := resumeCandidate(input.Run)
	if err != nil {
		return errors.Join(cause, err)
	}
	block, err := ExecutionBlockForUserRemediation("create sessions for updated profiles", cause.Error(), []string{"closed stale sessions after configuration reload"}, "repair the profile or provider setup, then explicitly resume or close the run")
	if err != nil {
		return errors.Join(cause, err)
	}
	if err := candidate.PauseExecutionBlocked(block); err != nil {
		return errors.Join(cause, err)
	}
	if err := persistResumeCandidate(ctx, input, candidate); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(ErrResumeReconciliation, cause)
}
