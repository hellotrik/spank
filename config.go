package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"
)

const spankConfigFileName = "spank.json"

// Bounds match tui.go / soundSetCooldown (tui is not built on all GOOS).
const (
	cfgMinCooldown = 50
	cfgMaxCooldown = 60000
	cfgMinSpeed    = 0.25
	cfgMaxSpeed    = 4.0
)

func clampConfigBounds() {
	if cooldownMs < cfgMinCooldown {
		cooldownMs = cfgMinCooldown
	}
	if cooldownMs > cfgMaxCooldown {
		cooldownMs = cfgMaxCooldown
	}
	if speedRatio < cfgMinSpeed {
		speedRatio = cfgMinSpeed
	}
	if speedRatio > cfgMaxSpeed {
		speedRatio = cfgMaxSpeed
	}
}

// spankConfigJSON is persisted next to the working directory when the process starts.
type spankConfigJSON struct {
	Version          int     `json:"version"`
	Pack             string  `json:"pack,omitempty"`
	CooldownMs       int     `json:"cooldown_ms"`
	Speed            float64 `json:"speed"`
	VolumeScaling    bool    `json:"volume_scaling"`
	InputMode        string  `json:"input_mode"`
	Log              bool    `json:"log"`
	LogDir           string  `json:"log_dir"`
	LogRetentionDays int     `json:"log_retention_days"`
}

const (
	inputModeMouse    = "mouse"
	inputModeKeyboard = "keyboard"
	inputModeBoth     = "both"
)

var (
	configMu              sync.Mutex
	embeddedPackForConfig string // last embedded pack id (pain, sexy, …); updated on load and TUI cycle
	appRunContext         context.Context
)

func spankConfigPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, spankConfigFileName), nil
}

func applyInputModeString(s string) {
	inputListenMu.Lock()
	defer inputListenMu.Unlock()
	switch s {
	case inputModeKeyboard:
		listenMouse, listenKeyboard = false, true
	case inputModeBoth:
		listenMouse, listenKeyboard = true, true
	default:
		listenMouse, listenKeyboard = true, false
	}
}

func inputModeString() string {
	m, k := inputListenSnapshot()
	if m && k {
		return inputModeBoth
	}
	if k {
		return inputModeKeyboard
	}
	return inputModeMouse
}

func cycleInputMode(delta int) {
	cur := inputModeString()
	order := []string{inputModeMouse, inputModeKeyboard, inputModeBoth}
	idx := 0
	for i, x := range order {
		if x == cur {
			idx = i
			break
		}
	}
	n := len(order)
	idx = (idx + delta%n + n) % n
	applyInputModeString(order[idx])
	logFileLine(fmt.Sprintf("tui: input_mode=%s", order[idx]))
	persistSpankConfig()
}

// loadSpankConfig merges spank.json into globals. When cmd is non-nil, fields with an
// explicit CLI flag take precedence over the file.
func loadSpankConfig(cmd *cobra.Command) error {
	path, err := spankConfigPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var c spankConfigJSON
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("spank.json: %w", err)
	}
	if c.Version != 0 && c.Version != 1 {
		return fmt.Errorf("spank.json: unsupported version %d", c.Version)
	}

	changed := func(name string) bool {
		if cmd == nil {
			return false
		}
		return cmd.Flags().Changed(name)
	}

	if !fastMode && !changed("cooldown") && c.CooldownMs > 0 {
		cooldownMs = c.CooldownMs
	}
	if !changed("speed") && c.Speed > 0 {
		speedRatio = c.Speed
	}
	if !changed("volume-scaling") {
		volumeScaling = c.VolumeScaling
	}
	if !changed("mouse") && !changed("keyboard") && c.InputMode != "" {
		applyInputModeString(c.InputMode)
	}
	if !changed("log") {
		logToFile = c.Log
	}
	if !changed("log-dir") && c.LogDir != "" {
		logDir = c.LogDir
	}
	if !changed("log-retention-days") && c.LogRetentionDays > 0 {
		logRetention = c.LogRetentionDays
	}
	if c.Pack != "" && !sexyMode && !haloMode && !lizardMode && !swardMode && customPath == "" && len(customFiles) == 0 {
		embeddedPackForConfig = c.Pack
	}
	return nil
}

// persistSpankConfig writes current settings to ./spank.json (best-effort).
func persistSpankConfig() {
	configMu.Lock()
	defer configMu.Unlock()
	path, err := spankConfigPath()
	if err != nil {
		return
	}
	c := spankConfigJSON{
		Version:          1,
		CooldownMs:       cooldownMs,
		Speed:            speedRatio,
		VolumeScaling:    volumeScaling,
		InputMode:        inputModeString(),
		Log:              logToFile,
		LogDir:           logDir,
		LogRetentionDays: logRetention,
	}
	if embeddedPackForConfig != "" {
		c.Pack = embeddedPackForConfig
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func setLogEnabled(ctx context.Context, on bool) {
	logToFile = on
	if on {
		_ = enableFileLogging(ctx)
	} else {
		disableFileLogging()
	}
	persistSpankConfig()
}
