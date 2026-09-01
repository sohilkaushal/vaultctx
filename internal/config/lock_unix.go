//go:build darwin || linux

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func acquireProcessLock(path string, timeout time.Duration) (func(), error) {
	return acquireProcessLockContext(context.Background(), path, timeout)
}

func acquireProcessLockContext(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			_ = lock.Close()
			return nil, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				_ = lock.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lock.Close()
			return nil, fmt.Errorf("lock config: %w", err)
		}
		if time.Now().After(deadline) {
			_ = lock.Close()
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
			_ = lock.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func openLockFile(path string) (*os.File, error) {
	for {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			lock, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			if createErr != nil {
				return nil, fmt.Errorf("create config lock: %w", createErr)
			}
			// OpenFile's creation mode is filtered by umask. Repair it explicitly
			// so a restrictive umask cannot create a persistent unreadable lock.
			if chmodErr := lock.Chmod(0o600); chmodErr != nil {
				_ = lock.Close()
				_ = os.Remove(path)
				return nil, fmt.Errorf("secure config lock: %w", chmodErr)
			}
			return lock, nil
		}
		if err != nil {
			return nil, fmt.Errorf("inspect config lock: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("config lock %q must be a regular file, not a symlink", path)
		}
		if err := PermissionError(path, info.Mode()); err != nil {
			return nil, err
		}
		if err := validateOwnerAndLinks(path, info, false); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("open config lock: %w", err)
		}
		openedInfo, statErr := lock.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			_ = lock.Close()
			if statErr != nil {
				return nil, fmt.Errorf("inspect opened config lock: %w", statErr)
			}
			continue
		}
		return lock, nil
	}
}
