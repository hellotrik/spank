//go:build !darwin && !windows

package main

import (
	"context"
	"fmt"
	"runtime"
)

func platformRun(ctx context.Context, tuning runtimeTuning, pack *soundPack) error {
	_, _, _ = ctx, tuning, pack
	return fmt.Errorf("spank: unsupported GOOS %q (build for darwin or windows)", runtime.GOOS)
}
