//go:build darwin || windows

package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"
)

func listenForMouseOnly(ctx context.Context, pack *soundPack, tuning runtimeTuning) error {
	if useWindowTUI {
		return runWindowTUI(ctx, pack, tuning)
	}
	return listenMousePlain(ctx, pack, tuning)
}

func listenMousePlain(ctx context.Context, pack *soundPack, tuning runtimeTuning) error {
	rt := &mouseLoopRuntime{
		Pack:    pack,
		Tuning:  tuning,
		Tracker: newSlapTracker(pack, tuning.cooldown),
	}

	if stdioMode {
		go readStdinCommands()
	}

	presetLabel := "default"
	if fastMode {
		presetLabel = "fast"
	}

	fmt.Printf("spank: mouse and/or Space/Enter key release → sound (%s pack, %s tuning); ctrl+c to quit · --mouse/--keyboard\n", pack.name, presetLabel)
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "spank: if cmd title shows “Select”/「选择」or you dragged to select text, Windows pauses this program — press Esc or disable Quick Edit Mode (cmd → Properties → Options).")
	}

	if stdioMode {
		fmt.Printf("{\"status\":\"ready\",\"platform\":\"%s\"}\n", runtime.GOOS)
	}

	ticker := time.NewTicker(tuning.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nbye!")
			return nil
		case <-ticker.C:
		}
		rt.tick(time.Now())
	}
}
