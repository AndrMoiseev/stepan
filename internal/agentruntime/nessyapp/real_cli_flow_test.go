//go:build nessy_real_cli && (windows || darwin)

package nessyapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/specflow"
)

func testRealCLIFeatureFlow(t *testing.T, fixture realCLIFixture) {
	firstRuntime := startRealCLIRuntime(t, fixture, specflow.FlowEnvelopeSchema())
	first := newRealCLIApplication(t, fixture.workspace, firstRuntime)
	brief := "Create a provider acceptance feature. Do not ask clarification questions: publish each valid document immediately. " +
		"The first specification must leave retention duration explicitly TBD so its reviewer records one material SPEC-F finding. " +
		"When /apply requests automatic rework, resolve retention to 30 days and preserve traceability. The second spec review and plan review must approve."
	progress, err := first.application.StartFeature(brief)
	if err != nil {
		t.Fatal("start real /feature application flow")
	}
	featureID := progress.FeatureID
	progress = publishRealCLIStage(t, first.application, progress, specflow.StageIntent)
	intentHead := gitHead(t, fixture.workspace)
	progress = submitRealCLIApplication(t, first.application, "/approve")
	assertApprovalTransition(t, progress, specflow.StageSpec, intentHead, fixture.workspace)
	progress = publishRealCLIStage(t, first.application, progress, specflow.StageSpec)
	specHead := gitHead(t, fixture.workspace)
	progress = submitRealCLIApplication(t, first.application, "/review")
	if progress.ReviewStatus != specflow.ReviewAwaitingDecisions {
		t.Fatal("real spec review did not expose a material decision")
	}
	progress = submitRealCLIApplication(t, first.application, "/apply")
	if progress.CurrentStage != specflow.StageSpec || progress.StageStatus != specflow.StagePublished || progress.ReviewStatus != specflow.ReviewNotStarted {
		t.Fatal("real /apply did not complete automatic specification rework")
	}
	progress = submitRealCLIApplication(t, first.application, "/review")
	if progress.ReviewStatus != specflow.ReviewCompleted {
		t.Fatal("reworked specification did not pass a fresh review")
	}
	progress = submitRealCLIApplication(t, first.application, "/approve")
	assertApprovalTransition(t, progress, specflow.StagePlan, specHead, fixture.workspace)
	transient := realCLIRuntimeIdentities(firstRuntime)
	assertDurableFlowFiles(t, fixture.workspace, featureID, transient)
	closeRealCLIApplication(t, first)

	secondRuntime := startRealCLIRuntime(t, fixture, specflow.FlowEnvelopeSchema())
	second := newRealCLIApplication(t, fixture.workspace, secondRuntime)
	progress, err = second.application.Resume(featureID)
	if err != nil || progress.FeatureID != featureID || progress.CurrentStage != specflow.StagePlan {
		t.Fatal("fresh application/runtime generation did not resume the durable plan stage")
	}
	progress = publishRealCLIStage(t, second.application, progress, specflow.StagePlan)
	planHead := gitHead(t, fixture.workspace)
	progress = submitRealCLIApplication(t, second.application, "/review")
	if progress.ReviewStatus == specflow.ReviewAwaitingDecisions {
		progress = submitRealCLIApplication(t, second.application, "/apply")
		progress = submitRealCLIApplication(t, second.application, "/review")
	}
	if progress.ReviewStatus != specflow.ReviewCompleted {
		t.Fatal("real plan review did not complete")
	}
	progress = submitRealCLIApplication(t, second.application, "/approve")
	if progress.FlowStatus != specflow.FlowActive || progress.StageStatus != specflow.StageCommitted || progress.TextAllowed || gitHead(t, fixture.workspace) == planHead {
		t.Fatal("final approval did not close and commit the real /feature flow")
	}
	transient = append(transient, realCLIRuntimeIdentities(secondRuntime)...)
	assertDurableFlowFiles(t, fixture.workspace, featureID, transient)
	closeRealCLIApplication(t, second)
}

type realCLIApplication struct {
	application *specflow.ApplicationController
	registry    *specflow.SessionRegistry
	session     *specflow.Session
}

type boundedRealCLIApplicationRuntime struct {
	runtime *Runtime
}

func (bounded *boundedRealCLIApplicationRuntime) StartThread(config agentruntime.ThreadConfig) (agentruntime.Thread, error) {
	return startRealCLIThreadResult(bounded.runtime, config, realCLITimeout)
}

func (bounded *boundedRealCLIApplicationRuntime) RunTurn(thread agentruntime.Thread, prompt string) (json.RawMessage, error) {
	return runRealCLITurnResult(bounded.runtime, thread, prompt, realCLITimeout)
}

func (bounded *boundedRealCLIApplicationRuntime) CloseThread(thread agentruntime.Thread) error {
	return bounded.runtime.CloseThread(thread)
}

func (bounded *boundedRealCLIApplicationRuntime) Interrupt() error {
	return stopRealCLIRuntimeBounded(bounded.runtime)
}

func (bounded *boundedRealCLIApplicationRuntime) Close() error {
	return closeRealCLIRuntimeBounded(bounded.runtime)
}

func newRealCLIApplication(t *testing.T, workspace string, runtime *Runtime) realCLIApplication {
	t.Helper()
	repository, err := specflow.NewFSFeatureRepository(workspace)
	if err != nil {
		t.Fatal("create real-flow repository")
	}
	bounded := &boundedRealCLIApplicationRuntime{runtime: runtime}
	session := specflow.NewSession(func(context.Context) (agentruntime.Runtime, error) { return bounded, nil })
	registry, err := specflow.NewSessionRegistry(session, repository)
	if err != nil {
		t.Fatal("create real-flow session registry")
	}
	catalog := specflow.NewEmbeddedPromptCatalog()
	author, err := specflow.NewStageEngine(workspace, registry, repository, catalog)
	if err != nil {
		t.Fatal("create real-flow author engine")
	}
	reviewer, err := specflow.NewReviewEngine(workspace, registry, repository, catalog)
	if err != nil {
		t.Fatal("create real-flow review engine")
	}
	flow, err := specflow.NewFeatureController(repository, author, reviewer)
	if err != nil {
		t.Fatal("create real-flow controller")
	}
	manager, err := specflow.NewResumeManager(repository, registry, flow)
	if err != nil {
		t.Fatal("create real-flow resume manager")
	}
	application, err := specflow.NewApplicationController(workspace, session, manager, flow, specflow.RuntimeIdentity{Provider: "nessy", Model: "cli-selected"})
	if err != nil {
		t.Fatal("create real-flow application")
	}
	return realCLIApplication{application: application, registry: registry, session: session}
}

func closeRealCLIApplication(t *testing.T, harness realCLIApplication) {
	t.Helper()
	if _, err := harness.application.Close(); err != nil {
		t.Fatal("close real-flow application")
	}
	if err := harness.registry.Close(); err != nil {
		t.Fatal("close real-flow session registry")
	}
	if err := harness.session.Close(); err != nil {
		t.Fatal("close real-flow runtime session")
	}
}

func publishRealCLIStage(t *testing.T, application *specflow.ApplicationController, progress specflow.Progress, stage specflow.Stage) specflow.Progress {
	t.Helper()
	for attempt := 0; attempt < agentruntime.DefaultRetryLimit && progress.StageStatus == specflow.StageDrafting; attempt++ {
		progress = submitRealCLIApplication(t, application,
			"No material ambiguity remains. Write the complete valid fixed-name artifact now, with all required IDs/traces, then return the artifact envelope.")
	}
	if progress.CurrentStage != stage || progress.StageStatus != specflow.StagePublished {
		t.Fatal("real application stage did not publish through its controller")
	}
	return progress
}

func submitRealCLIApplication(t *testing.T, application *specflow.ApplicationController, input string) specflow.Progress {
	t.Helper()
	progress, err := application.Submit(input)
	if err != nil {
		t.Fatal("real application controller transition failed")
	}
	return progress
}

func assertApprovalTransition(t *testing.T, progress specflow.Progress, next specflow.Stage, previousHead, workspace string) {
	t.Helper()
	if progress.CurrentStage != next || gitHead(t, workspace) == previousHead {
		t.Fatal("application approval gate did not commit and advance the stage")
	}
}

func assertDurableFlowFiles(t *testing.T, workspace, featureID string, transient []string) {
	t.Helper()
	target, err := specflow.FeatureTargetForID(workspace, featureID)
	if err != nil {
		t.Fatal("resolve durable feature target")
	}
	for _, path := range []string{target.StatePath, target.JournalPath} {
		data, err := os.ReadFile(path)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			t.Fatal("real application did not persist state.json and mem-log.md")
		}
		for _, identity := range transient {
			if identity != "" && strings.Contains(string(data), identity) {
				t.Fatal("durable state or mem-log contains a transient provider identity")
			}
		}
	}
}

func realCLIRuntimeIdentities(runtime *Runtime) []string {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var identities []string
	for _, state := range runtime.threads {
		thread, ok := state.backend.(*nessyThread)
		if !ok {
			continue
		}
		identities = append(identities, thread.connection.SessionID())
		thread.process.mu.Lock()
		if thread.process.command != nil && thread.process.command.Process != nil {
			identities = append(identities, fmt.Sprint(thread.process.command.Process.Pid))
		}
		thread.process.mu.Unlock()
	}
	return identities
}
