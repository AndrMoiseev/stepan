//go:build !windows

package main

import (
	"os"

	"github.com/charmbracelet/x/term"
)

func isConsole(file *os.File) bool {
	return file != nil && term.IsTerminal(file.Fd())
}
