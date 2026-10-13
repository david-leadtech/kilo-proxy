//go:build desktop

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDesktopWaitsForNativeLoopReturn(t *testing.T) {
	loopDone := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- waitDesktopClosed("tray", loopDone) }()
	// Quit/onExit callbacks may have run, but the native message loop has not
	// returned yet. Process exit must not race its remaining native cleanup.
	select {
	case err := <-result:
		t.Fatalf("tray wait returned before the native loop: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(loopDone)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tray wait did not observe the native loop returning")
	}
}

func TestDesktopIncompleteNativeCloseFails(t *testing.T) {
	for _, component := range []string{"window", "tray"} {
		if err := waitDesktopClosed(component, make(chan struct{})); err == nil {
			t.Fatalf("incomplete %s close succeeded", component)
		}
	}
}

func TestDesktopProcessExitPreservesCode(t *testing.T) {
	if os.Getenv("KILO_TEST_DESKTOP_EXIT_CHILD") == "1" {
		code, err := strconv.Atoi(os.Args[len(os.Args)-1])
		if err != nil {
			os.Exit(99)
		}
		if err := os.WriteFile(os.Getenv("KILO_TEST_DESKTOP_EXIT_MARKER"), []byte("saved before exit"), 0600); err != nil {
			os.Exit(97)
		}
		exitDesktopProcess(code)
		os.Exit(98) // The production exit function must not return.
	}
	for _, code := range []int{0, 7} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		marker := filepath.Join(t.TempDir(), "saved-state")
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDesktopProcessExitPreservesCode$", "--", strconv.Itoa(code))
		cmd.Env = append(os.Environ(), "KILO_TEST_DESKTOP_EXIT_CHILD=1", "KILO_TEST_DESKTOP_EXIT_MARKER="+marker)
		output, err := cmd.CombinedOutput()
		cancel()
		got := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatal(err)
			}
			got = exitErr.ExitCode()
		}
		if got != code {
			t.Fatalf("desktop exit code = %d, want %d: %s", got, code, output)
		}
		if saved, err := os.ReadFile(marker); err != nil || string(saved) != "saved before exit" {
			t.Fatalf("state was not persisted before exit: %q, %v", saved, err)
		}
	}
}
