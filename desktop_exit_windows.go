//go:build desktop && windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Called only after the desktop controller has saved state, closed services
// and completed bounded window/tray shutdown (an incomplete close exits nonzero). ExitProcess (used by os.Exit) forcibly stops the
// other threads before invoking DLL detach hooks; native GUI/driver hooks can
// then crash or deadlock. Terminating our own process avoids that second round
// of native cleanup. This must never replace the explicit application cleanup.
func exitDesktopProcess(code int) {
	if err := windows.TerminateProcess(windows.CurrentProcess(), uint32(code)); err != nil {
		fmt.Fprintln(os.Stderr, "Could not terminate the desktop process:", err)
		os.Exit(1)
	}
}
