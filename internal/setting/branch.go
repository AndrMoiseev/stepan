package setting

import (
	"encoding/json"
	"errors"
	"strings"
)

// MainBranchName returns the configured project branch name. An empty result
// means the implementation flow should discover the repository's default.
func (configuration Configuration) MainBranchName() (string, error) {
	if len(configuration.MainBranch) == 0 {
		return "", nil
	}
	var branch string
	if err := json.Unmarshal(configuration.MainBranch, &branch); err != nil || strings.TrimSpace(branch) == "" {
		return "", errors.New("project flows.impl_loop.main_branch must be a non-empty string")
	}
	return branch, nil
}
