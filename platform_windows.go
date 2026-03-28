//go:build windows

package main

import "context"

func platformRun(ctx context.Context, tuning runtimeTuning, pack *soundPack) error {
	return listenForMouseOnly(ctx, pack, tuning)
}
