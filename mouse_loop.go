//go:build darwin || windows

package main

import (
	"sync"
	"time"
)

// mouseLoopRuntime holds per-session mouse + audio state for one listen/TUI run.
type mouseLoopRuntime struct {
	mu          sync.Mutex
	Pack        *soundPack
	Tuning      runtimeTuning
	Tracker     *slapTracker
	SpeakerInit bool
	MouseState  mouseHoldState
	LastYell    time.Time
}

// switchToPack replaces the active pack and resets slap state (TUI / future hot-swap).
func (rt *mouseLoopRuntime) switchToPack(pack *soundPack) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.Pack = pack
	rt.Tracker = newSlapTracker(pack, rt.Tuning.cooldown)
	rt.LastYell = time.Time{}
}

// tick samples the mouse and dispatches one release event if any. Returns
// non-nil UI lines only in window-TUI mode (plain/stdio print inside dispatch).
func (rt *mouseLoopRuntime) tick(now time.Time) []string {
	released, relTime, holdDur := updateMouseLeftHold(&rt.MouseState, now)
	if !released {
		return nil
	}

	pausedMu.RLock()
	isPaused := paused
	pausedMu.RUnlock()

	soundMu.RLock()
	cdMs := cooldownMs
	soundMu.RUnlock()
	cooldown := time.Duration(cdMs) * time.Millisecond

	var reason string
	var played bool
	var num int
	var score, amp float64
	var file string

	rt.mu.Lock()
	switch {
	case isPaused:
		reason = "paused"
	case holdDur < mouseHoldMinPlay:
		reason = "short"
	case time.Since(rt.LastYell) <= cooldown:
		reason = "cooldown"
	default:
		played = true
		rt.LastYell = now
		amp = mouseHoldDurationToAmplitude(holdDur)
		num, score = rt.Tracker.record(now)
		file = rt.Tracker.getFile(score)
		pack := rt.Pack
		rt.mu.Unlock()
		go playAudio(pack, file, amp, &rt.SpeakerInit)
		return dispatchMouseRelease(relTime, holdDur, played, reason, num, score, amp, file)
	}
	rt.mu.Unlock()
	return dispatchMouseRelease(relTime, holdDur, played, reason, num, score, amp, file)
}
