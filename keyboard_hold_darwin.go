//go:build darwin

package main

import (
	"sync"

	"github.com/ebitengine/purego"
)

// Virtual key codes (Events.h).
const (
	kVKSpace       uint16 = 0x31
	kVKReturn      uint16 = 0x24 // main keyboard Return / Enter
	kVKKeypadEnter uint16 = 0x4C // keypad Enter
)

var (
	keyboardCGOnce    sync.Once
	keyboardCGInitErr error
	// CoreGraphics uses MacTypes Boolean = unsigned char; bool would mis-decode via purego.
	cgKeyState func(state uint32, key uint16) byte
)

func ensureKeyboardCG() error {
	keyboardCGOnce.Do(func() {
		path := "/System/Library/Frameworks/CoreGraphics.framework/Versions/Current/CoreGraphics"
		lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			keyboardCGInitErr = err
			return
		}
		purego.RegisterLibFunc(&cgKeyState, lib, "CGEventSourceKeyState")
	})
	return keyboardCGInitErr
}

// spaceKeyDown reports whether Space is physically down (HID via CoreGraphics).
func spaceKeyDown() (bool, error) {
	if err := ensureKeyboardCG(); err != nil {
		return false, err
	}
	return cgKeyState(kCGEventSourceStateHIDSystemState, kVKSpace) != 0, nil
}

// enterKeyDown reports whether Return or keypad Enter is physically down.
func enterKeyDown() (bool, error) {
	if err := ensureKeyboardCG(); err != nil {
		return false, err
	}
	st := kCGEventSourceStateHIDSystemState
	a := cgKeyState(st, kVKReturn) != 0
	b := cgKeyState(st, kVKKeypadEnter) != 0
	return a || b, nil
}
