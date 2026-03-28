//go:build windows

package main

import (
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

// bubbleTeaPlatformOptions attaches bubbletea to the real console when stdio
// handles are not console devices (common when stdin/stdout are pipes or
// NUL). Closing the returned cleanup releases any extra *os.File we opened.
func bubbleTeaPlatformOptions() (cleanup func(), extra []tea.ProgramOption) {
	var files []*os.File
	cleanup = func() {
		for _, f := range files {
			_ = f.Close()
		}
	}

	if f, ok := any(os.Stdout).(term.File); ok && !term.IsTerminal(f.Fd()) {
		if c, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
			files = append(files, c)
			extra = append(extra, tea.WithOutput(c))
		}
	}
	if f, ok := any(os.Stdin).(term.File); ok && !term.IsTerminal(f.Fd()) {
		extra = append(extra, tea.WithInputTTY())
	} else if !ok {
		extra = append(extra, tea.WithInputTTY())
	}

	return cleanup, extra
}
