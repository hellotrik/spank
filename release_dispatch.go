package main

import (
	"encoding/json"
	"fmt"
	"time"
)

var useWindowTUI bool

func formatInputReleaseHuman(trigger string, at time.Time, d time.Duration, played bool, reason string, num int, amp float64, file string) string {
	ms := float64(d) / float64(time.Millisecond)
	label := "mouse"
	if trigger == "keyboard" {
		label = "keyboard"
	}
	switch {
	case played:
		return fmt.Sprintf("%s #%d [held %.1fms amp=%.3f] -> %s", label, num, ms, amp, file)
	case reason == "paused":
		return fmt.Sprintf("%s: held %.1fms (paused, no sound)", label, ms)
	case reason == "cooldown":
		return fmt.Sprintf("%s: held %.1fms (cooldown, no sound)", label, ms)
	case reason == "short":
		return fmt.Sprintf("%s: held %.1fms (too short, no sound)", label, ms)
	default:
		return fmt.Sprintf("%s: held %.1fms (no sound)", label, ms)
	}
}

// dispatchInputRelease writes file log, handles stdio JSON or plain/TUI output.
// trigger is "mouse" or "keyboard".
func dispatchInputRelease(trigger string, at time.Time, d time.Duration, played bool, reason string, num int, score, amp float64, file string) []string {
	human := formatInputReleaseHuman(trigger, at, d, played, reason, num, amp, file)
	fileLoggerMu.RLock()
	lg := fileLogger
	fileLoggerMu.RUnlock()
	if lg != nil {
		lg.WriteLine(human)
	}

	if stdioMode {
		ms := float64(d) / float64(time.Millisecond)
		evType := "mouse_hold_release"
		if trigger == "keyboard" {
			evType = "keyboard_hold_release"
		}
		ev := map[string]interface{}{
			"type":        evType,
			"duration_ms": ms,
			"timestamp":   at.Format(time.RFC3339Nano),
			"played":      played,
			"trigger":     trigger,
		}
		if reason != "" {
			ev["reason"] = reason
		}
		if played {
			ev["slapNumber"] = num
			ev["score"] = score
			ev["amplitude"] = amp
			ev["file"] = file
		}
		if data, err := json.Marshal(ev); err == nil {
			fmt.Println(string(data))
		}
		return nil
	}

	if useWindowTUI {
		return []string{human}
	}

	fmt.Println(human)
	return nil
}
