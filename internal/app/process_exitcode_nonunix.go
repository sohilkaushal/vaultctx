//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package app

import "os/exec"

func managedExitCode(exitErr *exec.ExitError) int {
	if code := exitErr.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
