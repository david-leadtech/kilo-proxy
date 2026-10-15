//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOmnigentRuntimeSpecialFilesRejectWithoutOpeningAStream(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "metadata-pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := omnigentReadRegular(fifo, 1024); err == nil {
		t.Fatal("accepted FIFO metadata")
	}
	if _, err := omnigentFileHash(fifo); err == nil {
		t.Fatal("accepted FIFO source")
	}
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fifo, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := omnigentReadRegular(fifo, 1024); err == nil {
		t.Fatal("accepted directory metadata")
	}
}
