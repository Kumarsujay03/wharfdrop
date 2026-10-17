//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var procGetConsoleProcessList = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// launchedByDoubleClick reports whether this process owns its console window,
// which is the case when it was started from Explorer or a Start menu shortcut
// rather than from an existing PowerShell or Command Prompt.
func launchedByDoubleClick() bool {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
