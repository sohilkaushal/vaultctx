//go:build linux

package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestInteractiveChildCanReadFromPTY(t *testing.T) {
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("/usr/bin/script is required for the PTY regression test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// util-linux script accepts the command as one argument and allocates the
	// controlling PTY used by the nested vaultctx test process. The production
	// exec path itself never invokes a shell.
	commandText := shellQuoteForPTYTest(os.Args[0]) + " -test.run=^TestVaultctxExecProcessHelper$"
	command := exec.CommandContext(ctx, "/usr/bin/script", "-q", "-e", "-c", commandText, "/dev/null")
	command.Env = helperEnvironment(os.Environ(), "interactive-wrapper", "")
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inputWriter.Close()
	command.Stdin = inputReader
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		_ = inputReader.Close()
		t.Fatal(err)
	}
	_ = inputReader.Close()
	time.Sleep(100 * time.Millisecond)
	if _, err := inputWriter.WriteString("vaultctx-pty-line\n"); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	if ctx.Err() != nil {
		t.Fatalf("interactive child hung while reading from PTY: %v; output=%q", ctx.Err(), output.String())
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			t.Fatalf("PTY wrapper exited %d: %s", exitErr.ExitCode(), output.String())
		}
		t.Fatalf("run PTY wrapper: %v", err)
	}
	if !strings.Contains(output.String(), "vaultctx-pty-ok") {
		t.Fatalf("PTY output = %q, want child read confirmation", output.String())
	}
}

func shellQuoteForPTYTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
