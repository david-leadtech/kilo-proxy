//go:build desktop

package main

import (
	"testing"

	gioapp "gioui.org/app"
)

func activationTestDesktop() *nativeDesktop {
	return &nativeDesktop{
		owner:       &app{quit: make(chan struct{})},
		windowReady: make(chan desktopWindowReady, 1),
	}
}

func activationTestRegister(d *nativeDesktop, w *gioapp.Window, explicit bool, platform string) desktopWindowReady {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.registerWindowLocked(w, explicit, platform)
}

func TestDesktopWindowActivationExplicitNewDarwin(t *testing.T) {
	d := activationTestDesktop()
	w := new(gioapp.Window)
	ready := activationTestRegister(d, w, true, "darwin")
	d.queueWindowReady(ready)
	select {
	case event := <-d.windowReady:
		if event != ready {
			t.Fatal("the first ready notification did not preserve the explicit window identity")
		}
		if got := d.readyWindowForRaise(event); got != w {
			t.Fatal("explicit Open did not request activation of its new ready Mac window")
		}
		if got := d.readyWindowForRaise(event); got != nil {
			t.Fatal("the same window-ready event activated the window twice")
		}
	default:
		t.Fatal("explicit Open lost the new Mac window's first ready notification")
	}
}

func TestDesktopWindowActivationDoesNotRaiseInitialOrOtherPlatforms(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit bool
		platform string
	}{
		{"initial-mac-including-hidden-startup", false, "darwin"},
		{"explicit-linux", true, "linux"},
		{"explicit-windows", true, "windows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := activationTestDesktop()
			ready := activationTestRegister(d, new(gioapp.Window), test.explicit, test.platform)
			d.queueWindowReady(ready)
			select {
			case <-d.windowReady:
				t.Fatal("startup or another platform acquired a deferred Mac activation")
			default:
			}
			if got := d.readyWindowForRaise(ready); got != nil {
				t.Fatal("a window without an explicit Mac Open intent could activate")
			}
		})
	}
}

func TestDesktopWindowActivationCloseCancelsBeforeAndAfterReady(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-ready", true: "after-ready"}[queued], func(t *testing.T) {
			d := activationTestDesktop()
			w := new(gioapp.Window)
			ready := activationTestRegister(d, w, true, "darwin")
			if queued {
				d.queueWindowReady(ready)
			}
			if got := d.windowForClose(); got != w {
				t.Fatal("Close did not retain the current window for its native close action")
			}
			if !d.hidden {
				t.Fatal("Close did not mark the window hidden before the native close action")
			}
			if !queued {
				d.queueWindowReady(ready)
				select {
				case <-d.windowReady:
					t.Fatal("Close before readiness still queued activation")
				default:
				}
			}
			if got := d.readyWindowForRaise(ready); got != nil {
				t.Fatal("a deferred ready notification reopened a closed window")
			}
		})
	}
}

func TestDesktopWindowActivationCoalescesOldReadyOnReopen(t *testing.T) {
	d := activationTestDesktop()
	oldWindow, currentWindow := new(gioapp.Window), new(gioapp.Window)
	oldReady := activationTestRegister(d, oldWindow, true, "darwin")
	d.queueWindowReady(oldReady)
	d.windowForClose()
	currentReady := activationTestRegister(d, currentWindow, true, "darwin")
	if got := d.readyWindowForRaise(oldReady); got != nil {
		t.Fatal("the old window activated after a new generation was registered")
	}
	d.queueWindowReady(currentReady)
	d.queueWindowReady(oldReady)
	select {
	case got := <-d.windowReady:
		if got != currentReady {
			t.Fatal("a full stale-ready queue lost or replaced the reopened current window")
		}
		if d.readyWindowForRaise(got) != currentWindow {
			t.Fatal("a stale notification consumed the current generation's activation")
		}
	default:
		t.Fatal("reopened current window has no ready notification")
	}
}

func TestDesktopWindowActivationRejectsForeignIdentityAndGeneration(t *testing.T) {
	d := activationTestDesktop()
	w := new(gioapp.Window)
	ready := activationTestRegister(d, w, true, "darwin")
	for _, foreign := range []desktopWindowReady{
		{window: new(gioapp.Window), generation: ready.generation},
		{window: w, generation: ready.generation + 1},
		{window: nil, generation: ready.generation},
	} {
		d.queueWindowReady(foreign)
		if d.readyWindowForRaise(foreign) != nil {
			t.Fatal("foreign window/generation was accepted")
		}
	}
	d.queueWindowReady(ready)
	if got := <-d.windowReady; got != ready || d.readyWindowForRaise(got) != w {
		t.Fatal("rejecting foreign readiness lost the exact current Open intent")
	}
}

func TestDesktopWindowActivationQuitAndNativeHideCancel(t *testing.T) {
	for _, quit := range []bool{false, true} {
		t.Run(map[bool]string{false: "native-hide", true: "quit"}[quit], func(t *testing.T) {
			d := activationTestDesktop()
			ready := activationTestRegister(d, new(gioapp.Window), true, "darwin")
			d.queueWindowReady(ready)
			if quit {
				close(d.owner.quit)
			} else {
				d.mu.Lock()
				d.hidden = true
				d.mu.Unlock()
			}
			if d.readyWindowForRaise(<-d.windowReady) != nil {
				t.Fatal("pending activation survived Quit or native hide")
			}
		})
	}
}

func TestDesktopWindowActivationControllerCloseOrdering(t *testing.T) {
	for _, closeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "raise-then-close", true: "close-then-ready"}[closeFirst], func(t *testing.T) {
			d := activationTestDesktop()
			w := new(gioapp.Window)
			ready := activationTestRegister(d, w, true, "darwin")
			d.queueWindowReady(ready)
			if closeFirst {
				d.windowForClose()
			}
			got := d.readyWindowForRaise(<-d.windowReady)
			if closeFirst && got != nil {
				t.Fatal("controller raised after it already processed Close")
			}
			if !closeFirst {
				if got != w {
					t.Fatal("controller lost a current explicit Open before Close")
				}
				d.windowForClose()
			}
			if !d.hidden || d.windowRaiseGeneration != 0 {
				t.Fatal("a later Close left the window visible or eligible for deferred activation")
			}
		})
	}
}

func TestDesktopWindowActivationRevalidatesOnNativeThread(t *testing.T) {
	for _, change := range []string{"native-close", "quit", "new-generation"} {
		t.Run(change, func(t *testing.T) {
			d := activationTestDesktop()
			ready := activationTestRegister(d, new(gioapp.Window), true, "darwin")
			var pending func()
			raises := 0
			d.runWindowRaise(ready, func(f func()) { pending = f }, func() { raises++ })
			if pending == nil || raises != 0 {
				t.Fatal("activation was not deferred to the native thread")
			}
			switch change {
			case "native-close":
				d.mu.Lock()
				d.hidden = true
				d.mu.Unlock()
			case "quit":
				close(d.owner.quit)
			case "new-generation":
				activationTestRegister(d, new(gioapp.Window), true, "darwin")
			}
			pending()
			if raises != 0 {
				t.Fatal("a stale activation survived native Close, Quit or a new window before its native callback")
			}
		})
	}
}

func TestDesktopWindowActivationNativeCloseOrder(t *testing.T) {
	for _, closeFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "native-close-before-raise", false: "raise-before-native-close"}[closeFirst], func(t *testing.T) {
			d := activationTestDesktop()
			ready := activationTestRegister(d, new(gioapp.Window), true, "darwin")
			var pending func()
			visible, raises := true, 0
			d.runWindowRaise(ready, func(f func()) { pending = f }, func() { visible = true; raises++ })
			nativeClose := func() {
				visible = false
				d.mu.Lock()
				d.hidden = true
				d.mu.Unlock()
			}
			if closeFirst {
				nativeClose()
			}
			pending()
			if !closeFirst {
				nativeClose()
			}
			expectedRaises := 1
			if closeFirst {
				expectedRaises = 0
			}
			if visible || raises != expectedRaises {
				t.Fatal("the native Close did not remain final in native callback order")
			}
		})
	}
}
