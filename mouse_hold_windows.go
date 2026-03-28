//go:build windows

package main

import "syscall"

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
)

// VK_LBUTTON — left mouse button.
const vkLButton = 0x01

func leftMouseButtonDown() (bool, error) {
	if err := user32.Load(); err != nil {
		return false, err
	}
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vkLButton))
	return r&0x8000 != 0, nil
}
