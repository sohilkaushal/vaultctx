package main

import (
	"os"
	"runtime"
	"testing"
)

func TestIsTerminalRejectsPipeAndNullDevice(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	defer writeEnd.Close()
	if isTerminal(readEnd) {
		t.Fatal("pipe was classified as an interactive terminal")
	}

	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Fatal("null character device was classified as an interactive terminal")
	}
}

func TestIsTerminalAcceptsControllingTTY(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("interactive terminal detection is supported on Darwin and Linux")
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skipf("test process has no controlling TTY: %v", err)
	}
	defer tty.Close()
	if !isTerminal(tty) {
		t.Fatal("controlling TTY was not classified as interactive")
	}
}
