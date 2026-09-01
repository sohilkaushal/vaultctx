//go:build darwin || linux

package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDoctorRejectsNonRegularTLSFile(t *testing.T) {
	testApp := newTestApplication(t)
	dir := filepath.Dir(filepath.Dir(testApp.app.Store.Path))
	fifo := filepath.Join(dir, "not-a-certificate.pem")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200", "--ca-cert", fifo)
	testApp.app.LookPath = func(string) (string, error) { return "/bin/true", nil }
	if code := testApp.execute(t, "doctor", "prod"); code == 0 {
		t.Fatal("doctor accepted a FIFO as TLS certificate material")
	}
	if !strings.Contains(testApp.out.String(), "must resolve to a regular file") {
		t.Fatalf("doctor FIFO output = %s", testApp.out.String())
	}
}
