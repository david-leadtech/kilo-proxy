//go:build desktop && darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <stdint.h>
void kiloInstallLifecycle(uintptr_t view);
int kiloWindowVisible(uintptr_t view);
int kiloRaiseVisibleWindow(uintptr_t view);
*/
import "C"

import (
	gioapp "gioui.org/app"
	"sync"
)

var nativeLifecycle struct {
	sync.Mutex
	desktop *nativeDesktop
}

func installNativeLifecycle(d *nativeDesktop, event gioapp.ViewEvent) {
	view := event.(gioapp.AppKitViewEvent).View
	d.mu.Lock()
	d.nativeView = view
	d.mu.Unlock()
	nativeLifecycle.Lock()
	nativeLifecycle.desktop = d
	nativeLifecycle.Unlock()
	C.kiloInstallLifecycle(C.uintptr_t(view))
}

//export kiloNativeQuit
func kiloNativeQuit() {
	nativeLifecycle.Lock()
	d := nativeLifecycle.desktop
	nativeLifecycle.Unlock()
	if d != nil {
		d.owner.requestQuit()
	}
}

//export kiloNativeReopen
func kiloNativeReopen() {
	nativeLifecycle.Lock()
	d := nativeLifecycle.desktop
	nativeLifecycle.Unlock()
	if d != nil {
		d.dispatch("open")
	}
}

//export kiloNativeHide
func kiloNativeHide() {
	nativeLifecycle.Lock()
	d := nativeLifecycle.desktop
	nativeLifecycle.Unlock()
	if d != nil {
		d.mu.Lock()
		d.hidden = true
		d.mu.Unlock()
	}
}

func (d *nativeDesktop) windowVisible() bool {
	d.mu.Lock()
	w, view := d.window, d.nativeView
	d.mu.Unlock()
	if w == nil || view == 0 {
		return false
	}
	visible := false
	w.Run(func() { visible = C.kiloWindowVisible(C.uintptr_t(view)) != 0 })
	return visible
}

// Deferred activation belongs to this exact new native view. Run the final
// checks and visible-only raise on Cocoa's thread, serialized with native Close.
func (d *nativeDesktop) raiseNewWindow(ready desktopWindowReady) {
	event, ok := ready.view.(gioapp.AppKitViewEvent)
	if !ok || event.View == 0 || ready.window == nil {
		return
	}
	d.runWindowRaise(ready, ready.window.Run, func() {
		C.kiloRaiseVisibleWindow(C.uintptr_t(event.View))
	})
}
