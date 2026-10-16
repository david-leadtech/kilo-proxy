//go:build desktop

package main

import (
	"runtime"
	"testing"
	"time"
)

func TestNativeBackendWorkerPreservesOrderWithoutBlockingController(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	d := &nativeDesktop{owner: a, actions: make(chan string, 16), backendActions: make(chan string, 16), backendResults: make(chan desktopBackendResult, 16), trayWake: make(chan struct{}, 1)}
	finished := make(chan struct{})
	go func() { d.runBackendActions(); close(finished) }()
	a.mu.Lock()
	d.handleControlAction("start")
	deadline := time.Now().Add(time.Second)
	for len(d.backendActions) != 0 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if len(d.backendActions) != 0 {
		a.mu.Unlock()
		a.requestQuit()
		<-finished
		t.Fatal("backend worker did not receive Start")
	}
	done := make(chan struct{})
	go func() { d.handleControlAction("close"); d.handleControlAction("stop"); close(done) }()
	blocked := false
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	a.mu.Unlock()
	<-done
	for _, action := range []string{"start", "stop"} {
		select {
		case result := <-d.backendResults:
			if result.action != action || result.err != nil || action == "start" && !result.needsSetup {
				t.Errorf("unexpected backend result: %+v", result)
			}
		case <-time.After(3 * time.Second):
			t.Error("backend action did not finish")
		}
	}
	a.requestQuit()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("backend worker leaked after Quit")
	}
	if blocked {
		t.Fatal("controller action waited for the proxy/backend worker")
	}
}

func TestNativeTrayWorkerJoinsAfterQuitWithoutWindow(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	d := &nativeDesktop{owner: a, trayReady: make(chan struct{}), trayWake: make(chan struct{}, 1)}
	finished := make(chan struct{})
	go func() { d.runTrayUpdates(); close(finished) }()
	d.setTrayMessage("synthetic-message")
	a.requestQuit()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("tray worker leaked after Quit")
	}
}

func TestNativeBackendWorkerDoesNotStartQueuedActionAfterQuit(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	d := &nativeDesktop{owner: a, backendActions: make(chan string, 1), backendResults: make(chan desktopBackendResult, 1)}
	d.backendActions <- "start"
	a.requestQuit()
	finished := make(chan struct{})
	go func() { d.runBackendActions(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("backend worker leaked")
	}
	if len(d.backendResults) != 0 {
		t.Fatal("queued action ran after Quit")
	}
}
