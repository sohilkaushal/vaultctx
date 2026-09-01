//go:build darwin

package app

import (
	"fmt"
	"syscall"
)

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
	if _, err := syscall.Kevent(queue, []syscall.Kevent_t{change}, nil, nil); err != nil {
		_ = syscall.Close(queue)
		return nil, nil, fmt.Errorf("monitor child process: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		events := make([]syscall.Kevent_t, 1)
		for {
			if _, err := syscall.Kevent(queue, nil, events, nil); err == syscall.EINTR {
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
