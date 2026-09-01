package app

import (
	"context"
	"errors"
)

// CancellationSignal is a context cancellation cause used by the executable
// entry point to retain whether the wrapper received SIGINT or SIGTERM.
type CancellationSignal uint8

const (
	CancellationInterrupt CancellationSignal = iota + 1
	CancellationTerminate
)

func (signal CancellationSignal) Error() string {
	switch signal {
	case CancellationInterrupt:
		return "received SIGINT"
	case CancellationTerminate:
		return "received SIGTERM"
	default:
		return "received termination signal"
	}
}

func cancellationSignal(ctx context.Context) CancellationSignal {
	var signal CancellationSignal
	if errors.As(context.Cause(ctx), &signal) {
		return signal
	}
	return 0
}

func cancellationExitCode(ctx context.Context) int {
	if cancellationSignal(ctx) == CancellationTerminate {
		return 143
	}
	// Plain context cancellation and SIGINT both use the conventional 130.
	return 130
}
