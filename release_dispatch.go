package main

import (
	"encoding/json"
	"fmt"
	"time"
)

var (
	fileLogger     *rotatingDailyLogger
	useWindowTUI   bool
)

func formatMouseReleaseHuman(at time.Time, d time.Duration, played bool, reason string, num int, amp float64, file string) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case played:
		return fmt.Sprintf("mouse #%d [held %.1fms amp=%.3f] -> %s", num, ms, amp, file)
	case reason == "paused":
		return fmt.Sprintf("mouse: left held %.1fms (paused, no sound)", ms)
	case reason == "cooldown":
		return fmt.Sprintf("mouse: left held %.1fms (cooldown, no sound)", ms)
	case reason == "short":
		return fmt.Sprintf("mouse: left held %.1fms (too short, no sound)", ms)
	default:
		return fmt.Sprintf("mouse: left held %.1fms (no sound)", ms)
	}
}

// dispatchMouseRelease writes file log, handles stdio JSON or plain/TUI output.
// Returns a one-line slice for window TUI to append; nil otherwise.
func dispatchMouseRelease(at time.Time, d time.Duration, played bool, reason string, num int, score, amp float64, file string) []string {
	human := formatMouseReleaseHuman(at, d, played, reason, num, amp, file)
	if fileLogger != nil {
		fileLogger.WriteLine(human)
	}

	if stdioMode {
		ms := float64(d) / float64(time.Millisecond)
		ev := map[string]interface{}{
			"type":        "mouse_hold_release",
			"duration_ms": ms,
			"timestamp":   at.Format(time.RFC3339Nano),
			"played":      played,
			"trigger":     "mouse",
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

func logFileLine(msg string) {
	if fileLogger != nil {
		fileLogger.WriteLine(msg)
	}
}
