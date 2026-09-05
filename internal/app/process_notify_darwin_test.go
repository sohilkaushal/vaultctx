//go:build darwin

package app

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

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
