package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/sohilkaushal/vaultctx/internal/app"
	"github.com/sohilkaushal/vaultctx/internal/config"
	"github.com/sohilkaushal/vaultctx/internal/selector"
)

var version = "dev"

func main() {
	path, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vaultctx: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case received := <-signals:
			// Restore default handling after the first signal so a second signal
			// can still force immediate termination during graceful child cleanup.
			signal.Stop(signals)
			if received == syscall.SIGTERM {
				cancel(app.CancellationTerminate)
			} else {
				cancel(app.CancellationInterrupt)
			}
		case <-ctx.Done():
		}
	}()
	application := &app.App{
		In:       os.Stdin,
		Out:      os.Stdout,
		Err:      os.Stderr,
		Store:    config.NewStore(path),
		Picker:   selector.New(os.Stdin, os.Stdout, os.Stderr, isTerminal(os.Stdin)),
		Getenv:   os.Getenv,
		Environ:  os.Environ,
		Command:  exec.CommandContext,
		HomeDir:  os.UserHomeDir,
		LookPath: exec.LookPath,
		Version:  version,
	}
	exitCode := application.Execute(ctx, os.Args[1:])
	// os.Exit does not run deferred functions, so explicitly release signal
	// notification resources before terminating the process.
	signal.Stop(signals)
	cancel(context.Canceled)
	os.Exit(exitCode)
}
