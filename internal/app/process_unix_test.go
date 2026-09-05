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
