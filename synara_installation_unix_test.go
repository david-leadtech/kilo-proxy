//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSynaraDiscoveryDoesNotOpenPipesAsDirectoriesOrPackages(t *testing.T) {
	home := t.TempDir()
	pipe := filepath.Join(home, "Downloads")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pipe, filepath.Join(home, "Applications")); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(home, "Synara-1.0.3-x86_64.AppImage")
	if err := os.Symlink(pipe, image); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if got := synaraAppImageCandidates(home); len(got) != 0 {
			t.Error("pipe was discovered as an AppImage", got)
		}
		if synaraAppImage(image) {
			t.Error("pipe was treated as a regular package")
		}
		file, err := synaraOpenCandidate(pipe)
		if err != nil {
			t.Error(err)
		} else {
			file.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("discovery blocked opening a pipe")
	}
}
