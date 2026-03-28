//go:build !darwin && !windows

package main

import "errors"

var errKeyboardListenUnsupported = errors.New("keyboard listen is only supported on macOS and Windows")

func spaceKeyDown() (bool, error) {
	return false, errKeyboardListenUnsupported
}

func enterKeyDown() (bool, error) {
	return false, errKeyboardListenUnsupported
}
