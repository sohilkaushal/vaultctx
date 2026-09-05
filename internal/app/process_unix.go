//go:build darwin || linux

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	processTerminationGrace          = 250 * time.Millisecond
	processGroupQuiescenceTimeout    = time.Second
	processGroupQuiescencePollPeriod = 2 * time.Millisecond
)

type exitObserverFunc func(int) (<-chan error, func(), error)

var observeProcessExit exitObserverFunc = processExitNotification

// runManagedCommand gives a non-interactive child a dedicated process group.
// On cancellation, the whole group first receives the preserved SIGINT or
// SIGTERM and then, after a short grace period, SIGKILL. This cleans up ordinary
// same-group descendants without invoking a shell. An interactive child stays
// in the terminal foreground group, and a daemon that deliberately escapes its
// group is outside this portable policy.
func runManagedCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// A child in a separate background process group cannot read from the
	// controlling terminal: the kernel stops it with SIGTTIN. Keep interactive
	// commands in vaultctx's foreground group; non-interactive commands receive
	// the stronger whole-process-group cleanup policy.
	dedicatedProcessGroup := !commandUsesTerminalStdin(cmd)
	if dedicatedProcessGroup {
		attributes := syscall.SysProcAttr{}
		if cmd.SysProcAttr != nil {
			attributes = *cmd.SysProcAttr
		}
		attributes.Setpgid = true
		attributes.Pgid = 0
		cmd.SysProcAttr = &attributes
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	exited, closeObserver, err := observeProcessExit(cmd.Process.Pid)
	if err != nil {
		abortUnmonitoredCommand(cmd, dedicatedProcessGroup)
		return err
	}
	defer closeObserver()

	select {
	case observerErr := <-exited:
		if observerErr != nil {
			abortUnmonitoredCommand(cmd, dedicatedProcessGroup)
			return observerErr
		}
		return finishObservedCommand(ctx, cmd, dedicatedProcessGroup)
	case <-ctx.Done():
		// If the process independently completed at the same time as cancellation,
		// preserve its real result, but still clean up any same-group descendants.
		select {
		case observerErr := <-exited:
			if observerErr != nil {
				abortUnmonitoredCommand(cmd, dedicatedProcessGroup)
				return observerErr
			}
			return finishObservedCommand(ctx, cmd, dedicatedProcessGroup)
		default:
		}
		gracefulSignal := cancellationProcessSignal(ctx)
		if dedicatedProcessGroup {
			if _, err := terminateProcessGroup(cmd, gracefulSignal, exited); err != nil {
				return err
			}
		} else {
			terminateDirectProcess(cmd, gracefulSignal, exited)
		}
		return ctx.Err()
	}
}

func finishObservedCommand(ctx context.Context, cmd *exec.Cmd, dedicatedProcessGroup bool) error {
	if ctx.Err() == nil || !dedicatedProcessGroup {
		return cmd.Wait()
	}

	// The direct child is still unreaped, so its process-group ID cannot be
	// reused. Clean the group before Wait releases that protection, then retain
	// the independently completed child's real result.
	waitErr, cleanupErr := terminateProcessGroup(cmd, cancellationProcessSignal(ctx), nil)
	if cleanupErr != nil {
		return cleanupErr
	}
	return waitErr
}

func abortUnmonitoredCommand(cmd *exec.Cmd, dedicatedProcessGroup bool) {
	if dedicatedProcessGroup {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} else {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

func commandUsesTerminalStdin(cmd *exec.Cmd) bool {
	file, ok := cmd.Stdin.(*os.File)
	if !ok {
		return false
	}
	return terminalFile(file)
}

func cancellationProcessSignal(ctx context.Context) syscall.Signal {
	if cancellationSignal(ctx) == CancellationInterrupt {
		return syscall.SIGINT
	}
	return syscall.SIGTERM
}

func terminateProcessGroup(cmd *exec.Cmd, gracefulSignal syscall.Signal, pendingExit <-chan error) (waitErr, cleanupErr error) {
	processGroupID := cmd.Process.Pid
	_ = syscall.Kill(-processGroupID, gracefulSignal)

	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	<-timer.C
	// The direct child has intentionally not been reaped, so its PID cannot be
	// reused as an unrelated group ID before this one final group signal.
	_ = syscall.Kill(-processGroupID, syscall.SIGKILL)
	waitErr = cmd.Wait()
	if pendingExit != nil {
		<-pendingExit
	}

	// Reaping the direct child does not prove that the kernel has finished
	// stopping and reaping every same-group descendant. Poll group existence
	// after the final SIGKILL so cancellation cannot report completion while a
	// descendant still executes. No signal is sent after the leader is reaped,
	// avoiding harm if its numeric ID is rapidly reused by an unrelated group.
	deadline := time.Now().Add(processGroupQuiescenceTimeout)
	ticker := time.NewTicker(processGroupQuiescencePollPeriod)
	defer ticker.Stop()
	for {
		active, err := processGroupActive(processGroupID)
		if err != nil {
			return waitErr, fmt.Errorf("%w: confirm process group %d termination: %v", errProcessCleanupIncomplete, processGroupID, err)
		}
		if !active {
			return waitErr, nil
		}
		if !time.Now().Before(deadline) {
			return waitErr, fmt.Errorf("%w: process group %d still has runnable members after %s", errProcessCleanupIncomplete, processGroupID, processGroupQuiescenceTimeout)
		}
		<-ticker.C
	}
}

func terminateDirectProcess(cmd *exec.Cmd, gracefulSignal syscall.Signal, exited <-chan error) {
	_ = cmd.Process.Signal(gracefulSignal)
	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	observed := false
	select {
	case observerErr := <-exited:
		observed = true
		if observerErr != nil {
			_ = cmd.Process.Kill()
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if !observed {
		<-exited
	}
}

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
