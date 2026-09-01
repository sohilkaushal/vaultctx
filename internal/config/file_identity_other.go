//go:build !darwin && !linux

package config

import "os"

func validateOwnerAndLinks(_ string, _ os.FileInfo, _ bool) error { return nil }
