//go:build darwin || linux

package config

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPersistentLockRepairsRestrictiveUmask(t *testing.T) {
	dir := t.TempDir()
	secureTestDirectory(t, dir)
	path := filepath.Join(dir, "config.json")

	oldUmask := syscall.Umask(0o777)
	t.Cleanup(func() { syscall.Umask(oldUmask) })
	store := NewStore(path)
	if err := store.Update(func(file *File) error {
		file.Contexts["one"] = Context{Address: "https://one.example"}
		return nil
	}); err != nil {
		t.Fatalf("first Update() under restrictive umask: %v", err)
	}
	syscall.Umask(oldUmask)

	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("persistent lock mode = %04o, want 0600", info.Mode().Perm())
	}
	if err := store.Update(func(file *File) error {
		file.Contexts["two"] = Context{Address: "https://two.example"}
		return nil
	}); err != nil {
		t.Fatalf("second Update() after restrictive umask: %v", err)
	}
}
