//go:build linux

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDirectCancellationWaitsForPendingLinuxObserver(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$")
	cmd.Env = helperEnvironment(os.Environ(), "linger", ready)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	waitForHelperFile(t, ready)

	killed := make(chan struct{})
	originalKill := killDirectProcess
	killDirectProcess = func(process *os.Process) error {
		err := originalKill(process)
		close(killed)
		return err
	}
	t.Cleanup(func() { killDirectProcess = originalKill })
	exited := make(chan error, 1)
	go func() {
		<-killed
		// Force observer execution after the grace timer and SIGKILL. A
		// premature Wait will have reaped the child before waitid can see it.
		time.Sleep(50 * time.Millisecond)
		pending, closeObserver, err := processExitNotification(cmd.Process.Pid)
		if err != nil {
			exited <- err
			return
		}
		defer closeObserver()
		exited <- <-pending
	}()
	if err := terminateDirectProcess(cmd, syscall.SIGTERM, exited); err != nil {
		t.Fatalf("direct cancellation lost its pending observer: %v", err)
	}
}
