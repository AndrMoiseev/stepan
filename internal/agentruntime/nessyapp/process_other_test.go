//go:build !windows && !darwin

package nessyapp

import "os"

func makeDirectoryLink(link, target string) error { return os.Symlink(target, link) }
