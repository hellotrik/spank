//go:build windows

package main

import (
	"context"
	"fmt"
	"time"
)

func listenForMouseOnly(ctx context.Context, pack *soundPack, tuning runtimeTuning) error {
	tracker := newSlapTracker(pack, tuning.cooldown)
	speakerInit := false
	var lastYell time.Time

	if stdioMode {
		go readStdinCommands()
	}

	presetLabel := "default"
	if fastMode {
		presetLabel = "fast"
	}
	fmt.Printf("spank: Windows — mouse hold triggers only (%s pack, %s tuning); ctrl+c to quit\n", pack.name, presetLabel)
	if stdioMode {
		fmt.Println(`{"status":"ready","platform":"windows"}`)
	}

	ticker := time.NewTicker(tuning.pollInterval)
	defer ticker.Stop()

	var mouseState mouseHoldState
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nbye!")
			return nil
		case <-ticker.C:
		}

		now := time.Now()
		released, relTime, holdDur := updateMouseLeftHold(&mouseState, now)

		pausedMu.RLock()
		isPaused := paused
		pausedMu.RUnlock()

		if released {
			cooldown := time.Duration(cooldownMs) * time.Millisecond
			var reason string
			played := false
			var num int
			var score, amp float64
			var file string
			switch {
			case isPaused:
				reason = "paused"
			case holdDur < mouseHoldMinPlay:
				reason = "short"
			case time.Since(lastYell) <= cooldown:
				reason = "cooldown"
			default:
				played = true
				lastYell = now
				amp = mouseHoldDurationToAmplitude(holdDur)
				num, score = tracker.record(now)
				file = tracker.getFile(score)
				go playAudio(pack, file, amp, &speakerInit)
			}
			emitMouseRelease(relTime, holdDur, played, reason, num, score, amp, file)
		}
	}
}
