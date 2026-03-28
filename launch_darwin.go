//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
)

// envDarwinInTerminal is set when we're already running inside Terminal (or any TTY), including
// after relaunch from Finder-launched .app.
const envDarwinInTerminal = "SPANK_DARWIN_IN_TERMINAL"

// ensureDarwinTTYForWindowTUI fixes double-click on a .app bundle: Launch Services starts the binary
// with no controlling terminal, so bubbletea has no TTY and exits immediately. We re-exec via
// Terminal.app when a TUI session is required.
func ensureDarwinTTYForWindowTUI() {
	if stdioMode || plainOutput || !useWindowTUI {
		return
	}
	if os.Getenv(envDarwinInTerminal) == "1" {
		return
	}
	if f, ok := any(os.Stdout).(term.File); ok && term.IsTerminal(f.Fd()) {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "spank: %v\n", err)
		fallbackPlainNoTTY()
		return
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		exe, _ = os.Executable()
	}

	inner := fmt.Sprintf("export %s=1; exec %s", envDarwinInTerminal, shellSingleQuoted(exe))
	script := `tell application "Terminal" to activate` + "\n" +
		`tell application "Terminal" to do script ` + appleScriptQuotedString(inner)

	cmd := exec.Command("osascript", "-e", script)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "spank: could not open Terminal (%v); falling back to plain output\n", err)
		fallbackPlainNoTTY()
		return
	}
	os.Exit(0)
}

func fallbackPlainNoTTY() {
	plainOutput = true
	useWindowTUI = false
}

func shellSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// appleScriptQuotedString produces a double-quoted AppleScript string literal.
func appleScriptQuotedString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
