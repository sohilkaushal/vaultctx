//go:build !darwin && !linux

package main

import "os"

// Unsupported platforms fail closed to the non-interactive picker path.
func isTerminal(*os.File) bool { return false }
