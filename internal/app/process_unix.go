//go:build darwin || linux

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

const (
	processTerminationGrace          = 250 * time.Millisecond
	processGroupQuiescenceTimeout    = time.Second
	processGroupQuiescencePollPeriod = 2 * time.Millisecond
)

type exitObserverFunc func(int) (<-chan error, func(), error)

var (
	observeProcessExit  exitObserverFunc = processExitNotification
	signalProcess                        = syscall.Kill
	inspectProcessGroup                  = processGroupActive
)

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
		return abortAfterObserverFailure(cmd, dedicatedProcessGroup, err)
	}
	defer closeObserver()

	select {
	case observerErr := <-exited:
		if observerErr != nil {
			return abortAfterObserverFailure(cmd, dedicatedProcessGroup, observerErr)
		}
		return finishObservedCommand(ctx, cmd, dedicatedProcessGroup)
	case <-ctx.Done():
		// If the process independently completed at the same time as cancellation,
		// preserve its real result, but still clean up any same-group descendants.
		select {
		case observerErr := <-exited:
			if observerErr != nil {
				return abortAfterObserverFailure(cmd, dedicatedProcessGroup, observerErr)
			}
			return finishObservedCommand(ctx, cmd, dedicatedProcessGroup)
		default:
		}
		gracefulSignal := cancellationProcessSignal(ctx)
		if dedicatedProcessGroup {
			_, observerErr, cleanupErr := terminateProcessGroup(cmd, gracefulSignal, exited)
			if cleanupErr != nil {
				return errors.Join(cleanupErr, observerErr)
			}
			if observerErr != nil {
				return observerErr
			}
		} else {
			if observerErr := terminateDirectProcess(cmd, gracefulSignal, exited); observerErr != nil {
				return observerErr
			}
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
	waitErr, _, cleanupErr := terminateProcessGroup(cmd, cancellationProcessSignal(ctx), nil)
	if cleanupErr != nil {
		return cleanupErr
	}
	return waitErr
}

func abortAfterObserverFailure(cmd *exec.Cmd, dedicatedProcessGroup bool, observerErr error) error {
	cleanupErr := abortUnmonitoredCommand(cmd, dedicatedProcessGroup)
	if cleanupErr != nil {
		return errors.Join(cleanupErr, observerErr)
	}
	return observerErr
}

func abortUnmonitoredCommand(cmd *exec.Cmd, dedicatedProcessGroup bool) error {
	if !dedicatedProcessGroup {
		signalErr := unexpectedSignalError("kill direct child", cmd.Process.Kill())
		waitErr := unexpectedWaitError(cmd.Wait())
		return directProcessCleanupError(signalErr, waitErr)
	}

	processGroupID := cmd.Process.Pid
	groupSignalErr := unexpectedSignalError("send SIGKILL to process group", signalProcess(-processGroupID, syscall.SIGKILL))
	var directSignalErr error
	if groupSignalErr != nil {
		// A failed group signal must not prevent reaping the direct child while
		// the caller records that descendant cleanup could not be guaranteed.
		directSignalErr = unexpectedSignalError("kill direct child after group signal failure", cmd.Process.Kill())
	}
	waitErr := unexpectedWaitError(cmd.Wait())
	quiescenceErr := confirmProcessGroupQuiescence(processGroupID)
	return finalProcessGroupCleanupError(processGroupID, groupSignalErr, directSignalErr, waitErr, quiescenceErr)
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

func terminateProcessGroup(cmd *exec.Cmd, gracefulSignal syscall.Signal, pendingExit <-chan error) (waitErr, observerErr, cleanupErr error) {
	processGroupID := cmd.Process.Pid
	_ = signalProcess(-processGroupID, gracefulSignal)

	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	<-timer.C
	// The direct child has intentionally not been reaped, so its PID cannot be
	// reused as an unrelated group ID before this one final group signal.
	groupSignalErr := unexpectedSignalError("send SIGKILL to process group", signalProcess(-processGroupID, syscall.SIGKILL))
	var directSignalErr error
	if groupSignalErr != nil {
		directSignalErr = unexpectedSignalError("kill direct child after group signal failure", cmd.Process.Kill())
	}
	if pendingExit != nil {
		// Linux observes exit with waitid(WNOWAIT). Consume that result before
		// Wait reaps the child, or the observer can spuriously report ECHILD.
		observerErr = <-pendingExit
	}
	waitErr = cmd.Wait()
	waitCleanupErr := unexpectedWaitError(waitErr)
	quiescenceErr := confirmProcessGroupQuiescence(processGroupID)
	cleanupErr = finalProcessGroupCleanupError(processGroupID, groupSignalErr, directSignalErr, waitCleanupErr, quiescenceErr)
	return waitErr, observerErr, cleanupErr
}

func finalProcessGroupCleanupError(processGroupID int, groupSignalErr, directSignalErr, waitErr, quiescenceErr error) error {
	// Darwin's group-signal path skips zombies and can return EPERM for an
	// unreaped, exited leader alone. EPERM is not itself proof of exit: accept
	// it only after successful reaping and independent group quiescence. Keep
	// all other signal errors, and never relax EPERM from the group probe.
	if runtime.GOOS == "darwin" && errors.Is(groupSignalErr, syscall.EPERM) && directSignalErr == nil && waitErr == nil && quiescenceErr == nil {
		groupSignalErr = nil
	}
	return processGroupCleanupError(processGroupID, groupSignalErr, directSignalErr, waitErr, quiescenceErr)
}

func confirmProcessGroupQuiescence(processGroupID int) error {
	// Reaping the direct child does not prove that the kernel has finished
	// stopping and reaping every same-group descendant. Poll group existence
	// after the final SIGKILL so cancellation cannot report completion while a
	// descendant still executes. No signal is sent after the leader is reaped,
	// avoiding harm if its numeric ID is rapidly reused by an unrelated group.
	deadline := time.Now().Add(processGroupQuiescenceTimeout)
	ticker := time.NewTicker(processGroupQuiescencePollPeriod)
	defer ticker.Stop()
	for {
		active, err := inspectProcessGroup(processGroupID)
		if err != nil {
			return fmt.Errorf("confirm process group termination: %w", err)
		}
		if !active {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("still has runnable members after %s", processGroupQuiescenceTimeout)
		}
		<-ticker.C
	}
}

func terminateDirectProcess(cmd *exec.Cmd, gracefulSignal syscall.Signal, exited <-chan error) (observerErr error) {
	_ = cmd.Process.Signal(gracefulSignal)
	timer := time.NewTimer(processTerminationGrace)
	defer timer.Stop()
	observed := false
	select {
	case observerErr = <-exited:
		observed = true
		if observerErr != nil {
			_ = cmd.Process.Kill()
		}
	case <-timer.C:
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if !observed {
		observerErr = <-exited
	}
	return observerErr
}

func unexpectedSignalError(operation string, err error) error {
	if err == nil || errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func unexpectedWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return fmt.Errorf("reap direct child: %w", err)
}

func processGroupCleanupError(processGroupID int, failures ...error) error {
	failure := errors.Join(failures...)
	if failure == nil {
		return nil
	}
	return fmt.Errorf("%w: process group %d: %v", errProcessCleanupIncomplete, processGroupID, failure)
}

func directProcessCleanupError(failures ...error) error {
	failure := errors.Join(failures...)
	if failure == nil {
		return nil
	}
	return fmt.Errorf("%w: direct child: %v", errProcessCleanupIncomplete, failure)
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
