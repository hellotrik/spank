//go:build windows

package main

import (
	"os"
	"syscall"
)

var (
	kernel32Console      = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleWindow = kernel32Console.NewProc("GetConsoleWindow")
	procAllocConsole     = kernel32Console.NewProc("AllocConsole")
)

// ensureWindowsConsole allocates a console when this process has none, then
// refreshes os.Stdin/out/err from the kernel standard handles. Explorer
// double-click on a console-subsystem binary normally already has a console;
// this covers GUI-subsystem builds, detached launches, and other edge cases
// where bubbletea would otherwise fail without a real console.
func ensureWindowsConsole() {
	_ = kernel32Console.Load()
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd != 0 {
		return
	}
	r0, _, _ := procAllocConsole.Call()
	if r0 == 0 {
		return
	}
	syncOSStdFromKernel()
}

func syncOSStdFromKernel() {
	try := func(which int, name string) {
		h, err := syscall.GetStdHandle(which)
		if err != nil {
			return
		}
		if h == 0 || h == syscall.InvalidHandle {
			return
		}
		f := os.NewFile(uintptr(h), name)
		switch which {
		case syscall.STD_INPUT_HANDLE:
			os.Stdin = f
		case syscall.STD_OUTPUT_HANDLE:
			os.Stdout = f
		case syscall.STD_ERROR_HANDLE:
			os.Stderr = f
		}
	}
	try(syscall.STD_INPUT_HANDLE, "CONIN$")
	try(syscall.STD_OUTPUT_HANDLE, "CONOUT$")
	try(syscall.STD_ERROR_HANDLE, "CONERR$")
}
