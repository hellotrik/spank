//go:build windows

package main

const (
	vkSpace  = 0x20
	vkReturn = 0x0D // Enter (main and numpad)
)

// spaceKeyDown reports whether Space is currently pressed (GetAsyncKeyState).
func spaceKeyDown() (bool, error) {
	if err := user32.Load(); err != nil {
		return false, err
	}
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vkSpace))
	return r&0x8000 != 0, nil
}

// enterKeyDown reports whether Enter is currently pressed.
func enterKeyDown() (bool, error) {
	if err := user32.Load(); err != nil {
		return false, err
	}
	r, _, _ := procGetAsyncKeyState.Call(uintptr(vkReturn))
	return r&0x8000 != 0, nil
}
