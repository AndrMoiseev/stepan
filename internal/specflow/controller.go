package specflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
	"github.com/AndrMoiseev/stepan/internal/gitsnapshot"
)

type State uint8

const (
	StateIdle State = iota
	StateAwaitingBrief
	StateDraft
	StateAwaitingChangeAnswer
)

type Progress struct {
	State    State
	Question string
	Answer   string
	SpecID   string
	Path     string
}

type initialTurnRunner interface {
	StartThread() (agentruntime.Thread, error)
	RunTurn(agentruntime.Thread, string, agentruntime.TurnOptions) (json.RawMessage, error)
}

type Controller struct {
	runner   initialTurnRunner
	root     string
	state    State
	brief    string
	thread   agentruntime.Thread
	question string
	specID   string
	path     string
}

func NewController(root string, runner initialTurnRunner) *Controller {
	return &Controller{root: root, runner: runner}
}

func (controller *Controller) StartFeature(brief string) (Progress, error) {
	controller.reset()
	if controller.runner == nil {
		return controller.Progress(), fmt.Errorf("start feature: turn runner is required")
	}
	thread, err := controller.runner.StartThread()
	if err != nil {
		return controller.Progress(), fmt.Errorf("start feature thread: %w", err)
	}
	controller.thread = thread
	controller.brief = brief
	if brief == "" {
		controller.state = StateAwaitingBrief
		return controller.Progress(), nil
	}
	return controller.createInitialDraft(context.Background(), brief)
}

// StartIdea is retained for package-level compatibility. Interactive users must
// invoke /feature; new callers should use StartFeature.
func (controller *Controller) StartIdea(brief string) (Progress, error) {
	return controller.StartFeature(brief)
}

func (controller *Controller) Submit(text string) (Progress, error) {
	switch controller.state {
	case StateAwaitingBrief:
		controller.brief = text
		return controller.createInitialDraft(context.Background(), text)
	default:
		return controller.Progress(), fmt.Errorf("feature brief is not awaiting input")
	}
}

func (controller *Controller) Progress() Progress {
	return Progress{State: controller.state, Question: controller.question, SpecID: controller.specID, Path: controller.path}
}

func (controller *Controller) AskQuestion(question string) (Progress, error) {
	if controller.state != StateDraft {
		return controller.Progress(), fmt.Errorf("specification draft is not available")
	}
	output, err := controller.runner.RunTurn(controller.thread, QuestionPrompt(question), agentruntime.TurnOptions{
		OutputSchema: QuestionSchema(),
		Policy:       agentruntime.ReadOnlyTurnPolicy(),
	})
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("ask specification question: %w", err)
	}
	result, err := DecodeQuestionResult(output)
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("validate specification answer: %w", err)
	}
	progress := controller.Progress()
	progress.Answer = result.Message
	return progress, nil
}

func (controller *Controller) ProposeChange(ctx context.Context, request string) (Progress, error) {
	if controller.state != StateDraft {
		return controller.Progress(), fmt.Errorf("specification draft is not available")
	}
	return controller.analyzeChange(ctx, ChangePrompt(request))
}

func (controller *Controller) SubmitChangeAnswer(ctx context.Context, answer string) (Progress, error) {
	if controller.state != StateAwaitingChangeAnswer {
		return controller.Progress(), fmt.Errorf("specification change is not awaiting input")
	}
	return controller.analyzeChange(ctx, changeAnswerPrompt(answer))
}

func (controller *Controller) Approve() (Progress, error) {
	if controller.state != StateDraft {
		return controller.Progress(), fmt.Errorf("specification draft is not available")
	}
	entrypoint := filepath.Join(controller.root, filepath.FromSlash(controller.path))
	info, err := os.Lstat(entrypoint)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return controller.Progress(), fmt.Errorf("approve specification entrypoint %q: %w", entrypoint, err)
	}
	controller.reset()
	return controller.Progress(), nil
}

func (controller *Controller) createInitialDraft(ctx context.Context, brief string) (Progress, error) {
	featuresDirectory, err := PrepareFeaturesDirectory(controller.root)
	if err != nil {
		return controller.failDraft(fmt.Errorf("prepare feature directory: %w", err))
	}
	entries, err := featureEntries(featuresDirectory)
	if err != nil {
		return controller.failDraft(fmt.Errorf("read feature directory: %w", err))
	}
	policy, err := agentruntime.SingleWriteRootTurnPolicy(featuresDirectory)
	if err != nil {
		return controller.failDraft(fmt.Errorf("prepare write policy: %w", err))
	}
	baseline, err := gitsnapshot.Capture(ctx, controller.root)
	if err != nil {
		return controller.failDraft(fmt.Errorf("capture pre-write repository: %w", err))
	}

	output, turnErr := controller.runner.RunTurn(controller.thread, InitialPrompt(brief, featuresDirectory), agentruntime.TurnOptions{
		OutputSchema: InitialSchema(),
		Policy:       policy,
	})
	var resultErr error
	var target SpecTarget
	if turnErr != nil {
		resultErr = fmt.Errorf("run initial draft turn: %w", turnErr)
	} else if result, err := DecodeInitialResult(output); err != nil {
		resultErr = fmt.Errorf("validate initial draft result: %w", err)
	} else if _, exists := entries[result.SpecID]; exists {
		resultErr = fmt.Errorf("specification directory for feature-id %q already exists", result.SpecID)
	} else if target, err = SpecTargetForID(controller.root, result.SpecID); err != nil {
		resultErr = fmt.Errorf("prepare specification target: %w", err)
	}
	if resultErr == nil {
		if err := controller.validateWrite(ctx, target, baseline, resultErr); err != nil {
			return controller.failDraft(err)
		}
	} else {
		// Still inspect writes after any failed agent turn or malformed result so
		// callers receive the same boundary and entrypoint diagnostics.
		target = SpecTarget{Directory: featuresDirectory}
		if err := controller.validateInitialWrite(ctx, baseline, resultErr); err != nil {
			return controller.failDraft(err)
		}
	}

	result, _ := DecodeInitialResult(output)
	controller.state = StateDraft
	controller.specID = result.SpecID
	controller.path = target.DisplayPath
	return controller.Progress(), nil
}

func (controller *Controller) validateInitialWrite(ctx context.Context, baseline gitsnapshot.Snapshot, resultErr error) error {
	var postErrors []error
	if resultErr != nil {
		postErrors = append(postErrors, resultErr)
	}
	if after, err := gitsnapshot.Capture(ctx, controller.root); err != nil {
		postErrors = append(postErrors, fmt.Errorf("capture post-write repository: %w", err))
	} else if changed, err := gitsnapshot.Compare(ctx, controller.root, baseline, after); err != nil {
		postErrors = append(postErrors, fmt.Errorf("compare repository snapshots: %w", err))
	} else if err := gitsnapshot.CheckBoundary(changed, displayFeaturesDirectory()); err != nil {
		postErrors = append(postErrors, err)
	}
	return errors.Join(postErrors...)
}

func (controller *Controller) validateWrite(ctx context.Context, target SpecTarget, baseline gitsnapshot.Snapshot, resultErr error) error {
	var postErrors []error
	if resultErr != nil {
		postErrors = append(postErrors, resultErr)
	}
	if err := CheckContainment(controller.root, target.Directory); err != nil {
		postErrors = append(postErrors, fmt.Errorf("verify specification target: %w", err))
	}
	if info, err := os.Lstat(target.Entrypoint); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		postErrors = append(postErrors, fmt.Errorf("verify specification entrypoint %q: %w", target.Entrypoint, err))
	}
	if after, err := gitsnapshot.Capture(ctx, controller.root); err != nil {
		postErrors = append(postErrors, fmt.Errorf("capture post-write repository: %w", err))
	} else if changed, err := gitsnapshot.Compare(ctx, controller.root, baseline, after); err != nil {
		postErrors = append(postErrors, fmt.Errorf("compare repository snapshots: %w", err))
	} else if err := gitsnapshot.CheckBoundary(changed, path.Dir(target.DisplayPath)); err != nil {
		postErrors = append(postErrors, err)
	}
	return errors.Join(postErrors...)
}

func (controller *Controller) analyzeChange(ctx context.Context, prompt string) (Progress, error) {
	output, err := controller.runner.RunTurn(controller.thread, prompt, agentruntime.TurnOptions{
		OutputSchema: ChangeSchema(),
		Policy:       agentruntime.ReadOnlyTurnPolicy(),
	})
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("analyze specification change: %w", err)
	}
	result, err := DecodeChangeResult(output)
	if err != nil {
		controller.reset()
		return controller.Progress(), fmt.Errorf("validate specification change analysis: %w", err)
	}
	controller.question = ""
	if result.Status == StatusNeedsInput {
		controller.state = StateAwaitingChangeAnswer
		controller.question = result.Message
		return controller.Progress(), nil
	}
	return controller.updateDraft(ctx)
}

func (controller *Controller) updateDraft(ctx context.Context) (Progress, error) {
	target := controller.target()
	if err := CheckContainment(controller.root, target.Directory); err != nil {
		return controller.failUpdate(fmt.Errorf("verify specification target: %w", err))
	}
	policy, err := agentruntime.SingleWriteRootTurnPolicy(target.Directory)
	if err != nil {
		return controller.failUpdate(fmt.Errorf("prepare write policy: %w", err))
	}
	baseline, err := gitsnapshot.Capture(ctx, controller.root)
	if err != nil {
		return controller.failUpdate(fmt.Errorf("capture pre-write repository: %w", err))
	}
	output, turnErr := controller.runner.RunTurn(controller.thread, UpdatePrompt(target.Directory), agentruntime.TurnOptions{
		OutputSchema: UpdateSchema(),
		Policy:       policy,
	})
	var resultErr error
	if turnErr != nil {
		resultErr = fmt.Errorf("run update turn: %w", turnErr)
	} else if _, err := DecodeUpdateResult(output); err != nil {
		resultErr = fmt.Errorf("validate update result: %w", err)
	}
	if err := controller.validateWrite(ctx, target, baseline, resultErr); err != nil {
		return controller.failUpdate(err)
	}
	controller.state = StateDraft
	controller.question = ""
	return controller.Progress(), nil
}

func (controller *Controller) failDraft(err error) (Progress, error) {
	controller.reset()
	return controller.Progress(), fmt.Errorf("initial draft failed; cleanup was not performed: %w", err)
}

func (controller *Controller) failUpdate(err error) (Progress, error) {
	controller.reset()
	return controller.Progress(), fmt.Errorf("update draft failed; cleanup was not performed: %w", err)
}

func (controller *Controller) target() SpecTarget {
	entrypoint := filepath.Join(controller.root, filepath.FromSlash(controller.path))
	return SpecTarget{Directory: filepath.Dir(entrypoint), Entrypoint: entrypoint, DisplayPath: controller.path}
}

func (controller *Controller) reset() {
	controller.state = StateIdle
	controller.brief = ""
	controller.thread = nil
	controller.question = ""
	controller.specID = ""
	controller.path = ""
}

func changeAnswerPrompt(answer string) string {
	return `Продолжи анализ предложения пользователя относительно текущей спецификации и
репозитория. Сначала заново прочитай текущие файлы спецификации с диска. Пока не
изменяй файлы.

Если материального решения всё ещё нет, верни NEEDS_INPUT и задай ровно один
уточняющий вопрос. Если информации достаточно для согласованной правки, верни
READY_TO_UPDATE.

USER ANSWER:
` + answer
}
