//go:build windows

package main

import (
	"context"
	"fmt"
)

func platformRun(ctx context.Context, tuning runtimeTuning, pack *soundPack) error {
	if !mouseHoldMonitor {
		return fmt.Errorf("Windows build has no accelerometer; run with --mouse-hold (e.g. spank --mouse-hold)")
	}
	return listenForMouseOnly(ctx, pack, tuning)
}
