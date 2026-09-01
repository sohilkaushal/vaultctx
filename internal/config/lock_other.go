//go:build !darwin && !linux

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

func acquireProcessLock(path string, timeout time.Duration) (func(), error) {
	return acquireProcessLockContext(context.Background(), path, timeout)
}

func acquireProcessLockContext(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return func() {
				_ = lock.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create config lock: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for config lock %q", path)
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
