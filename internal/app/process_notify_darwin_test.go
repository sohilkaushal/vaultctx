//go:build darwin

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func installRegistrationRaceKevent(t *testing.T, afterExit func()) {
	t.Helper()
	originalKevent := processKevent
	processKevent = func(queue int, changes, events []syscall.Kevent_t, timeout *syscall.Timespec) (int, error) {
		if len(changes) != 1 || events != nil {
			return originalKevent(queue, changes, events, timeout)
		}
		if _, err := originalKevent(queue, changes, nil, timeout); err != nil {
			if errors.Is(err, syscall.ESRCH) && afterExit != nil {
				afterExit()
			}
			return 0, err
		}
		observed := make([]syscall.Kevent_t, 1)
		wait := syscall.NsecToTimespec(int64(3 * time.Second))
		for {
			count, err := originalKevent(queue, nil, observed, &wait)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				return count, err
			}
			if count == 0 {
				return 0, syscall.ETIMEDOUT
			}
			break
		}
		if afterExit != nil {
			afterExit()
		}
		return 0, syscall.ESRCH
	}
	t.Cleanup(func() { processKevent = originalKevent })
}

func TestProcessExitNotificationAcceptsChildExitedBeforeRegistration(t *testing.T) {
	originalKevent := processKevent
	processKevent = func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
		if len(changes) != 1 || events != nil {
			t.Fatalf("registration call has %d changes and %d events", len(changes), len(events))
		}
		return 0, syscall.ESRCH
	}
	defer func() { processKevent = originalKevent }()

	exited, closeObserver, err := processExitNotification(123)
	if err != nil {
		t.Fatalf("processExitNotification() error = %v, want completed child notification", err)
	}
	defer closeObserver()

	select {
	case observerErr := <-exited:
		if observerErr != nil {
			t.Fatalf("completed child notification error = %v", observerErr)
		}
	case <-time.After(time.Second):
		t.Fatal("completed child notification was not ready")
	}
}

func TestProcessExitNotificationRejectsOtherRegistrationFailures(t *testing.T) {
	originalKevent := processKevent
	processKevent = func(_ int, _ []syscall.Kevent_t, _ []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
		return 0, syscall.EPERM
	}
	defer func() { processKevent = originalKevent }()

	exited, closeObserver, err := processExitNotification(123)
	if !errors.Is(err, syscall.EPERM) {
		t.Fatalf("processExitNotification() error = %v, want permission error", err)
	}
	if exited != nil || closeObserver != nil {
		t.Fatal("failed registration returned a usable observer")
	}
}

func TestProcessExitNotificationRejectsRetrievalNoSuchProcess(t *testing.T) {
	originalKevent := processKevent
	processKevent = func(_ int, changes, events []syscall.Kevent_t, _ *syscall.Timespec) (int, error) {
		switch {
		case len(changes) == 1 && events == nil:
			return 0, nil
		case changes == nil && len(events) == 1:
			return 0, syscall.ESRCH
		default:
			return 0, syscall.EINVAL
		}
	}
	defer func() { processKevent = originalKevent }()

	exited, closeObserver, err := processExitNotification(123)
	if err != nil {
		t.Fatalf("processExitNotification() error = %v", err)
	}
	defer closeObserver()

	select {
	case observerErr := <-exited:
		if !errors.Is(observerErr, syscall.ESRCH) {
			t.Fatalf("retrieval error = %v, want no-such-process error", observerErr)
		}
	case <-time.After(time.Second):
		t.Fatal("retrieval failure was not reported")
	}
}

func TestManagedCommandHandlesFastDarwinChildren(t *testing.T) {
	path, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 100; iteration++ {
		if err := runManagedCommand(context.Background(), exec.Command(path)); err != nil {
			t.Fatalf("fast child %d: %v", iteration, err)
		}
	}
}

func TestManagedCommandPreservesExitStatusAfterDarwinRegistrationRace(t *testing.T) {
	installRegistrationRaceKevent(t, nil)

	command := exec.Command(os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$")
	command.Env = helperEnvironment(os.Environ(), "exit:37", "")
	err := runManagedCommand(context.Background(), command)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runManagedCommand() error = %v, want exit status 37", err)
	}
	if got := managedExitCode(exitErr); got != 37 {
		t.Fatalf("runManagedCommand() exit status = %d, want 37", got)
	}
}

func TestManagedCommandCleansDescendantWhenDarwinRegistrationRaceIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	installRegistrationRaceKevent(t, cancel)

	ready := filepath.Join(t.TempDir(), "parent-ready")
	t.Cleanup(func() {
		data, err := os.ReadFile(ready)
		if err != nil {
			return
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			return
		}
		descendantPID, pidErr := strconv.Atoi(fields[0])
		processGroupID, groupErr := strconv.Atoi(fields[1])
		if pidErr != nil || groupErr != nil {
			return
		}
		if actualGroup, err := syscall.Getpgid(descendantPID); err == nil && actualGroup == processGroupID {
			_ = syscall.Kill(descendantPID, syscall.SIGKILL)
		}
	})

	testApp := newExecHelperApplication(t, "spawn-descendant-exit:37", ready)
	done := make(chan int, 1)
	go func() {
		done <- testApp.app.Execute(ctx, execHelperCommand())
	}()
	select {
	case code := <-done:
		if code != 37 {
			t.Fatalf("canceled registration race exit code = %d, want preserved child status 37; stderr=%s", code, testApp.errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled registration race did not return")
	}

	fields := strings.Fields(string(waitForHelperFile(t, ready)))
	if len(fields) != 2 {
		t.Fatalf("helper state has %d fields, want descendant PID and process group", len(fields))
	}
	descendantPID, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse descendant PID: %v", err)
	}
	processGroupID, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse process group: %v", err)
	}

	active, err := processGroupActive(processGroupID)
	if err != nil {
		t.Fatalf("check process group %d after cancellation: %v", processGroupID, err)
	}
	if active {
		t.Fatalf("process group %d still has runnable descendants after canceled registration race", processGroupID)
	}

	descendantReady := ready + ".descendant"
	before, err := os.ReadFile(descendantReady)
	if err != nil {
		t.Fatalf("read descendant heartbeat: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(descendantReady)
	if err != nil {
		t.Fatalf("read descendant heartbeat after cancellation: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("descendant process %d remained running after cancellation (heartbeat %q -> %q)", descendantPID, before, after)
	}
}
