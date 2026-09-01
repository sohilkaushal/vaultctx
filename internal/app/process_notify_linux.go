//go:build linux

package app

import (
	"fmt"
	"syscall"
	"unsafe"
)

func processExitNotification(pid int) (<-chan error, func(), error) {
	done := make(chan error, 1)
	go func() {
		var info [16]uint64 // Linux siginfo_t is 128 bytes and naturally aligned.
		for {
			_, _, errno := syscall.Syscall6(
				syscall.SYS_WAITID,
				1, // P_PID
				uintptr(pid),
				uintptr(unsafe.Pointer(&info[0])),
				uintptr(syscall.WEXITED|syscall.WNOWAIT),
				0,
				0,
			)
			if errno == syscall.EINTR {
				continue
			}
			if errno != 0 {
				done <- fmt.Errorf("monitor child process: %w", errno)
				return
			}
			done <- nil
			return
		}
	}()
	return done, func() {}, nil
}
