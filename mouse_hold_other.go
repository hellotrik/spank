//go:build !darwin && !windows

package main

func leftMouseButtonDown() (bool, error) {
	return false, nil
}
