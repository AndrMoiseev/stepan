//go:build !windows

package implementationconfig

import "path/filepath"

func canonicalExistingRulesPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
