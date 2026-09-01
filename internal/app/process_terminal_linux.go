//go:build linux

package app

import (
	"os"
	"syscall"
	"unsafe"
)

func terminalFile(file *os.File) bool {
	var attributes syscall.Termios
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		file.Fd(),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&attributes)),
		0,
		0,
		0,
	)
	return errno == 0
}
