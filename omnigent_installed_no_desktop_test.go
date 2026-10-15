//go:build !desktop || !darwin

package main

import (
	"errors"
	"os"
)

func omnigentAcceptanceNativeReady(*app) error {
	if os.Getenv("KILO_OMNIGENT_CUA_NATIVE_UI") == "1" {
		return errors.New("Native Omnigent acceptance requires a macOS Desktop test build.")
	}
	return nil
}
