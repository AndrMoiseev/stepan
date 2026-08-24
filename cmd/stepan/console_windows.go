//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var getConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleMode")

func isConsole(file *os.File) bool {
	var mode uint32
	ok, _, _ := getConsoleMode.Call(file.Fd(), uintptr(unsafe.Pointer(&mode)))
	return ok != 0
}
