//go:build !windows

package nessyapp

import "path/filepath"

func canonicalExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
