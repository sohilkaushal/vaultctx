//go:build darwin

package app

import (
	"os"
	"syscall"
	"unsafe"
)

func terminalFile(file *os.File) bool {
	var attributes syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		file.Fd(),
		uintptr(syscall.TIOCGETA),
		uintptr(unsafe.Pointer(&attributes)),
	)
	return errno == 0
}
