//go:build darwin || linux

package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	execHelperMode  = "VAULTCTX_EXEC_HELPER_MODE"
	execHelperReady = "VAULTCTX_EXEC_HELPER_READY"
)

func TestExecPreservesNumericAndSignalExitCodes(t *testing.T) {
	testCases := []struct {
		name string
		mode string
		want int
	}{
		{name: "numeric exit", mode: "exit:23", want: 23},
		{name: "independent SIGTERM", mode: "signal:15", want: 128 + int(syscall.SIGTERM)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testApp := newExecHelperApplication(t, testCase.mode, "")
			code := testApp.execute(t, execHelperCommand()...)
			if code != testCase.want {
				t.Fatalf("exec code = %d, want %d; stderr=%s", code, testCase.want, testApp.errOut.String())
			}
		})
	}
}

func TestCommandUsesTerminalStdinRejectsNonTTYFiles(t *testing.T) {
	null, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if commandUsesTerminalStdin(&exec.Cmd{Stdin: null}) {
		t.Fatal("/dev/null was classified as a terminal")
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	if commandUsesTerminalStdin(&exec.Cmd{Stdin: reader}) {
		t.Fatal("pipe was classified as a terminal")
	}
	if commandUsesTerminalStdin(&exec.Cmd{Stdin: strings.NewReader("")}) {
		t.Fatal("non-file reader was classified as a terminal")
	}
}

func TestExecObserverFailureTerminatesChild(t *testing.T) {
	originalObserver := observeProcessExit
	observeProcessExit = func(int) (<-chan error, func(), error) {
		result := make(chan error, 1)
		result <- errors.New("injected observer failure")
		return result, func() {}, nil
	}
	defer func() { observeProcessExit = originalObserver }()

	ready := filepath.Join(t.TempDir(), "ready")
	testApp := newExecHelperApplication(t, "linger", ready)
	done := make(chan int, 1)
	go func() {
		done <- testApp.app.Execute(context.Background(), execHelperCommand())
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("observer failure code = %d, want 1", code)
		}
		if !strings.Contains(testApp.errOut.String(), "injected observer failure") {
			t.Fatalf("observer failure stderr = %q", testApp.errOut.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observer failure left child running")
	}
}

func TestExecObserverFailureCleansCanceledDescendants(t *testing.T) {
	for _, registrationFailure := range []bool{true, false} {
		name := "retrieval"
		if registrationFailure {
			name = "registration"
		}
		t.Run(name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "parent-ready")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			originalObserver := observeProcessExit
			observeProcessExit = func(int) (<-chan error, func(), error) {
				if !waitForPath(ready, 2*time.Second) {
					return nil, nil, errors.New("helper did not become ready before observer failure")
				}
				cancel()
				injected := errors.New("injected observer failure with cancellation")
				if registrationFailure {
					return nil, nil, injected
				}
				result := make(chan error, 1)
				result <- injected
				return result, func() {}, nil
			}
			t.Cleanup(func() { observeProcessExit = originalObserver })

			inspectionCalls := 0
			originalInspection := inspectProcessGroup
			inspectProcessGroup = func(processGroupID int) (bool, error) {
				inspectionCalls++
				return originalInspection(processGroupID)
			}
			t.Cleanup(func() { inspectProcessGroup = originalInspection })
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

			testApp := newExecHelperApplication(t, "spawn-descendant", ready)
			done := make(chan int, 1)
			go func() {
				done <- testApp.app.Execute(ctx, execHelperCommand())
			}()
			select {
			case code := <-done:
				if code != 1 {
					t.Fatalf("observer failure exit code = %d, want 1; stderr=%s", code, testApp.errOut.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("observer failure with cancellation did not return")
			}
			if inspectionCalls == 0 {
				t.Fatal("observer failure cleanup did not inspect process-group quiescence")
			}
			if !strings.Contains(testApp.errOut.String(), "injected observer failure with cancellation") {
				t.Fatalf("observer failure was masked: %s", testApp.errOut.String())
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
				t.Fatalf("check process group %d after observer failure: %v", processGroupID, err)
			}
			if active {
				t.Fatalf("process group %d still has runnable descendants after observer failure", processGroupID)
			}

			descendantReady := ready + ".descendant"
			before, err := os.ReadFile(descendantReady)
			if err != nil {
				t.Fatalf("read descendant heartbeat: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			after, err := os.ReadFile(descendantReady)
			if err != nil {
				t.Fatalf("read descendant heartbeat after observer failure: %v", err)
			}
			if string(before) != string(after) {
				t.Fatalf("descendant process %d remained running after observer failure (heartbeat %q -> %q)", descendantPID, before, after)
			}
		})
	}
}

func TestExecObserverFailureSurfacesCleanupFailure(t *testing.T) {
	testCases := []struct {
		name      string
		detail    string
		configure func(t *testing.T)
	}{
		{
			name:   "group signal",
			detail: "input/output error",
			configure: func(t *testing.T) {
				originalSignal := signalProcess
				signalProcess = func(pid int, signal syscall.Signal) error {
					err := originalSignal(pid, signal)
					if pid < 0 && signal == syscall.SIGKILL {
						return syscall.EIO
					}
					return err
				}
				t.Cleanup(func() { signalProcess = originalSignal })
			},
		},
		{
			name:   "group inspection",
			detail: "injected process-group inspection failure",
			configure: func(t *testing.T) {
				originalInspection := inspectProcessGroup
				inspectProcessGroup = func(int) (bool, error) {
					return false, errors.New("injected process-group inspection failure")
				}
				t.Cleanup(func() { inspectProcessGroup = originalInspection })
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			originalObserver := observeProcessExit
			observeProcessExit = func(int) (<-chan error, func(), error) {
				if !waitForPath(ready, 2*time.Second) {
					return nil, nil, errors.New("helper did not become ready before observer failure")
				}
				return nil, nil, errors.New("injected observer failure during incomplete cleanup")
			}
			t.Cleanup(func() { observeProcessExit = originalObserver })
			testCase.configure(t)

			testApp := newExecHelperApplication(t, "linger", ready)
			done := make(chan int, 1)
			go func() {
				done <- testApp.app.Execute(context.Background(), execHelperCommand())
			}()
			select {
			case code := <-done:
				if code != 1 {
					t.Fatalf("cleanup failure exit code = %d, want 1", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("observer cleanup failure did not return")
			}
			stderr := testApp.errOut.String()
			for _, fragment := range []string{errProcessCleanupIncomplete.Error(), "injected observer failure during incomplete cleanup", testCase.detail} {
				if !strings.Contains(stderr, fragment) {
					t.Fatalf("cleanup failure stderr is missing %q: %s", fragment, stderr)
				}
			}
		})
	}
}

func TestExecGroupSignalEPERMRequiresQuiescence(t *testing.T) {
	for _, observerFailure := range []bool{false, true} {
		for _, state := range []string{"quiescent", "live descendant", "probe failure", "permission probe"} {
			t.Run(fmt.Sprintf("observer_failure=%t/%s", observerFailure, state), func(t *testing.T) {
				ready := filepath.Join(t.TempDir(), "ready")
				mode := "linger"
				if state == "live descendant" {
					mode = "spawn-descendant"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				originalObserver := observeProcessExit
				observeProcessExit = func(pid int) (<-chan error, func(), error) {
					if !waitForPath(ready, 2*time.Second) {
						return nil, nil, errors.New("helper did not become ready")
					}
					cancel()
					if observerFailure {
						return nil, nil, errors.New("injected EPERM observer failure")
					}
					return originalObserver(pid)
				}
				t.Cleanup(func() { observeProcessExit = originalObserver })

				originalSignal := signalProcess
				signalProcess = func(pid int, sig syscall.Signal) error {
					// Refuse delivery entirely: unlike the old diagnostic test,
					// this leaves a real descendant alive after the leader dies.
					if pid < 0 {
						return syscall.EPERM
					}
					return originalSignal(pid, sig)
				}
				t.Cleanup(func() { signalProcess = originalSignal })
				originalInspection := inspectProcessGroup
				inspectProcessGroup = func(pid int) (bool, error) {
					switch state {
					case "probe failure":
						return false, syscall.EIO
					case "permission probe":
						// Both platform probes treat EPERM as possibly active.
						return true, nil
					default:
						return originalInspection(pid)
					}
				}
				t.Cleanup(func() { inspectProcessGroup = originalInspection })
				t.Cleanup(func() {
					if state != "live descendant" {
						return
					}
					data, _ := os.ReadFile(ready)
					fields := strings.Fields(string(data))
					if len(fields) == 2 {
						pid, _ := strconv.Atoi(fields[0])
						group, _ := strconv.Atoi(fields[1])
						if pid > 0 && group > 0 {
							if actual, err := syscall.Getpgid(pid); err == nil && actual == group {
								_ = syscall.Kill(pid, syscall.SIGKILL)
							}
						}
					}
				})

				testApp := newExecHelperApplication(t, mode, ready)
				done := make(chan int, 1)
				go func() { done <- testApp.app.Execute(ctx, execHelperCommand()) }()
				wantIncomplete := state != "quiescent" || runtime.GOOS != "darwin"
				wantCode := 130
				if observerFailure || wantIncomplete {
					wantCode = 1
				}
				select {
				case code := <-done:
					if code != wantCode {
						t.Fatalf("exit code = %d, want %d; stderr=%s", code, wantCode, testApp.errOut.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("failed group signal cleanup did not return")
				}
				stderr := testApp.errOut.String()
				if got := strings.Contains(stderr, errProcessCleanupIncomplete.Error()); got != wantIncomplete {
					t.Fatalf("incomplete cleanup = %t, want %t; stderr=%s", got, wantIncomplete, stderr)
				}
				if observerFailure && !strings.Contains(stderr, "injected EPERM observer failure") {
					t.Fatalf("observer diagnostic lost: %s", stderr)
				}
				if state == "live descendant" {
					before := waitForHelperFile(t, ready+".descendant")
					time.Sleep(50 * time.Millisecond)
					after := waitForHelperFile(t, ready+".descendant")
					if string(before) == string(after) {
						t.Fatal("refused group signal did not leave a live heartbeat descendant")
					}
				}
			})
		}
	}
}

func TestExecCancellationTerminatesChildProcessGroup(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	testApp := newExecHelperApplication(t, "linger", ready)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- testApp.app.Execute(ctx, execHelperCommand())
	}()

	waitForHelperFile(t, ready)
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("canceled exec code = %d, want 130; stderr=%s", code, testApp.errOut.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled exec did not return")
	}
}

func TestCleanupReportsRefusedGroupAndDirectSignals(t *testing.T) {
	for _, path := range []string{"group cancellation", "direct cancellation", "group observer failure", "direct observer failure", "group pending retrieval failure", "direct pending retrieval failure"} {
		t.Run(path, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			cmd := exec.Command(os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$")
			cmd.Env = helperEnvironment(os.Environ(), "linger", ready)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				// The bounded failure path retains the one eventual reaper.
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if errors.Is(syscall.Kill(cmd.Process.Pid, 0), syscall.ESRCH) {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				t.Error("background cleanup did not reap the released child")
			})
			waitForHelperFile(t, ready)

			originalSignal, originalKill := signalProcess, killDirectProcess
			signalProcess = func(int, syscall.Signal) error { return syscall.EPERM }
			killDirectProcess = func(*os.Process) error { return syscall.EPERM }
			t.Cleanup(func() { signalProcess, killDirectProcess = originalSignal, originalKill })
			observerFailure := strings.HasSuffix(path, "observer failure")
			pendingFailure := strings.HasSuffix(path, "pending retrieval failure")
			var exited <-chan error
			if pendingFailure {
				pending := make(chan error, 1)
				killDirectProcess = func(*os.Process) error {
					pending <- errors.New("injected double-refusal observer failure")
					return syscall.EPERM
				}
				exited = pending
			} else if !observerFailure {
				var closeObserver func()
				var err error
				exited, closeObserver, err = processExitNotification(cmd.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}
				defer closeObserver()
			}
			done := make(chan error, 1)
			go func() {
				switch path {
				case "group cancellation", "group pending retrieval failure":
					_, observerErr, cleanupErr := terminateProcessGroup(cmd, syscall.SIGTERM, exited)
					done <- errors.Join(cleanupErr, observerErr)
				case "direct cancellation", "direct pending retrieval failure":
					done <- terminateDirectProcess(cmd, syscall.SIGTERM, exited)
				default:
					done <- abortAfterObserverFailure(cmd, strings.HasPrefix(path, "group"), errors.New("injected double-refusal observer failure"))
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, errProcessCleanupIncomplete) || !strings.Contains(err.Error(), "direct child exit unconfirmed") {
					t.Fatalf("refused signal error = %v", err)
				}
				if (observerFailure || pendingFailure) && !strings.Contains(err.Error(), "injected double-refusal observer failure") {
					t.Fatalf("observer diagnostic lost: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("refused group and direct SIGKILL left cleanup blocked")
			}
			before := waitForHelperFile(t, ready)
			time.Sleep(50 * time.Millisecond)
			after := waitForHelperFile(t, ready)
			if string(before) == string(after) {
				t.Fatal("signal refusal did not leave the child alive")
			}
		})
	}
}

func TestExecCancellationPreservesSignalCause(t *testing.T) {
	testCases := []struct {
		name       string
		cause      error
		wantCode   int
		wantSignal syscall.Signal
	}{
		{name: "plain cancellation", cause: context.Canceled, wantCode: 130, wantSignal: syscall.SIGTERM},
		{name: "SIGINT", cause: CancellationInterrupt, wantCode: 130, wantSignal: syscall.SIGINT},
		{name: "SIGTERM", cause: CancellationTerminate, wantCode: 143, wantSignal: syscall.SIGTERM},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			testApp := newExecHelperApplication(t, "record-signal", ready)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			done := make(chan int, 1)
			go func() {
				done <- testApp.app.Execute(ctx, execHelperCommand())
			}()

			waitForHelperFile(t, ready)
			cancel(testCase.cause)
			select {
			case code := <-done:
				if code != testCase.wantCode {
					t.Fatalf("canceled exec code = %d, want %d; stderr=%s", code, testCase.wantCode, testApp.errOut.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("signal-caused cancellation did not return")
			}

			data, err := os.ReadFile(ready)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("signal:%d", testCase.wantSignal)
			if strings.TrimSpace(string(data)) != want {
				t.Fatalf("forwarded signal record = %q, want %q", data, want)
			}
		})
	}
}

func TestExecCancellationKillsSameGroupDescendants(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "parent-ready")
	testApp := newExecHelperApplication(t, "spawn-descendant", ready)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- testApp.app.Execute(ctx, execHelperCommand())
	}()

	data := waitForHelperFile(t, ready)
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		cancel()
		t.Fatalf("helper state = %q, want descendant PID and process group", data)
	}
	descendantPID, err := strconv.Atoi(fields[0])
	if err != nil {
		cancel()
		t.Fatalf("parse descendant PID: %v", err)
	}
	processGroupID, err := strconv.Atoi(fields[1])
	if err != nil {
		cancel()
		t.Fatalf("parse process group: %v", err)
	}
	if got, err := syscall.Getpgid(descendantPID); err != nil || got != processGroupID {
		cancel()
		t.Fatalf("descendant process group = %d, %v; want %d", got, err, processGroupID)
	}

	canceledAt := time.Now()
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("canceled exec code = %d, want 130; stderr=%s", code, testApp.errOut.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled exec with descendant did not return")
	}
	if elapsed := time.Since(canceledAt); elapsed < processTerminationGrace {
		t.Fatalf("SIGTERM-ignoring process group returned after %v, before %v grace period", elapsed, processTerminationGrace)
	}
	active, err := processGroupActive(processGroupID)
	if err != nil {
		t.Fatalf("check process group %d after vaultctx returned: %v", processGroupID, err)
	}
	if active {
		t.Fatalf("process group %d still has runnable members after vaultctx returned", processGroupID)
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
		t.Fatalf("descendant process %d remained running after vaultctx returned (heartbeat %q -> %q)", descendantPID, before, after)
	}
}

func newExecHelperApplication(t *testing.T, mode, ready string) *testApplication {
	t.Helper()
	testApp := newTestApplication(t)
	testApp.mustExecute(t, "add", "prod", "--address", "https://vault.example:8200")
	testApp.env[execHelperMode] = mode
	if ready != "" {
		testApp.env[execHelperReady] = ready
	}
	return testApp
}

func execHelperCommand() []string {
	return []string{"exec", "prod", "--", os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$"}
}

func waitForHelperFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read helper state: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not create %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVaultctxExecProcessHelper(t *testing.T) {
	mode := os.Getenv(execHelperMode)
	if mode == "" {
		return
	}

	switch {
	case strings.HasPrefix(mode, "exit:"):
		code, err := strconv.Atoi(strings.TrimPrefix(mode, "exit:"))
		if err != nil {
			os.Exit(125)
		}
		os.Exit(code)
	case strings.HasPrefix(mode, "signal:"):
		number, err := strconv.Atoi(strings.TrimPrefix(mode, "signal:"))
		if err != nil {
			os.Exit(125)
		}
		_ = syscall.Kill(os.Getpid(), syscall.Signal(number))
		for {
			time.Sleep(time.Hour)
		}
	case mode == "linger":
		signal.Ignore(syscall.SIGTERM)
		ready := os.Getenv(execHelperReady)
		for heartbeat := uint64(1); ; heartbeat++ {
			if err := writeHelperFile(ready, []byte(strconv.FormatUint(heartbeat, 10))); err != nil {
				os.Exit(125)
			}
			time.Sleep(10 * time.Millisecond)
		}
	case mode == "record-signal":
		received := make(chan os.Signal, 1)
		signal.Notify(received, syscall.SIGINT, syscall.SIGTERM)
		ready := os.Getenv(execHelperReady)
		if err := writeHelperFile(ready, []byte("ready")); err != nil {
			os.Exit(125)
		}
		forwarded := (<-received).(syscall.Signal)
		if err := writeHelperFile(ready, []byte(fmt.Sprintf("signal:%d", forwarded))); err != nil {
			os.Exit(125)
		}
		os.Exit(0)
	case mode == "interactive-wrapper":
		child := exec.Command(os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$")
		child.Env = helperEnvironment(os.Environ(), "read-line", "")
		child.Stdin = os.Stdin
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if !commandUsesTerminalStdin(child) {
			os.Exit(125)
		}
		if err := runManagedCommand(context.Background(), child); err != nil {
			os.Exit(125)
		}
		os.Exit(0)
	case mode == "read-line":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "vaultctx-pty-line" {
			os.Exit(125)
		}
		fmt.Fprintln(os.Stdout, "vaultctx-pty-ok")
		os.Exit(0)
	case mode == "spawn-descendant" || strings.HasPrefix(mode, "spawn-descendant-exit:"):
		ready := os.Getenv(execHelperReady)
		descendantReady := ready + ".descendant"
		descendant := exec.Command(os.Args[0], "-test.run=^TestVaultctxExecProcessHelper$")
		descendant.Env = helperEnvironment(os.Environ(), "linger", descendantReady)
		if err := descendant.Start(); err != nil {
			os.Exit(125)
		}
		if !waitForPath(descendantReady, 2*time.Second) {
			_ = descendant.Process.Kill()
			os.Exit(125)
		}
		state := fmt.Sprintf("%d %d\n", descendant.Process.Pid, syscall.Getpgrp())
		if err := writeHelperFile(ready, []byte(state)); err != nil {
			_ = descendant.Process.Kill()
			os.Exit(125)
		}
		if strings.HasPrefix(mode, "spawn-descendant-exit:") {
			code, err := strconv.Atoi(strings.TrimPrefix(mode, "spawn-descendant-exit:"))
			if err != nil {
				_ = descendant.Process.Kill()
				os.Exit(125)
			}
			os.Exit(code)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(125)
	}
}

func helperEnvironment(environment []string, mode, ready string) []string {
	result := setEnvironment(environment, execHelperMode, mode)
	return setEnvironment(result, execHelperReady, ready)
}

func writeHelperFile(path string, data []byte) error {
	temporary := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func waitForPath(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
