//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// processGroupActive reports whether a process group still contains a process
// that the kernel could schedule. Linux retains killed descendants as zombies
// until their new parent reaps them; kill(-pgid, 0) reports such a zombie-only
// group as existing even though none of its members can execute.
func processGroupActive(processGroupID int) (bool, error) {
	return processGroupActiveAfterProbe(processGroupID, syscall.Kill(-processGroupID, 0))
}

func processGroupActiveAfterProbe(processGroupID int, probeErr error) (bool, error) {
	if errors.Is(probeErr, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(probeErr, syscall.EPERM) {
		// The kernel confirmed that the group exists, but this process cannot
		// signal it. Do not let a restricted or filtered /proc view override
		// that result: it may hide a runnable same-group descendant.
		return true, nil
	}
	if probeErr != nil {
		return false, probeErr
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("read /proc: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			// A process can disappear during the scan. Any other read failure
			// prevents a fail-closed determination that the group is quiescent.
			return false, fmt.Errorf("read process %s state: %w", entry.Name(), err)
		}
		group, state, ok := linuxProcessGroupState(data)
		if ok && group == processGroupID && state != 'Z' && state != 'X' {
			return true, nil
		}
	}
	return false, nil
}

func linuxProcessGroupState(stat []byte) (int, byte, bool) {
	// The command name is parenthesized and may itself contain spaces or ')',
	// so split after the final closing parenthesis. The following fields are
	// state, parent PID, and process-group ID.
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+2 >= len(stat) {
		return 0, 0, false
	}
	fields := strings.Fields(string(stat[closing+1:]))
	if len(fields) < 3 || len(fields[0]) != 1 {
		return 0, 0, false
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	return group, fields[0][0], true
}
