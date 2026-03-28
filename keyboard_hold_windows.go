//go:build windows

package main

const vkReturn = 0x0D // Enter (main keyboard)

// enterKeyDown reports whether Enter is currently pressed.
func enterKeyDown() (bool, error) {
	if err := user32.Load(); err != nil {
		return false, err
	}
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vkReturn))
	return r&0x8000 != 0, nil
}
