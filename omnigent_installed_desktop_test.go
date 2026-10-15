//go:build desktop && darwin

package main

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

var omnigentAcceptanceNativeApp = make(chan *app)

// The explicit guest-only mode keeps the real Cocoa event loop on the main
// thread while the installed fixture provides its synthetic gateway and vault.
func TestMain(m *testing.M) {
	if os.Getenv("KILO_OMNIGENT_CUA_NATIVE_UI") != "1" {
		os.Exit(m.Run())
	}
	if !flag.Parsed() {
		flag.Parse()
	}
	if os.Getenv("KILO_OMNIGENT_CUA_GUEST") != "1" || flag.Lookup("test.run").Value.String() != "^TestOmnigentInstalledPrivateWorkspace$" {
		fmt.Fprintln(os.Stderr, "Native Omnigent acceptance requires the explicit disposable guest fixture.")
		os.Exit(2)
	}
	go func() { os.Exit(m.Run()) }()
	a := <-omnigentAcceptanceNativeApp
	if err := a.runDesktop("", false, "", a.requestQuit); err != nil {
		fmt.Fprintln(os.Stderr, "The isolated native acceptance window could not start.")
		os.Exit(1)
	}
}

func omnigentAcceptanceNativeReady(a *app) error {
	if os.Getenv("KILO_OMNIGENT_CUA_NATIVE_UI") == "1" {
		omnigentAcceptanceNativeApp <- a
	}
	return nil
}
