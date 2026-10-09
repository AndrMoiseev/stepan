package impl_loop

import (
	"context"
	"errors"

	"github.com/AndrMoiseev/stepan/internal/setting"
)

// BootstrapConfigurationPaths names the user and project settings documents.
type BootstrapConfigurationPaths = setting.BootstrapConfigurationPaths

// BootstrapConfigurationDiff is the safe diff shown before confirmation.
type BootstrapConfigurationDiff = setting.BootstrapConfigurationDiff

// BootstrapConfigurationProposal is the validated, nonsecret pending update.
type BootstrapConfigurationProposal = setting.BootstrapConfigurationProposal

// BootstrapConfigurationPresenter obtains one confirmation for all changes.
type BootstrapConfigurationPresenter interface {
	ConfirmBootstrapConfiguration(context.Context, []BootstrapConfigurationDiff) (bool, error)
}

func defaultBootstrapConfigurationPaths(repository string) (BootstrapConfigurationPaths, error) {
	return setting.DefaultBootstrapConfigurationPaths(repository)
}

func PrepareBootstrapConfigurationProposal(paths BootstrapConfigurationPaths, response AgentResponse) (BootstrapConfigurationProposal, error) {
	if response.Kind != ResponseConfigurationProposed || response.UserSettings == nil || response.ProjectSettings == nil {
		return BootstrapConfigurationProposal{}, errors.New("bootstrap response does not contain a configuration proposal")
	}
	return setting.PrepareBootstrapConfigurationProposal(paths, *response.UserSettings, *response.ProjectSettings)
}

func SaveBootstrapConfigurationProposal(proposal BootstrapConfigurationProposal) error {
	return setting.SaveBootstrapConfigurationProposal(proposal)
}

func ConfirmAndSaveBootstrapConfiguration(ctx context.Context, proposal BootstrapConfigurationProposal, confirm func(context.Context, []BootstrapConfigurationDiff) (bool, error)) (bool, error) {
	return setting.ConfirmAndSaveBootstrapConfiguration(ctx, proposal, confirm)
}
