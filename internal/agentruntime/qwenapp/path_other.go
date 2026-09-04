//go:build !windows

package qwenapp

import "path/filepath"

func canonicalExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
