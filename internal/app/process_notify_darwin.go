//go:build darwin

package app

import (
	"errors"
	"fmt"
	"syscall"
)

var processKevent = syscall.Kevent

func processExitNotification(pid int) (<-chan error, func(), error) {
	queue, err := syscall.Kqueue()
	if err != nil {
		return nil, nil, fmt.Errorf("monitor child process: %w", err)
	}
	change := syscall.Kevent_t{
		Ident:  uint64(pid),
		Filter: syscall.EVFILT_PROC,
		Flags:  syscall.EV_ADD | syscall.EV_ONESHOT,
		Fflags: syscall.NOTE_EXIT,
	}
	if _, err := processKevent(queue, []syscall.Kevent_t{change}, nil, nil); err != nil {
		_ = syscall.Close(queue)
		if errors.Is(err, syscall.ESRCH) {
			// A fast child can exit between Start and EV_ADD. It is still our
			// unreaped child, so its PID cannot have been reused; let the caller
			// use Wait to recover the real exit status.
			done := make(chan error, 1)
			done <- nil
			return done, func() {}, nil
		}
		return nil, nil, fmt.Errorf("monitor child process: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		events := make([]syscall.Kevent_t, 1)
		for {
			if _, err := processKevent(queue, nil, events, nil); err == syscall.EINTR {
				continue
			} else if err != nil {
				done <- fmt.Errorf("monitor child process: %w", err)
				return
			}
			done <- nil
			return
		}
	}()
	return done, func() { _ = syscall.Close(queue) }, nil
}
