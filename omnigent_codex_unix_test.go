//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOmnigentCodexSpecialFilesDoNotBlockSelectedResolution(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "codex")
	if err := syscall.Mkfifo(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveOmnigentCodexPackage(fifo, "linux", "arm64"); err == nil {
		t.Fatal("selected FIFO accepted")
	}
	wrapper, _, metadata, _ := omnigentCodexNpmFixture(t, "linux", "arm64", "bin", "nested")
	if err := os.Remove(metadata); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveOmnigentCodexPackage(wrapper, "linux", "arm64"); err == nil {
		t.Fatal("FIFO metadata accepted")
	}
}
