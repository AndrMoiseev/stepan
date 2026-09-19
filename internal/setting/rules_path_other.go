//go:build !windows

package setting

import "path/filepath"

func canonicalExistingRulesPath(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
