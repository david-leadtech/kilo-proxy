//go:build desktop

package main

import (
	"testing"
	"time"
)

// Proxy validation can deliberately hold app.mu while inspecting a managed
// installation. It must not prevent the window from producing another frame.
func TestNativeFrameDoesNotWaitForProxyMutex(t *testing.T) {
	for _, page := range []string{"agents", "activity", "settings"} {
		t.Run(page, func(t *testing.T) {
			u := nativeTestUI(t)
			u.page = page
			nativeTestFrame(t, u)
			done := make(chan struct{})
			u.owner.mu.Lock()
			started := time.Now()
			go func() { nativeTestFrame(t, u); close(done) }()
			blocked := false
			select {
			case <-done:
			case <-time.After(250 * time.Millisecond):
				blocked = true
			}
			u.owner.mu.Unlock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("frame did not finish after releasing the proxy mutex")
			}
			t.Logf("frame completed in %s (proxy mutex held until observation)", time.Since(started))
			if blocked {
				t.Fatal("native frame waited for the proxy mutex")
			}
		})
	}
}
