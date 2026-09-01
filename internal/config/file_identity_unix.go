//go:build darwin || linux

package config

import (
	"fmt"
	"os"
	"syscall"
)

func validateOwnerAndLinks(path string, info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect ownership for %q: unsupported file metadata", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%q is owned by uid %d, expected uid %d", path, stat.Uid, os.Geteuid())
	}
	if !directory && stat.Nlink != 1 {
		return fmt.Errorf("config %q has %d hard links; expected exactly one", path, stat.Nlink)
	}
	return nil
}
