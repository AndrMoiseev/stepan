package specflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndrMoiseev/stepan/internal/agentruntime"
)

// RuntimeIdentity is the provider metadata written to review reports. It is
// presentation/runtime configuration, not part of the durable flow state.
type RuntimeIdentity struct {
	Provider string
	Model    string
}

// ApplicationController is the terminal-facing facade for the complete
// planning lifecycle. It owns input routing and feature-ID generation while
// FeatureController remains the sole owner of domain transitions.
type ApplicationController struct {
	workspace string
	runner    dialogueRunner
	manager   *ResumeManager
	flow      *FeatureController
	runtime   RuntimeIdentity
	now       func() time.Time
	current   Progress
}

func NewApplicationController(workspace string, runner dialogueRunner, manager *ResumeManager, flow *FeatureController, runtime RuntimeIdentity) (*ApplicationController, error) {
	if strings.TrimSpace(workspace) == "" || runner == nil || manager == nil || flow == nil {
		return nil, fmt.Errorf("create planning application: workspace, runner, resume manager, and feature controller are required")
	}
	if !validRuntimeMetadata(runtime.Provider) || !validRuntimeMetadata(runtime.Model) {
		return nil, fmt.Errorf("create planning application: provider and model are required")
	}
	return &ApplicationController{workspace: workspace, runner: runner, manager: manager, flow: flow, runtime: runtime, now: time.Now}, nil
}

func (c *ApplicationController) StartFeature(brief string) (Progress, error) {
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return Progress{}, fmt.Errorf("feature brief must not be empty")
	}
	featureID, err := c.generateFeatureID(brief)
	if err != nil {
		return Progress{}, err
	}
	at := c.now()
	target, err := PrepareFeatureTarget(c.workspace, at.Format("2006-01-02"), featureID)
	if err != nil {
		return Progress{}, fmt.Errorf("prepare feature identity: %w", err)
	}
	progress, err := c.manager.Begin(CreateFeatureRequest{FeatureID: target.ID, Brief: brief, At: at}, c.runtimeContext())
	c.current = progress
	return progress, err
}

func (c *ApplicationController) DiscoverResumable() ([]ResumableFlow, error) {
	return c.manager.Discover()
}

func (c *ApplicationController) Resume(featureID string) (Progress, error) {
	progress, err := c.manager.Activate(featureID, c.runtimeContext())
	c.current = progress
	return progress, err
}

func (c *ApplicationController) Submit(input string) (Progress, error) {
	trimmed := strings.TrimSpace(input)
	var (
		progress Progress
		err      error
	)
	switch trimmed {
	case "/review":
		progress, err = c.flow.StartReview(c.runtime.Provider, c.runtime.Model, c.runtimeContext())
	case "/apply":
		progress, err = c.flow.ApplyReview()
	case "/approve":
		progress, err = c.flow.Approve(c.runtimeContext())
	case "/revise-spec":
		progress, err = c.flow.ReviseSpec(c.runtimeContext())
	case "/status":
		progress, err = c.flow.Status()
	case "/non-material":
		progress, err = c.flow.ClassifyIntentRevision(IntentRevisionNonMaterial, "", c.runtimeContext())
	case "/material":
		featureID, idErr := c.generateSupersedingFeatureID()
		if idErr != nil {
			return c.current, idErr
		}
		progress, err = c.flow.ClassifyIntentRevision(IntentRevisionMaterial, featureID, c.runtimeContext())
	default:
		if action, scope, ok := parseRevisionInput(trimmed, c.current.Revision); ok {
			progress, err = c.flow.RevisionDecision(action, scope)
		} else if action, ok := parseFingerprintInput(trimmed, c.current.Review.FingerprintActions); ok {
			progress, err = c.flow.ReviewFingerprintDecision(action)
		} else if c.current.ReviewStatus == ReviewRunning || c.current.ReviewStatus == ReviewAwaitingDecisions {
			progress, err = c.flow.ReviewMessage(input)
		} else {
			progress, err = c.flow.AuthorMessage(input)
		}
	}
	c.current = progress
	return progress, err
}

func (c *ApplicationController) Close() (Progress, error) {
	progress, err := c.manager.Close()
	c.current = progress
	return progress, err
}

func (c *ApplicationController) generateFeatureID(brief string) (string, error) {
	thread, err := c.runner.StartThread(agentruntime.ThreadConfig{Workspace: c.workspace, OutputSchema: FeatureIDSchema()})
	if err != nil {
		return "", fmt.Errorf("start feature-id request: %w", err)
	}
	raw, turnErr := c.runner.RunTurn(thread, FeatureIDPrompt(brief))
	closeErr := c.runner.CloseThread(thread)
	if turnErr != nil {
		return "", fmt.Errorf("generate feature-id: %w", turnErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close feature-id thread: %w", closeErr)
	}
	result, err := DecodeFeatureID(raw)
	if err != nil {
		return "", fmt.Errorf("validate feature-id: %w", err)
	}
	return result.FeatureID, nil
}

func (c *ApplicationController) generateSupersedingFeatureID() (string, error) {
	brief := "Material revision of feature " + c.current.FeatureID
	if c.current.Path != "" {
		path := c.current.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(c.workspace, filepath.FromSlash(path))
		}
		if document, err := os.ReadFile(path); err == nil {
			brief += ":\n" + string(document)
		}
	}
	shortID, err := c.generateFeatureID(brief)
	if err != nil {
		return "", err
	}
	target, err := PrepareFeatureTarget(c.workspace, c.now().Format("2006-01-02"), shortID)
	if err != nil {
		return "", fmt.Errorf("prepare superseding feature identity: %w", err)
	}
	return target.ID, nil
}

func (c *ApplicationController) runtimeContext() string {
	return fmt.Sprintf("Provider: %s\nModel: %s", c.runtime.Provider, c.runtime.Model)
}

func parseRevisionInput(input string, available []RevisionAction) (RevisionAction, string, bool) {
	parts := strings.SplitN(input, " ", 2)
	action := RevisionAction(parts[0])
	if !containsRevisionAction(available, action) {
		return "", "", false
	}
	scope := ""
	if len(parts) == 2 {
		scope = strings.TrimSpace(parts[1])
	}
	return action, scope, true
}

func containsRevisionAction(actions []RevisionAction, action RevisionAction) bool {
	for _, candidate := range actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func parseFingerprintInput(input string, available []ReviewFingerprintAction) (ReviewFingerprintAction, bool) {
	action := ReviewFingerprintAction(input)
	for _, candidate := range available {
		if candidate == action {
			return action, true
		}
	}
	return "", false
}
