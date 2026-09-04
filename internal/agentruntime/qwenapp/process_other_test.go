//go:build !windows && !darwin

package qwenapp

import "os"

func makeDirectoryLink(link, target string) error { return os.Symlink(target, link) }
