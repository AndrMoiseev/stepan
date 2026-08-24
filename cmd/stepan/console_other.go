//go:build !windows

package main

import "os"

func isConsole(*os.File) bool { return false }
