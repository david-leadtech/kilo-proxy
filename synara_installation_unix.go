//go:build !windows

package main

import (
	"os"
	"syscall"
)

// A path swapped for a pipe between stat and open must not stall discovery.
// The caller verifies the descriptor is still the original regular file.
func synaraOpenCandidate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
