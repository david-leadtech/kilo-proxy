//go:build desktop

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	gioapp "gioui.org/app"
	"gioui.org/io/clipboard"
	gioevent "gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

type desktopWindowReady struct {
	window     *gioapp.Window
	generation uint64
	view       gioapp.ViewEvent
}

type nativeDesktop struct {
	owner            *app
	ui               *nativeUI
	cleanup          func()
	backendActions   chan string
	backendResults   chan desktopBackendResult
	trayWake         chan struct{}
	trayProjection   atomic.Pointer[trayState]
	lastMessage      string
	actions          chan string
	trayReady        chan struct{}
	trayDone         chan struct{}
	trayOnce         sync.Once
	firstFrameOnce   sync.Once
	firstFrameReady  chan struct{}
	trayEnd          func()
	items            map[string]*systray.MenuItem
	mu               sync.Mutex
	window           *gioapp.Window
	windowDone       chan struct{}
	nativeView       uintptr
	hidden           bool
	frames           uint64
	generation       uint64
	jobs             []func()
	copyPending      []string
	clipboardRead    chan string
	clipboardWaiting chan string
	clipboardTag     struct{}
	last             trayState
	message          string
	quitLabel        string
	exitCode         int

	windowReady           chan desktopWindowReady
	windowRaiseGeneration uint64
}

// CopyText schedules the OS clipboard operation on the next native frame. It
// does not depend on a browser API or a separate clipboard executable.
func (d *nativeDesktop) CopyText(text string) error {
	d.mu.Lock()
	if d.window == nil {
		d.mu.Unlock()
		return errors.New("Open the Kilo Proxy window before copying")
	}
	if len(d.copyPending) >= 32 {
		d.mu.Unlock()
		return errors.New("The clipboard is busy; try again")
	}
	d.copyPending = append(d.copyPending, text)
	w := d.window
	d.mu.Unlock()
	w.Invalidate()
	return nil
}

func (d *nativeDesktop) OpenExternal(address string) error { return openBrowser(address) }

func (d *nativeDesktop) invalidate() {
	d.mu.Lock()
	w := d.window
	d.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (d *nativeDesktop) dispatch(action string) {
	select {
	case d.actions <- action:
	case <-d.owner.quit:
	default:
	}
}

// A closed window does not drain UI updates. Never block the tray controller
// on a full queue: it must still be able to reopen the window or process Quit.
func (u *nativeUI) reportProxyStartFailure(err error) {
	go u.enqueue(func() {
		u.setNotice(nativeToneError, describeProxyStartFailure(err, u.language).Message)
	})
}

func (d *nativeDesktop) show() {
	d.showWindow(true)
}

// Initial startup creates the native window to initialize Cocoa and the tray.
// Only a later explicit Open requests activation after a new window is ready.
func (d *nativeDesktop) showWindow(explicit bool) {
	d.mu.Lock()
	if w := d.window; w != nil {
		d.hidden = false
		d.windowRaiseGeneration = 0
		d.mu.Unlock()
		w.Option(gioapp.Windowed.Option())
		w.Perform(system.ActionRaise)
		w.Invalidate()
		return
	}
	w := new(gioapp.Window)
	ready := d.registerWindowLocked(w, explicit, runtime.GOOS)
	done := d.windowDone
	d.mu.Unlock()
	go d.runWindow(w, done, ready.generation)
}

func (d *nativeDesktop) registerWindowLocked(w *gioapp.Window, explicit bool, platform string) desktopWindowReady {
	d.window = w
	d.hidden = false
	d.windowDone = make(chan struct{})
	d.generation++
	d.windowRaiseGeneration = 0
	if explicit && platform == "darwin" {
		d.windowRaiseGeneration = d.generation
	}
	return desktopWindowReady{window: w, generation: d.generation}
}

// Frame delivery must never wait for the controller. Coalesce a stale ready
// notification so that a rapid close/reopen cannot lose the current window.
func (d *nativeDesktop) queueWindowReady(ready desktopWindowReady) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.windowReady == nil || d.window != ready.window || d.generation != ready.generation || d.windowRaiseGeneration != ready.generation {
		return
	}
	select {
	case d.windowReady <- ready:
	default:
		select {
		case <-d.windowReady:
		default:
		}
		d.windowReady <- ready
	}
}

// The controller serializes activation with Close. Recheck identity and intent
// here rather than raising from the frame goroutine after a newer Close.
func (d *nativeDesktop) readyWindowForRaise(ready desktopWindowReady) *gioapp.Window {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.window != ready.window || d.generation != ready.generation || d.windowRaiseGeneration != ready.generation {
		return nil
	}
	d.windowRaiseGeneration = 0
	if d.hidden {
		return nil
	}
	select {
	case <-d.owner.quit:
		return nil
	default:
		return ready.window
	}
}

// Dispatch without holding d.mu, then validate on the native event thread.
// A native red-button Close may occur while the callback is waiting to run.
func (d *nativeDesktop) runWindowRaise(ready desktopWindowReady, run func(func()), raise func()) {
	run(func() {
		if d.readyWindowForRaise(ready) != nil {
			raise()
		}
	})
}

func (d *nativeDesktop) windowForClose() *gioapp.Window {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hidden = true
	d.windowRaiseGeneration = 0
	return d.window
}

func (d *nativeDesktop) runWindow(w *gioapp.Window, done chan struct{}, generation uint64) {
	defer close(done)
	defer func() {
		d.mu.Lock()
		if d.window == w {
			d.window = nil
		}
		d.mu.Unlock()
	}()
	w.Option(gioapp.Title("Kilo Proxy"), gioapp.Size(unit.Dp(1180), unit.Dp(820)), gioapp.MinSize(unit.Dp(720), unit.Dp(560)))
	var ops op.Ops
	var nativeViewEvent gioapp.ViewEvent
	lifecycleInstalled := false
	windowReady := false
	for {
		switch e := w.Event().(type) {
		case gioapp.DestroyEvent:
			if e.Err != nil {
				fmt.Fprintln(os.Stderr, "Native window failed:", e.Err)
				d.mu.Lock()
				d.exitCode = 1
				d.mu.Unlock()
				d.owner.requestQuit()
			}
			return
		case gioapp.ViewEvent:
			if e.Valid() && runtime.GOOS == "darwin" {
				nativeViewEvent = e
				// Cocoa has one application loop. Register on Gio's native
				// thread, without replacing its application delegate.
				d.trayOnce.Do(func() {
					w.Run(func() {
						start, end := systray.RunWithExternalLoop(d.setupTray, func() {})
						d.trayEnd = end
						start()
					})
				})
			}
		case gioapp.FrameEvent:
			// AppKit emits ViewEvent while setContentView is still executing,
			// before Gio assigns the NSWindow delegate. Install the close hook
			// after creation completes, at the first real native frame.
			if runtime.GOOS == "darwin" && !lifecycleInstalled && nativeViewEvent != nil {
				w.Run(func() { installNativeLifecycle(d, nativeViewEvent) })
				lifecycleInstalled = true
			}
			gtx := gioapp.NewContext(&ops, e)
			d.processFrame(gtx)
			d.ui.Layout(gtx)
			e.Frame(gtx.Ops)
			if !windowReady && (runtime.GOOS != "darwin" || lifecycleInstalled) {
				d.queueWindowReady(desktopWindowReady{window: w, generation: generation, view: nativeViewEvent})
				windowReady = true
			}
			d.mu.Lock()
			d.frames++
			d.mu.Unlock()
			d.firstFrameOnce.Do(func() { close(d.firstFrameReady) })
		}
	}
}

func (d *nativeDesktop) processFrame(gtx layout.Context) {
	gioevent.Op(gtx.Ops, &d.clipboardTag)
	for {
		e, ok := gtx.Event(transfer.TargetFilter{Target: &d.clipboardTag, Type: "text/plain"}, transfer.TargetFilter{Target: &d.clipboardTag, Type: "application/text"})
		if !ok {
			break
		}
		if data, ok := e.(transfer.DataEvent); ok {
			stream := data.Open()
			text, err := io.ReadAll(io.LimitReader(stream, 1<<20))
			_ = stream.Close()
			d.mu.Lock()
			waiting := d.clipboardWaiting
			d.clipboardWaiting = nil
			d.mu.Unlock()
			if waiting != nil && err == nil {
				select {
				case waiting <- string(text):
				default:
				}
			}
		}
	}
	d.mu.Lock()
	jobs, copies, read := d.jobs, d.copyPending, d.clipboardRead
	d.jobs, d.copyPending, d.clipboardRead = nil, nil, nil
	if read != nil {
		d.clipboardWaiting = read
	}
	d.mu.Unlock()
	for _, job := range jobs {
		job()
	}
	for _, text := range copies {
		gtx.Execute(clipboard.WriteCmd{Type: "text/plain", Data: io.NopCloser(strings.NewReader(text))})
	}
	if read != nil {
		gtx.Execute(clipboard.ReadCmd{Tag: &d.clipboardTag})
	}
}

func trayICO(png []byte) []byte {
	// ICO with an embedded PNG uses Windows' built-in icon decoder.
	result := make([]byte, 22, 22+len(png))
	result[2], result[4], result[6], result[7], result[10], result[12], result[18] = 1, 1, 44, 44, 1, 32, 22
	size := uint32(len(png))
	for i := 0; i < 4; i++ {
		result[14+i] = byte(size >> (8 * i))
	}
	return append(result, png...)
}

func (d *nativeDesktop) setupTray() {
	if runtime.GOOS == "darwin" {
		systray.SetTemplateIcon(trayIcon(true), trayIcon(false))
	} else if runtime.GOOS == "windows" {
		systray.SetIcon(trayICO(trayIcon(false)))
	} else {
		systray.SetIcon(trayIcon(false))
	}
	systray.SetTitle("")
	d.items = map[string]*systray.MenuItem{}
	systray.AddMenuItem("Kilo Proxy · "+version, "").Disable()
	for _, key := range []string{"status", "team", "address", "activity", "spend", "coverage", "balance"} {
		d.items[key] = systray.AddMenuItem(" ", "")
		d.items[key].Disable()
	}
	systray.AddSeparator()
	for _, key := range []string{"open", "start", "stop"} {
		d.items[key] = systray.AddMenuItem(" ", "")
	}
	d.items["message"] = systray.AddMenuItem(" ", "")
	d.items["message"].Disable()
	systray.AddSeparator()
	d.items["quit"] = systray.AddMenuItem("Quit Kilo Proxy", "")
	for _, key := range []string{"open", "start", "stop", "quit"} {
		action, clicked := key, d.items[key].ClickedCh
		go func() {
			for {
				select {
				case <-clicked:
					d.dispatch(action)
				case <-d.owner.quit:
					return
				}
			}
		}()
	}
	close(d.trayReady)
}

func (d *nativeDesktop) updateTray() {
	select {
	case <-d.trayReady:
	default:
		return
	}
	s := d.owner.trayState()
	d.trayProjection.Store(&s)
	select {
	case <-d.owner.quit:
		return
	default:
	}
	d.mu.Lock()
	message := d.message
	d.mu.Unlock()
	if s == d.last && message == d.lastMessage {
		return
	}
	if s.display != d.last.display || s.display != trayDisplayIcon && s.amount != d.last.amount {
		if s.display == trayDisplaySpend || s.display == trayDisplayBalance || s.display == trayDisplayChatGPTQuota {
			systray.SetTitle(s.amount)
		} else {
			systray.SetTitle("")
		}
	}
	d.items["status"].SetTitle(s.status)
	d.items["team"].SetTitle(s.team)
	d.items["address"].SetTitle(s.labels.endpoint + s.address)
	d.items["activity"].SetTitle(s.activity)
	d.items["spend"].SetTitle(s.spend)
	d.items["coverage"].SetTitle(s.coverage)
	d.items["balance"].SetTitle(s.balance)
	d.items["open"].SetTitle(s.labels.open)
	start := s.labels.setup
	if s.configured {
		start = s.labels.start
	}
	d.items["start"].SetTitle(start)
	if s.running || s.pending {
		d.items["start"].Disable()
	} else {
		d.items["start"].Enable()
	}
	d.items["stop"].SetTitle(s.labels.stop)
	if s.running {
		d.items["stop"].Enable()
	} else {
		d.items["stop"].Disable()
	}
	d.items["quit"].SetTitle(s.labels.exit)
	d.items["message"].SetTitle(s.labels.message(message))
	systray.SetTooltip(s.tooltip)
	d.mu.Lock()
	d.quitLabel = s.labels.exit
	d.mu.Unlock()
	d.last, d.lastMessage = s, message
}

type desktopBackendResult struct {
	action     string
	needsSetup bool
	err        error
}

func (d *nativeDesktop) setTrayMessage(message string) {
	d.mu.Lock()
	d.message = message
	d.mu.Unlock()
	select {
	case d.trayWake <- struct{}{}:
	default:
	}
}

// This worker alone owns native tray updates. Both backend snapshot collection
// and synchronous platform setters may wait; neither blocks window actions.
func (d *nativeDesktop) runTrayUpdates() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.owner.quit:
			return
		case <-ticker.C:
			d.updateTray()
		case <-d.trayWake:
			d.updateTray()
		}
	}
}

// Preserve Start/Stop ordering while keeping backend validation and shutdown
// outside the controller that handles Open and Close.
func (d *nativeDesktop) runBackendActions() {
	for {
		select {
		case <-d.owner.quit:
			return
		case action := <-d.backendActions:
			select {
			case <-d.owner.quit:
				return
			default:
			}
			result := desktopBackendResult{action: action}
			if action == "start" {
				result.needsSetup = !d.owner.trayState().configured
				if !result.needsSetup {
					result.err = d.owner.start()
				}
			} else if action == "stop" {
				d.owner.stop()
			}
			select {
			case d.backendResults <- result:
			case <-d.owner.quit:
				return
			}
		}
	}
}

func (d *nativeDesktop) handleControlAction(action string) {
	d.setTrayMessage("")
	switch action {
	case "open":
		d.show()
	case "close":
		if w := d.windowForClose(); w != nil {
			w.Perform(system.ActionClose)
		}
	case "start", "stop":
		select {
		case d.backendActions <- action:
		default:
		}
	case "quit":
		d.owner.requestQuit()
	}
}

func (d *nativeDesktop) control() {
	for {
		select {
		case <-d.owner.quit:
			d.ui.shutdownModelLibrary()
			d.cleanup()
			d.mu.Lock()
			w, done, code := d.window, d.windowDone, d.exitCode
			d.mu.Unlock()
			if w != nil {
				w.Perform(system.ActionClose)
				if runtime.GOOS == "windows" {
					if err := waitDesktopClosed("window", done); err != nil {
						fmt.Fprintln(os.Stderr, err)
						code = 1
					}
				} else {
					// Cocoa intercepts ActionClose to hide the window. Preserve
					// the existing bounded wait on platforms using os.Exit.
					select {
					case <-done:
					case <-time.After(2 * time.Second):
					}
				}
			}
			select {
			case <-d.trayReady:
				if d.trayEnd != nil {
					d.trayEnd()
				} else {
					systray.Quit()
					if runtime.GOOS == "windows" {
						if err := waitDesktopClosed("tray", d.trayDone); err != nil {
							fmt.Fprintln(os.Stderr, err)
							code = 1
						}
					}
				}
			default:
			}
			d.mu.Lock()
			if d.exitCode != 0 {
				code = d.exitCode
			}
			d.mu.Unlock()
			// Gio owns the startup thread indefinitely. Terminate only after
			// saving state and closing application services and native UI.
			exitDesktopProcess(code)
		case ready := <-d.windowReady:
			d.raiseNewWindow(ready)
		case action := <-d.actions:
			d.handleControlAction(action)
		case result := <-d.backendResults:
			if result.action == "start" {
				if result.needsSetup {
					d.show()
				} else if result.err != nil {
					d.setTrayMessage("start-error")
					d.show()
					d.ui.reportProxyStartFailure(result.err)
				}
			} else if result.action == "stop" {
				d.invalidate()
			}
			select {
			case d.trayWake <- struct{}{}:
			default:
			}
		}
	}
}

func (a *app) runDesktop(_ string, hidden bool, selfTest string, cleanup func()) error {
	d := &nativeDesktop{owner: a, cleanup: cleanup, actions: make(chan string, 16), backendActions: make(chan string, 16), backendResults: make(chan desktopBackendResult, 16), trayWake: make(chan struct{}, 1), trayReady: make(chan struct{}), firstFrameReady: make(chan struct{}), windowReady: make(chan desktopWindowReady, 1)}
	a.mu.Lock()
	a.desktop = d
	a.publishUIProjectionLocked()
	a.mu.Unlock()
	d.ui = newNativeUI(a, d.invalidate)
	if runtime.GOOS != "darwin" {
		d.trayDone = make(chan struct{})
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			defer close(d.trayDone)
			systray.Run(d.setupTray, func() {})
		}()
	}
	go d.runTrayUpdates()
	go d.runBackendActions()
	go d.control()
	// Cocoa initializes the tray through the first real window. Hidden startup
	// closes that window after the tray is ready while retaining application state.
	d.showWindow(false)
	if hidden {
		go func() { <-d.trayReady; <-d.firstFrameReady; d.dispatch("close") }()
	}
	if selfTest != "" {
		go d.selfTest(selfTest)
	}
	gioapp.Main()
	return nil
}
