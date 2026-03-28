//go:build darwin

package main

import (
	"sync"

	"github.com/ebitengine/purego"
)

// CoreGraphics CGEventSourceStateID / CGMouseButton (physical HID state).
const (
	kCGEventSourceStateHIDSystemState uint32 = 1
	kCGMouseButtonLeft                uint32 = 0
)

var (
	mouseCGOnce    sync.Once
	mouseCGInitErr error
	// CoreGraphics Boolean return is unsigned char.
	cgButtonState func(state, button uint32) byte
)

func ensureCoreGraphicsMouse() error {
	mouseCGOnce.Do(func() {
		path := "/System/Library/Frameworks/CoreGraphics.framework/Versions/Current/CoreGraphics"
		lib, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			mouseCGInitErr = err
			return
		}
		purego.RegisterLibFunc(&cgButtonState, lib, "CGEventSourceButtonState")
	})
	return mouseCGInitErr
}

// leftMouseButtonDown reports whether the left mouse button is currently down
// (hardware / HID session state via CoreGraphics).
func leftMouseButtonDown() (bool, error) {
	if err := ensureCoreGraphicsMouse(); err != nil {
		return false, err
	}
	return cgButtonState(kCGEventSourceStateHIDSystemState, kCGMouseButtonLeft) != 0, nil
}
