//go:build !darwin && !linux

package app

import (
	"context"
	"os/exec"
)

// runManagedCommand provides safe cancellation on platforms where this package
// cannot portably create and signal a Unix process group. The direct child is
// still terminated, and the build remains functional on Windows and other OSes.
func runManagedCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
	}()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		select {
		case err := <-wait:
			return err
		default:
		}
		_ = cmd.Process.Kill()
		<-wait
		return ctx.Err()
	}
}
