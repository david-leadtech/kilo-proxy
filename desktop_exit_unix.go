//go:build desktop && !windows

package main

import "os"

func exitDesktopProcess(code int) {
	os.Exit(code)
}
