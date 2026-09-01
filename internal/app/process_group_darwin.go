//go:build darwin

package app

import (
	"errors"
	"syscall"
)

func processGroupActive(processGroupID int) (bool, error) {
	err := syscall.Kill(-processGroupID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false, err
	}
	return true, nil
}
