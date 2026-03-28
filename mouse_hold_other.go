//go:build !darwin

package main

func leftMouseButtonDown() (bool, error) {
	return false, nil
}
