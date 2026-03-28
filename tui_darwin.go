//go:build darwin

package main

import tea "github.com/charmbracelet/bubbletea"

func bubbleTeaPlatformOptions() (cleanup func(), extra []tea.ProgramOption) {
	return func() {}, nil
}
