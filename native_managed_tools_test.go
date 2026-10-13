//go:build desktop

package main

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

type nativeManagedToolsFixture struct {
	h           *nativePointerHarness
	recorder    *nativeLaunchRecorder
	mu          sync.Mutex
	state       managedToolsSnapshot
	checks      int
	installs    int
	cancels     int
	gets        int
	channel     string
	failGet     bool
	failInstall bool
}

func newNativeManagedToolsFixture(t *testing.T, key, language string, size image.Point) *nativeManagedToolsFixture {
	t.Helper()
	u, recorder := nativeLaunchTestUI(t, key, false, false)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	u.language, u.page, u.client = language, "clients", key
	u.owner.mu.Lock()
	u.owner.apiKey, u.owner.config.OrgID = "", ""
	u.owner.mu.Unlock()
	if u.agentConnectionReady() {
		t.Fatal("installation fixture must have no provider connection")
	}
	u.clientState().LaunchInfo.Platform = "darwin"
	f := &nativeManagedToolsFixture{recorder: recorder, state: managedToolsSnapshot{Platform: "darwin", Clients: map[string]managedToolStatus{key: {Client: key, Name: uxhName(key), Channels: managedToolChannels(key)}}}}
	upstream, err := url.Parse("http://" + u.owner.adminHost)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/tools/") {
			proxy.ServeHTTP(w, r)
			return
		}
		if !secureEqual(r.Header.Get("Authorization"), "Bearer "+u.owner.adminToken) {
			t.Error("managed tool request omitted local admin authentication")
			jsonError(w, 401, "missing authentication")
			return
		}
		f.mu.Lock()
		switch r.URL.Path {
		case managedToolsStatusEndpoint:
			f.gets++
			if r.Method != "GET" {
				t.Error("status must be GET")
			}
			if f.failGet {
				f.mu.Unlock()
				jsonError(w, 503, "synthetic status unavailable")
				return
			}
		case managedToolsCheckEndpoint:
			var body struct{ Client, Channel string }
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Client != key {
				t.Error("version check omitted the selected tool")
			}
			f.checks++
			f.channel = body.Channel
			status := f.state.Clients[key]
			status.Busy, status.Phase = true, "checking"
			f.state.Clients[key] = status
		case managedToolsInstallEndpoint:
			var body struct {
				Client  string `json:"client"`
				PlanID  string `json:"planID"`
				Confirm bool   `json:"confirm"`
			}
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Client != key || !body.Confirm || body.PlanID != f.state.Clients[key].PlanID {
				t.Error("installer omitted the concrete plan confirmation")
			}
			f.installs++
			if f.failInstall {
				f.mu.Unlock()
				jsonError(w, 409, "The installation plan has expired. Check versions again.")
				return
			}
			status := f.state.Clients[key]
			status.Busy, status.Phase = true, "downloading"
			f.state.Clients[key] = status
		case "/api/tools/cancel":
			f.cancels++
			status := f.state.Clients[key]
			status.Busy, status.Phase, status.Error = false, "failed", "The installation was cancelled. The previous installation is still available."
			f.state.Clients[key] = status
		default:
			t.Errorf("unexpected managed tools request %s", r.URL.Path)
		}
		raw, err := json.Marshal(f.state)
		f.mu.Unlock()
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	f.h = &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
	f.h.frame()
	return f
}

func (f *nativeManagedToolsFixture) setStatus(status managedToolStatus) {
	f.mu.Lock()
	f.state.Clients[status.Client] = status
	f.mu.Unlock()
}

func (f *nativeManagedToolsFixture) counts() (checks, installs, gets int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.installs, f.gets
}

func (f *nativeManagedToolsFixture) refresh(t *testing.T) {
	t.Helper()
	u := f.h.u
	u.refreshManagedTools()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+managedToolsStatusEndpoint] })
	f.h.frame()
}

func TestNativeManagedToolInstallWithoutConnectionOrProfile(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		for _, size := range []image.Point{{1180, 820}, {780, 700}} {
			t.Run(language+"-"+fmtSize(size), func(t *testing.T) {
				f := newNativeManagedToolsFixture(t, "codex-cli", language, size)
				h, u := f.h, f.h.u
				for i := 0; i < 3; i++ {
					h.frame()
				}
				if checks, installs, gets := f.counts(); checks != 0 || installs != 0 || gets != 0 {
					t.Fatal("rendering contacted the managed installation API")
				}
				check := u.tr("Check versions", "Comprobar versiones")
				h.reveal(check, semantic.Button)
				h.click(check, semantic.Button)
				nativeTestWait(t, u, func() bool { return !u.busy["POST"+managedToolsCheckEndpoint] })
				h.frame()
				if checks, installs, _ := f.counts(); checks != 1 || installs != 0 {
					t.Fatal("checking started an installer")
				}
				if !nativeUpdateHasText(h, u.tr("Checking versions…", "Comprobando versiones…")) {
					t.Fatal("async version check has no progress")
				}
				status := managedToolStatus{Client: "codex-cli", Name: "Codex CLI", Installed: true, InstalledVersion: "1.0.0", LatestVersion: "1.1.0", CanInstall: true, PlanID: "concrete-plan", Method: "kilo", Scope: "A separate Kilo-managed installation. Existing installations, accounts and chats are kept.", Phase: "complete"}
				f.setStatus(status)
				f.refresh(t)
				if nativeUpdateHasText(h, u.tr("Kilo copy ready. Reopen the tool to use it.", "Copia Kilo lista. Vuelve a abrir la herramienta para usarla.")) {
					t.Fatal("version check claimed installation success")
				}
				install := u.tr("Install Kilo copy", "Instalar copia Kilo")
				h.reveal(install, semantic.Button)
				h.click(install, semantic.Button)
				if _, installs, _ := f.counts(); installs != 0 {
					t.Fatal("installation did not wait for confirmation")
				}
				confirm := u.tr("Confirm installation", "Confirmar instalación")
				h.reveal(confirm, semantic.Button)
				for _, text := range []string{u.tr("Install Codex CLI 1.1.0?", "¿Instalar Codex CLI 1.1.0?"), u.tr("Method: Verified official download", "Método: Descarga oficial verificada"), u.tr("Location: Private tools folder in your Kilo Proxy workspace", "Ubicación: Carpeta privada de herramientas de tu espacio Kilo Proxy")} {
					if !nativeUpdateHasText(h, text) {
						t.Fatalf("confirmation omitted %q", text)
					}
				}
				nativeGridCapture(t, h, "managed-install-confirm-"+language+"-"+fmtSize(size))
				h.click(confirm, semantic.Button)
				nativeTestWait(t, u, func() bool { return !u.busy["POST"+managedToolsInstallEndpoint] })
				h.frame()
				if _, installs, _ := f.counts(); installs != 1 {
					t.Fatal("confirmed plan did not start exactly once")
				}
				if !nativeUpdateHasText(h, u.tr("Downloading…", "Descargando…")) {
					t.Fatal("download progress is missing")
				}
				before := f.recorder.gets.Load()
				status.Managed, status.InstalledVersion, status.CanInstall, status.Phase = true, "1.1.0", false, "complete"
				f.setStatus(status)
				f.refresh(t)
				nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeLaunchEndpoint] })
				if !nativeUpdateHasText(h, u.tr("Kilo copy ready. Reopen the tool to use it.", "Copia Kilo lista. Vuelve a abrir la herramienta para usarla.")) || f.recorder.gets.Load() != before+1 {
					t.Fatal("completion did not report success and refresh discovery")
				}
				if f.recorder.prepares.Load() != 0 || f.recorder.launchRequests.Load() != 0 || f.recorder.count() != 0 {
					t.Fatal("install flow prepared a profile or launched a tool")
				}
			})
		}
	}
}

func TestNativeManagedToolChannelsInvalidatePlan(t *testing.T) {
	for _, key := range []string{"t3-code", "synara", "claude", "zed"} {
		t.Run(key, func(t *testing.T) {
			f := newNativeManagedToolsFixture(t, key, "es", image.Pt(1180, 1000))
			h, u := f.h, f.h.u
			channels := managedToolChannels(key)
			if len(channels) < 2 {
				t.Fatal("multi-channel tool must expose available channels")
			}
			channel, target := channels[0], channels[len(channels)-1]
			u.setValue("tool:"+key+":channel", channel)
			status := managedToolStatus{Client: key, Installed: true, Managed: true, InstalledVersion: "1.0.0", InstalledChannel: channel, LatestVersion: "1.1.0", Channel: channel, Channels: channels, CanUpdate: true, PlanID: "old-plan", Phase: "complete"}
			f.setStatus(status)
			f.refresh(t)
			u.prepareManagedTool(key)
			h.frame()
			h.reveal(target, semantic.Button)
			h.click(target, semantic.Button)
			if u.managedToolsState().confirmPlan != "" || u.managedToolsState().snapshot.Clients[key].PlanID != "" {
				t.Fatal("changing channels retained an install confirmation")
			}
			u.installManagedTool(key)
			if _, installs, _ := f.counts(); installs != 0 {
				t.Fatal("stale channel plan was installed")
			}
			h.reveal("Comprobar versiones", semantic.Button)
			h.click("Comprobar versiones", semantic.Button)
			nativeTestWait(t, u, func() bool { return !u.busy["POST"+managedToolsCheckEndpoint] })
			f.mu.Lock()
			actual := f.channel
			f.mu.Unlock()
			if actual != target {
				t.Fatalf("selected channel %q was replaced with %q", target, actual)
			}
		})
	}
}

func TestNativeManagedToolCodexCustomPathRequiresExplicitChoice(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeManagedToolsFixture(t, "codex", language, image.Pt(1180, 1100))
			h, u := f.h, f.h.u
			a := u.agentsState()
			custom := filepath.Join(u.owner.dir, "custom", "Codex.app")
			a.Preferences.Folder = u.owner.dir
			a.Preferences.Projects = map[string]string{"claude": filepath.Join(u.owner.dir, "claude-project")}
			a.Preferences.LastOpened = map[string]int64{"codex": 1234}
			u.setValue("clients-launch-app-path", custom)
			u.uxhSavePreferences()
			before, err := readAgentPreferences(u.owner.dir)
			if err != nil || before.CodexAppPath != custom {
				t.Fatalf("custom path fixture was not saved: %v", err)
			}
			status := managedToolStatus{Client: "codex", Installed: true, InstalledVersion: "1.0.0", LatestVersion: "1.1.0", CanInstall: true, PlanID: "codex-plan", Phase: "complete"}
			f.setStatus(status)
			f.refresh(t)
			u.prepareManagedTool("codex")
			h.frame()
			h.reveal(u.tr("Confirm installation", "Confirmar instalación"), semantic.Button)
			if !nativeUpdateHasText(h, u.tr("Your existing installation and profiles are kept. Your custom application path stays selected until you choose Use Kilo copy.", "Tu instalación y perfiles actuales se conservan. Tu ruta de aplicación personalizada seguirá seleccionada hasta que elijas Usar copia Kilo.")) {
				t.Fatal("confirmation did not explain that the custom path stays selected")
			}
			status.Managed, status.InstalledVersion, status.CanInstall = true, "1.1.0", false
			f.setStatus(status)
			u.managedToolsState().installClient = "codex"
			f.refresh(t)
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeLaunchEndpoint] })
			h.frame()
			if !nativeUpdateHasText(h, u.tr("Kilo copy ready. Your custom application path is still selected.", "Copia Kilo lista. Tu ruta de aplicación personalizada sigue seleccionada.")) {
				t.Fatal("completion falsely claimed the managed copy would be used")
			}
			preserved, err := readAgentPreferences(u.owner.dir)
			if err != nil || !reflect.DeepEqual(preserved, before) || u.value("clients-launch-app-path") != custom {
				t.Fatal("installing a copy changed a remembered custom path or other preferences")
			}
			gets := f.recorder.gets.Load()
			label := u.tr("Use Kilo copy", "Usar copia Kilo")
			h.reveal(label, semantic.Button)
			h.click(label, semantic.Button)
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeLaunchEndpoint] })
			selected, err := readAgentPreferences(u.owner.dir)
			before.CodexAppPath = ""
			if err != nil || !reflect.DeepEqual(selected, before) || u.value("clients-launch-app-path") != "" || a.Preferences.CodexAppPath != "" {
				t.Fatalf("explicit choice changed more than the custom app path: %#v, %v", selected, err)
			}
			if f.recorder.gets.Load() != gets+1 || f.recorder.launchRequests.Load() != 0 || f.recorder.prepares.Load() != 0 {
				t.Fatal("choosing the Kilo copy did not refresh discovery without opening a tool")
			}
		})
	}
}

func TestNativeManagedToolCodexCustomPathSaveFailureKeepsSelection(t *testing.T) {
	f := newNativeManagedToolsFixture(t, "codex", "en", image.Pt(1180, 1100))
	u := f.h.u
	a := u.agentsState()
	custom := filepath.Join(u.owner.dir, "custom", "Codex.app")
	u.setValue("clients-launch-app-path", custom)
	u.uxhSavePreferences()
	prefsPath := filepath.Join(u.owner.dir, "agent-preferences.json")
	if err := os.Rename(prefsPath, prefsPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(prefsPath, 0700); err != nil {
		t.Fatal(err)
	}
	u.managedToolsState().snapshot.Clients = map[string]managedToolStatus{"codex": {Client: "codex", Installed: true, Managed: true}}
	gets := f.recorder.gets.Load()
	u.useManagedToolCopy("codex")
	if u.value("clients-launch-app-path") != custom || a.Preferences.CodexAppPath != custom || u.managedToolsState().requestError == "" || f.recorder.gets.Load() != gets {
		t.Fatal("failed preference write changed selection or failed silently")
	}
}

func TestNativeManagedToolErrorsReconnectAndCancel(t *testing.T) {
	f := newNativeManagedToolsFixture(t, "codex-cli", "en", image.Pt(1180, 1000))
	h, u := f.h, f.h.u
	status := managedToolStatus{Client: "codex-cli", Busy: true, Phase: "downloading", Managed: true, Installed: true, InstalledVersion: "1.0.0", LatestVersion: "1.1.0"}
	f.setStatus(status)
	f.mu.Lock()
	f.failGet = true
	f.mu.Unlock()
	u.managedToolsState().snapshot = managedToolsSnapshot{Platform: "darwin", Clients: map[string]managedToolStatus{"codex-cli": status}}
	f.refresh(t)
	if u.managedToolsState().requestError == "" {
		t.Fatal("failed status refresh was silent")
	}
	f.mu.Lock()
	f.failGet = false
	f.mu.Unlock()
	h.reveal("Check versions", semantic.Button)
	h.click("Check versions", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+managedToolsStatusEndpoint] })
	h.frame()
	if u.managedToolsState().requestError != "" {
		t.Fatal("status could not reconnect while installation was pending")
	}
	if checks, _, _ := f.counts(); checks != 0 {
		t.Fatal("reconnecting restarted the operation")
	}
	h.reveal("Cancel installation", semantic.Button)
	h.click("Cancel installation", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["POST/api/tools/cancel"] })
	h.frame()
	f.mu.Lock()
	cancels := f.cancels
	f.mu.Unlock()
	if cancels != 1 || !nativeUpdateHasText(h, "The installation was cancelled. The previous installation is still available.") {
		t.Fatal("cancel did not preserve and explain the previous installation")
	}
	status.Busy, status.Phase, status.Error, status.PlanID, status.CanUpdate = false, "complete", "", "new-plan", true
	f.setStatus(status)
	f.refresh(t)
	u.prepareManagedTool("codex-cli")
	status.PlanID = "changed-plan"
	f.setStatus(status)
	f.refresh(t)
	u.installManagedTool("codex-cli")
	if _, installs, _ := f.counts(); installs != 0 {
		t.Fatal("stale plan confirmation reached the installer")
	}
}

func TestNativeManagedToolWindowsIsManual(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeManagedToolsFixture(t, "codex-cli", language, image.Pt(780, 700))
			h, u := f.h, f.h.u
			u.clientState().LaunchInfo.Platform = "windows"
			h.frame()
			if !nativeUpdateHasText(h, u.tr("Install and update this tool manually on Windows.", "Instala y actualiza esta herramienta manualmente en Windows.")) || nativeUpdateHasText(h, u.tr("Check versions", "Comprobar versiones")) {
				t.Fatal("Windows offered managed installation")
			}
			u.checkManagedTool("codex-cli")
			u.installManagedTool("codex-cli")
			if checks, installs, gets := f.counts(); checks != 0 || installs != 0 || gets != 0 {
				t.Fatal("Windows contacted the installer")
			}
		})
	}
}

func TestNativeManagedToolLinuxUnsupportedReasonAppearsOnce(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		for _, statusError := range []string{managedToolLinuxSandboxRequirement, "", "The download checksum does not match the official release. No installation was changed."} {
			f := newNativeManagedToolsFixture(t, "codex", language, image.Pt(780, 700))
			h, u := f.h, f.h.u
			u.managedToolsState().snapshot = managedToolsSnapshot{Platform: "linux", Clients: map[string]managedToolStatus{"codex": {Client: "codex", CheckedAt: 1, Phase: "failed", Error: statusError, UnsupportedReason: managedToolLinuxSandboxRequirement}}}
			h.frame()
			h.reveal(u.tr("How to install", "Cómo instalarlo"), semantic.Button)
			reason, count := nativeMessage(managedToolLinuxSandboxRequirement, language), 0
			for _, node := range h.nodes() {
				if node.Desc.Label == reason {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("Linux requirement appears %d times, want exactly once", count)
			}
			if statusError != "" && !nativeUpdateHasText(h, nativeMessage(statusError, language)) {
				t.Fatal("deduplicating the requirement hid a distinct installation error")
			}
		}
	}
}

func TestNativeManagedToolMissingRowIsReachable(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeAgentInstallationFixture(t, "codex-cli", language, image.Pt(780, 700), false, true)
			h, u := f.h, f.h.u
			name := uxhName("codex-cli")
			nativeRevealText(h, name)
			nativeClickNode(h, nativeAgentRowButton(h, name, u.tr("Install or update", "Instalar o actualizar")))
			if u.page != "clients" || u.client != "codex-cli" {
				t.Fatal("missing tool could not reach managed installation")
			}
			h.reveal(u.tr("Check versions", "Comprobar versiones"), semantic.Button)
			if f.recorder.prepares.Load() != 0 || f.recorder.launchRequests.Load() != 0 || f.recorder.count() != 0 {
				t.Fatal("opening installation prepared or launched a tool")
			}
		})
	}
}

func TestNativeManagedToolPreservesDetectedInitialChannel(t *testing.T) {
	f := newNativeManagedToolsFixture(t, "t3-code", "en", image.Pt(1180, 1000))
	h, u := f.h, f.h.u
	if u.value("tool:t3-code:channel") != "installed" {
		t.Fatal("an existing installation did not start with its detected channel")
	}
	h.reveal("Check versions", semantic.Button)
	h.click("Check versions", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["POST"+managedToolsCheckEndpoint] })
	f.mu.Lock()
	requestedChannel := f.channel
	f.mu.Unlock()
	if requestedChannel != "" {
		t.Fatalf("initial version check forced %q instead of detecting the installed channel", requestedChannel)
	}
	f.setStatus(managedToolStatus{Client: "t3-code", Installed: true, InstalledVersion: "1.0.0-nightly.1", InstalledChannel: "nightly", Channel: "nightly", Channels: managedToolChannels("t3-code"), LatestVersion: "1.0.0-nightly.2", CanInstall: true, PlanID: "nightly-plan", Phase: "complete"})
	f.refresh(t)
	if u.value("tool:t3-code:channel") != "nightly" {
		t.Fatal("detected Nightly was silently switched to another channel")
	}
	u.prepareManagedTool("t3-code")
	h.frame()
	if !nativeUpdateHasText(h, "Install T3 Code 1.0.0-nightly.2 · nightly?") || !nativeUpdateHasText(h, "Method: GitHub Releases") {
		t.Fatal("confirmation omitted the exact Nightly release and its source")
	}
}

func TestNativeManagedToolClaudeUnknownTrackIsExplicit(t *testing.T) {
	f := newNativeManagedToolsFixture(t, "claude", "en", image.Pt(1180, 1000))
	h, u := f.h, f.h.u
	if got := u.managedToolChannel("claude"); got != "stable" || u.value("tool:claude:channel") != "stable" {
		t.Fatalf("unknown external Claude track must default visibly to stable, got %q", got)
	}
	h.reveal("stable", semantic.Button)
	h.reveal("latest", semantic.Button)
	h.reveal("Check versions", semantic.Button)
	h.click("Check versions", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["POST"+managedToolsCheckEndpoint] })
	f.mu.Lock()
	requested := f.channel
	f.mu.Unlock()
	if requested != "stable" {
		t.Fatalf("unknown Claude track sent %q instead of the visible stable choice", requested)
	}
}

func TestNativeManagedSynaraCopyPersistsBeforeChangingSelection(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeManagedToolsFixture(t, "synara", lang, image.Pt(1180, 1600))
			u := f.h.u
			custom := syntheticSynaraExecutable(t, t.TempDir(), u.owner.launcher.platform)
			codex := cliPreferencesTestExecutable(t, "preserved Codex")
			postCLISelection(t, u.owner, map[string]any{"version": 1, "synaraExecutable": custom, "codexExecutable": codex}, 200)
			u.refreshCLIPreferencesAfter(nil)
			nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
			before, err := readAgentPreferences(u.owner.dir)
			if err != nil {
				t.Fatal(err)
			}
			u.managedToolsState().snapshot.Clients = map[string]managedToolStatus{"synara": {Client: "synara", Installed: true, Managed: true}}
			upstream, _ := url.Parse("http://" + u.owner.adminHost)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			gate, entered := make(chan struct{}), make(chan struct{}, 1)
			var mu sync.Mutex
			fail := true
			posts := []cliPreferences{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == nativeCLIPreferencesEndpoint && r.Method == http.MethodPost {
					var next cliPreferences
					if json.NewDecoder(r.Body).Decode(&next) != nil {
						jsonError(w, 400, "bad preferences")
						return
					}
					mu.Lock()
					posts = append(posts, next)
					failing := fail
					mu.Unlock()
					if failing {
						select {
						case entered <- struct{}{}:
						default:
						}
						select {
						case <-gate:
						case <-r.Context().Done():
							return
						}
						jsonError(w, 503, "Synthetic Synara selection persistence unavailable")
						return
					}
					data, _ := json.Marshal(next)
					r.Body = io.NopCloser(bytes.NewReader(data))
					r.ContentLength = int64(len(data))
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
				server.Close()
			})
			nativeTestWait(t, u, func() bool { return len(u.busy) == 0 })
			u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
			u.useManagedToolCopy("synara")
			nativeTestWait(t, u, func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			stored, err := readSettings(u.owner.dir)
			if err != nil || stored.SynaraExecutable != custom || u.value(nativeCLIPathField("synara")) != custom || u.clientState().CLIPreferences.Preferences.SynaraExecutable != custom || u.cliPreferencesReady() {
				t.Fatal("pending write changed the selected Synara", stored, err)
			}
			close(gate)
			nativeTestWait(t, u, func() bool { return !u.cliPreferencesBusy() })
			if u.clientState().CLIPreferences.Pending == nil || u.managedToolsState().requestError == "" || u.value(nativeCLIPathField("synara")) != custom {
				t.Fatal("failed write lost selection or retry state")
			}
			mu.Lock()
			fail = false
			mu.Unlock()
			u.saveCLIPreferences(*u.clientState().CLIPreferences.Pending)
			nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
			stored, err = readSettings(u.owner.dir)
			if err != nil || stored.SynaraExecutable != "" || stored.CodexCLIExecutable != codex || u.value(nativeCLIPathField("synara")) != "" {
				t.Fatal("confirmed choice failed to switch only Synara", stored, err)
			}
			after, err := readAgentPreferences(u.owner.dir)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("Synara selection changed projects or other agent preferences", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(posts) != 2 || posts[0] != posts[1] || posts[1].SynaraExecutable != "" || posts[1].CodexExecutable != codex {
				t.Fatal("retry changed the confirmed choice", posts)
			}
			if f.recorder.launchRequests.Load() != 0 || f.recorder.prepares.Load() != 0 || f.recorder.count() != 0 {
				t.Fatal("selection started a workspace")
			}
		})
	}
}
