//go:build desktop

package main

import (
	"fmt"
	"time"
)

func waitDesktopClosed(component string, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return fmt.Errorf("native %s did not finish closing", component)
	}
}
