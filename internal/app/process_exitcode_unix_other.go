//go:build aix || dragonfly || freebsd || illumos || netbsd || openbsd || solaris

package app

import (
	"os/exec"
	"syscall"
)

func managedExitCode(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if status.Signaled() {
			return 128 + int(status.Signal())
		}
		if status.Exited() {
			return status.ExitStatus()
		}
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
