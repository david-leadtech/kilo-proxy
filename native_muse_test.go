//go:build desktop

package main

import (
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

func postNativeMuseMode(t *testing.T, u *nativeUI, mode string) {
	t.Helper()
	w := adminRequest(u.owner, "clients/muse/preferences", `{"version":1,"connection":"`+mode+`"}`)
	if w.Code != 200 {
		t.Fatalf("Muse mode: %d %s", w.Code, w.Body.String())
	}
}

func nativeMuseTestUI(t *testing.T, language string) (*nativeUI, *nativeLaunchRecorder, string) {
	t.Helper()
	u := nativeTestUI(t)
	u.owner.mu.Lock()
	u.owner.apiKey, u.owner.config.OrgID, u.owner.config.Language = "", "", language
	u.owner.publishUIProjectionLocked()
	u.owner.mu.Unlock()
	u.language, u.authenticated, u.models = language, false, nil
	nativeSeedSharedForTest(t, u)
	binary := cliPreferencesTestExecutable(t, "Muse Code native")
	r := &nativeLaunchRecorder{}
	u.owner.launcher = &clientLaunchRuntime{
		platform: "macos", home: u.owner.editorTestRoot,
		resolve: func(client, custom string) (string, error) {
			if client == "muse" {
				return binary, nil
			}
			return "", errors.New("synthetic missing installation")
		},
		terminal: func() (bool, string) { return true, "" },
		start: func(plan clientLaunchPlan) error {
			if err := validateClientProcessPlan(plan); err != nil {
				return err
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			r.plans = append(r.plans, plan)
			return nil
		},
	}
	postNativeMuseMode(t, u, "native")
	u.client, u.page = "muse", "clients"
	u.detectLaunchers()
	u.refreshCLIPreferencesAfter(nil)
	u.refreshMusePreferencesAfter(nil)
	nativeTestWait(t, u, func() bool {
		return u.clientState().LaunchChecked && u.cliPreferencesReady() && u.musePreferencesReady()
	})
	return u, r, binary
}

func TestNativeMuseOpensWithNativeLoginWithoutKiloConnectionOrSharedModels(t *testing.T) {
	for _, size := range []image.Point{{1180, 1500}, {780, 1000}} {
		for _, language := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+language, func(t *testing.T) {
				u, recorder, binary := nativeMuseTestUI(t, language)
				project := t.TempDir()
				if err := u.uxhUseGlobalFolder(project); err != nil {
					t.Fatal(err)
				}
				before := u.owner.modelLibrary.snapshot().Revision
				if u.agentConnectionReady() || len(u.library.selection.Models) != 0 {
					t.Fatal("fixture unexpectedly has a Kilo connection or shared models")
				}
				state := u.uxhLaunchState("muse")
				if !state.canOpen || state.install != uxhInstalled || state.reason != "" {
					t.Fatalf("native Muse was blocked by Kilo setup: %+v", state)
				}
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				if !nativeUpdateHasText(h, u.tr("Official Muse catalog", "Catálogo oficial de Muse")) || !nativeUpdateHasText(h, u.tr("Muse Code · Meta login", "Muse Code · inicio de sesión de Meta")) {
					t.Fatal("Muse settings did not explain the native login and catalog")
				}
				for _, label := range []string{u.tr("Prepare without opening", "Preparar sin abrir"), u.tr("Export configuration", "Exportar configuración"), u.tr("Edit in Models", "Editar en Modelos")} {
					if nativeUpdateHasText(h, label) {
						t.Fatalf("Muse offered an unrelated Kilo profile operation: %s", label)
					}
				}
				scopeFound := false
				for _, node := range h.nodes() {
					if strings.TrimSpace(node.Desc.Label) == u.tr("· the general one", "· la general") {
						scopeFound = true
					}
				}
				if !scopeFound {
					t.Fatal("long folder lost its general-folder scope")
				}
				width := size.X - 40
				if size.X > 1000 {
					width = size.X - 300
				}
				var ops op.Ops
				gtx := layout.Context{Ops: &ops, Constraints: layout.Constraints{Max: image.Pt(width, 2000)}, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
				if got := u.uxhFolderRow("muse")(gtx).Size; got.Y > 200 {
					t.Fatal("long folder row lost its compact layout", got)
				}
				nativeGridCapture(t, h, "muse-native-"+fmtSize(size)+"-"+language)
				if size.X < 1000 {
					h.reveal(u.tr("Detect again", "Volver a detectar"), semantic.Button)
					nativeGridCapture(t, h, "muse-native-details-"+fmtSize(size)+"-"+language)
					h.reveal(u.tr("Open Muse Code", "Abrir Muse Code"), semantic.Button)
				}
				h.click(u.tr("Open Muse Code", "Abrir Muse Code"), semantic.Button)
				nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" && recorder.count() == 1 })
				recorder.mu.Lock()
				plan := recorder.plans[0]
				recorder.mu.Unlock()
				if plan.Client != "muse" || plan.Executable != binary || plan.Directory != project || plan.Env["MUSE_NO_AUTO_UPDATE"] != "1" {
					t.Fatal("Muse did not receive its native executable/project", plan)
				}
				for _, key := range []string{"KILO_LOCAL_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
					if _, present := plan.Env[key]; present {
						t.Fatalf("Muse received a Kilo profile environment key: %s", key)
					}
				}
				if !strings.Contains(strings.Join(plan.Unset, ","), "KILO_LOCAL_API_KEY") {
					t.Fatal("Muse did not clear the inherited local Kilo credential")
				}
				u.owner.mu.Lock()
				proxyStarted := u.owner.proxyServer != nil
				u.owner.mu.Unlock()
				if proxyStarted || u.owner.modelLibrary.snapshot().Revision != before {
					t.Fatal("native Muse started the proxy or rewrote shared models")
				}
				if u.agentsState().Preferences.LastOpened["muse"] == 0 {
					t.Fatal("successful native Muse opening was not remembered")
				}
			})
		}
	}
}

func TestNativeMuseRejectsPendingDraftExternalAndMissingExecutableSelections(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder, _ := nativeMuseTestUI(t, language)
			prefs := &u.clientState().CLIPreferences
			u.setValue(nativeCLIPathField("muse"), filepath.Join(t.TempDir(), "unsaved-muse"))
			if u.uxhLaunchState("muse").canOpen {
				t.Fatal("Muse draft enabled opening")
			}
			u.launchAgent("muse")
			if recorder.count() != 0 || u.clientState().Launching != "" {
				t.Fatal("Muse opened an unsaved executable selection")
			}
			u.acceptCLIPreferences(prefs.Preferences, true)
			next := prefs.Preferences
			prefs.Pending = &next
			u.launchAgent("muse")
			if recorder.count() != 0 || u.uxhLaunchState("muse").canOpen {
				t.Fatal("Muse opened an unconfirmed executable selection")
			}
			prefs.Pending = nil
			custom := cliPreferencesTestExecutable(t, "selected Muse")
			u.agentsState().T3Code.Info.Prepared = true
			postCLISelection(t, u.owner, cliPreferences{Version: 1, MuseExecutable: custom}, 200)
			u.launchAgent("muse")
			nativeTestWait(t, u, func() bool {
				return u.clientState().Launching == "" && !u.cliPreferencesBusy() && !u.busy["GET"+nativeLaunchEndpoint]
			})
			if recorder.count() != 0 || prefs.Preferences.MuseExecutable != custom || u.value(nativeCLIPathField("muse")) != custom || u.agentsState().T3Code.Info.Prepared {
				t.Fatal("external Muse selection was launched stale or not reflected in Settings", u.notice)
			}
			u.notice = ""
			u.launchAgent("muse")
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" && recorder.count() == 1 })
			recorder.mu.Lock()
			selected := recorder.plans[0].Executable
			recorder.mu.Unlock()
			if selected != custom {
				t.Fatal("explicit Muse installation did not reach the process boundary", selected)
			}
			if err := os.Remove(custom); err != nil {
				t.Fatal(err)
			}
			u.detectLaunchers()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeLaunchEndpoint] })
			if u.uxhLaunchState("muse").canOpen || u.clientState().LaunchInfo.Clients["muse"].Path != "" {
				t.Fatal("missing explicit Muse installation silently fell back to another executable")
			}
			u.launchAgent("muse")
			if recorder.count() != 1 {
				t.Fatal("missing explicit Muse installation opened the system installation")
			}
		})
	}
}

func TestNativeMuseProjectEditDuringExecutablePreflightBlocksLaunch(t *testing.T) {
	u, recorder, _ := nativeMuseTestUI(t, "en")
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nativeCLIPreferencesEndpoint {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
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
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	u.launchAgent("muse")
	nativeTestWait(t, u, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	u.setValue(agentProjectField("muse"), t.TempDir())
	close(gate)
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if recorder.count() != 0 || !strings.Contains(u.notice, "settings changed") {
		t.Fatal("Muse launched after its folder changed during executable confirmation", u.notice)
	}
}

func TestNativeMuseCliPathSettingsPreserveOtherToolsAndReturnToAutomatic(t *testing.T) {
	for _, size := range []image.Point{{1180, 1500}, {780, 1000}} {
		for _, language := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+language, func(t *testing.T) {
				u, _, _ := nativeMuseTestUI(t, language)
				codex, claude, muse := cliPreferencesTestExecutable(t, "custom Codex"), cliPreferencesTestExecutable(t, "custom Claude"), cliPreferencesTestExecutable(t, "custom Muse")
				postCLISelection(t, u.owner, cliPreferences{Version: 1, CodexExecutable: codex, ClaudeExecutable: claude}, 200)
				u.refreshCLIPreferencesAfter(nil)
				nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() })
				u.page, u.settingsTab = "settings", "cli-tools"
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				u.setValue(nativeCLIPathField("muse"), muse)
				h.frame()
				label := u.tr("Save path", "Guardar ruta") + " · Muse Code"
				h.reveal(label, semantic.Button)
				h.click(label, semantic.Button)
				nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
				if got := u.clientState().CLIPreferences.Preferences; got.MuseExecutable != muse || got.CodexExecutable != codex || got.ClaudeExecutable != claude {
					t.Fatal("saving the Muse installation changed another tool", got)
				}
				h.frame()
				nativeGridCapture(t, h, "muse-cli-paths-"+fmtSize(size)+"-"+language)
				label = u.tr("Automatic detection", "Detección automática") + " · Muse Code"
				h.reveal(label, semantic.Button)
				h.click(label, semantic.Button)
				nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
				if got := u.clientState().CLIPreferences.Preferences; got.MuseExecutable != "" || got.CodexExecutable != codex || got.ClaudeExecutable != claude {
					t.Fatal("automatic Muse detection changed another tool", got)
				}
			})
		}
	}
}

func nativeT3MuseOnlyTestUI(t *testing.T, language string, delay bool) (*nativeUI, *nativeLaunchRecorder) {
	t.Helper()
	u, recorder := nativeLaunchTestUI(t, "t3-code", delay, false)
	binary, err := u.owner.launchRuntime().resolve("t3-code", "")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := t3CodeOpenPackage(binary, "macos")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := archive.file.Name()
	files := map[string][]byte{}
	for _, path := range []string{"package.json", "apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs", "apps/server/dist/client/index.html", "apps/server/dist/client/assets/settings.providers-Fixture.js", "apps/server/dist/client/assets/utils-Fixture.js"} {
		files[path], err = archive.read(path, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = archive.file.Close()
	files["apps/server/dist/bin.mjs"] = append(files["apps/server/dist/bin.mjs"], []byte("\n"+t3CodeOptionalCapabilityFixture())...)
	writeT3CodeFixtureASAR(t, archivePath, files)
	u.owner.mu.Lock()
	prefs := normalizeT3CodePreferences(u.owner.config.T3Code)
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	prefs.Providers = []string{"muse"}
	u.owner.config.T3Code = prefs
	u.owner.apiKey, u.owner.config.OrgID, u.owner.config.Language = "", "", language
	u.owner.publishUIProjectionLocked()
	u.owner.mu.Unlock()
	u.authenticated, u.language, u.models = false, language, nil
	nativeSeedSharedForTest(t, u)
	u.refreshT3CodePreferences()
	u.detectLaunchers()
	nativeTestWait(t, u, func() bool {
		return u.agentsState().T3Code.PreferencesState.Loaded && !u.busy["GET"+nativeLaunchEndpoint]
	})
	return u, recorder
}

func TestNativeT3MuseOnlyWithoutKiloConnectionOrModelsAndTransition(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder := nativeT3MuseOnlyTestUI(t, language, false)
			before := u.owner.modelLibrary.snapshot().Revision
			if u.agentConnectionReady() || len(u.library.selection.Models) != 0 || !u.t3CodeNativeOnly() {
				t.Fatal("fixture unexpectedly has Kilo requirements configured")
			}
			if state := u.uxhLaunchState("t3-code"); !state.canOpen || state.reason != "" {
				t.Fatalf("native-only T3 is blocked by Kilo: %+v", state)
			}
			u.agentSetup("t3-code")
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
			h.frame()
			if !nativeUpdateHasText(h, u.tr("Native provider catalogs", "Catálogos nativos de los proveedores")) || nativeUpdateHasText(h, u.tr("Edit in Models", "Editar en Modelos")) {
				t.Fatal("native-only T3 shows the Kilo model library")
			}
			h.click(u.tr("Open T3 Code", "Abrir T3 Code"), semantic.Button)
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
			if recorder.count() != 1 || recorder.prepares.Load() != 1 || !u.t3CodePrepared(u.sharedClientSelection("t3-code")) {
				t.Fatalf("native-only T3 did not prepare/open: %s", u.notice)
			}
			recorder.mu.Lock()
			plan := recorder.plans[0]
			recorder.mu.Unlock()
			u.owner.mu.Lock()
			proxyStarted := u.owner.proxyServer != nil
			u.owner.mu.Unlock()
			if proxyStarted || !plan.skipProxy || u.owner.modelLibrary.snapshot().Revision != before {
				t.Fatal("native-only T3 started the gateway or rewrote shared models")
			}
			if entries, err := os.ReadDir(t3CodePaths(u.owner.dir).Secrets); err != nil || len(entries) != 0 {
				t.Fatal("native-only T3 wrote Kilo secrets", entries, err)
			}
			u.owner.mu.Lock()
			u.owner.config.T3Code.KiloCodex = true
			u.owner.publishUIProjectionLocked()
			u.owner.mu.Unlock()
			u.refreshT3CodePreferences()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeT3CodePreferencesEndpoint] })
			if u.t3CodeNativeOnly() || u.t3CodePrepared(u.sharedClientSelection("t3-code")) || u.uxhLaunchState("t3-code").canOpen {
				t.Fatal("switching to a Kilo account reused native-only readiness")
			}
			u.launchAgent("t3-code")
			if recorder.count() != 1 || recorder.prepares.Load() != 1 || u.notice == "" {
				t.Fatal("Kilo account launched without shared models")
			}
			u.models = nativeClientModelsForTest()
			nativeSeedSharedForTest(t, u, u.models[0])
			if state := u.uxhLaunchState("t3-code"); state.canOpen || !strings.Contains(state.reason, u.tr("Connect Kilo or ChatGPT", "Conecta Kilo o ChatGPT")) {
				t.Fatalf("Kilo account ignored its connection: %+v", state)
			}
		})
	}
}

func TestNativeT3MuseOnlyRejectsChangesDuringPreparation(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		for _, change := range []string{"preferences", "muse-path"} {
			t.Run(language+"/"+change, func(t *testing.T) {
				u, recorder := nativeT3MuseOnlyTestUI(t, language, true)
				u.launchAgent("t3-code")
				recorder.waitForPrepare(t, u)
				if change == "preferences" {
					u.agentsState().T3Code.PreferencesState.Preferences.KiloClaude = true
				} else {
					u.setValue(nativeCLIPathField("muse"), filepath.Join(t.TempDir(), "new-muse-draft"))
				}
				recorder.unblock()
				nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
				if recorder.count() != 0 || u.notice == "" {
					t.Fatal("native-only T3 launched with stale settings")
				}
			})
		}
	}
}

func TestNativeMuseKiloReadinessSnapshotsAndNativeRecovery(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder, binary := nativeMuseTestUI(t, language)
			c := u.clientState()
			prefs := nativeMusePreferences{Version: 1, Connection: "kilo"}
			c.Muse = nativeMuseState{Preferences: prefs, Requested: true, Loaded: true, ProfileRequested: true, ProfileLoaded: true, ProfilePath: binary, Info: nativeMuseInfo{Preferences: prefs, KiloSupported: true, ResolvedExecutable: binary}}
			if state := u.uxhLaunchState("muse"); state.canOpen || !strings.Contains(state.reason, u.tr("Connect Kilo or ChatGPT", "Conecta Kilo o ChatGPT")) {
				t.Fatalf("Muse Kilo bypassed connection: %+v", state)
			}
			u.owner.mu.Lock()
			u.owner.apiKey, u.owner.config.OrgID = "synthetic-kilo-key", "synthetic-org"
			u.owner.publishUIProjectionLocked()
			u.owner.mu.Unlock()
			if state := u.uxhLaunchState("muse"); state.canOpen || !strings.Contains(state.reason, u.tr("save shared models", "guarda modelos compartidos")) {
				t.Fatalf("Muse Kilo bypassed saved Models: %+v", state)
			}
			model := modelInfo{ID: "vendor/exact", Name: "Exact shared model"}
			u.models = []modelInfo{model}
			nativeSeedSharedForTest(t, u, model)
			if state := u.uxhLaunchState("muse"); !state.canOpen {
				t.Fatalf("confirmed Muse Kilo remained blocked: %+v", state)
			}
			c.Launching = "muse"
			if reason := u.museReadinessReason(); reason != "" {
				t.Fatal("Muse blocked its own preparation", reason)
			}
			u.busy["POST/api/config"] = true
			if reason := u.museReadinessReason(); reason == "" {
				t.Fatal("Muse ignored a concurrent connection change during preparation")
			}
			delete(u.busy, "POST/api/config")
			c.Launching = "t3-code"
			if reason := u.museReadinessReason(); reason == "" {
				t.Fatal("Muse ignored another active launcher")
			}
			c.Launching = ""
			c.Muse.Info.Library = func() *modelLibrary { value := u.modelLibraryValue(); return &value }()
			c.Muse.Info.EffectiveModels = []nativeT3CodeModelSummary{{ID: model.ID, Name: model.Name}}
			initial := u.launchSettingsFingerprint("muse", "")
			c.Muse.Pending = &nativeMusePreferences{Version: 1, Connection: "native"}
			if u.uxhLaunchState("muse").canOpen || initial == u.launchSettingsFingerprint("muse", "") {
				t.Fatal("pending connection did not invalidate Kilo opening")
			}
			c.Muse.Pending = nil
			c.Muse.Info.KiloSupported = false
			if state := u.uxhLaunchState("muse"); state.canOpen || state.reason == "" {
				t.Fatal("missing Kilo capability was launchable")
			}
			c.Muse.Info.KiloSupported = true
			for _, size := range []image.Point{{1180, 1500}, {780, 1000}} {
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				if !nativeUpdateHasText(h, "Kilo Proxy · Muse Code") {
					t.Fatal("Kilo account label absent")
				}
				nativeGridCapture(t, h, "muse-kilo-"+fmtSize(size)+"-"+language)
				if size.X == 780 {
					h.reveal(u.tr("Detect again", "Volver a detectar"), semantic.Button)
					nativeGridCapture(t, h, "muse-kilo-details-"+fmtSize(size)+"-"+language)
				}
			}
			c.Muse.Preferences = nativeMusePreferences{Version: 1, Connection: "native"}
			c.Muse.Info.Preferences = c.Muse.Preferences
			u.owner.mu.Lock()
			u.owner.apiKey, u.owner.config.OrgID = "", ""
			u.owner.publishUIProjectionLocked()
			u.owner.mu.Unlock()
			nativeSeedSharedForTest(t, u)
			if !u.museNative() || !u.uxhLaunchState("muse").canOpen {
				t.Fatal("Meta recovery retained Kilo connection/library gate")
			}
			if initial == u.launchSettingsFingerprint("muse", "") {
				t.Fatal("mode transition retained Kilo fingerprint")
			}
			if recorder.count() != 0 {
				t.Fatal("readiness/layout spawned a Muse process")
			}
		})
	}
}

func nativeMuseKiloTestUI(t *testing.T, language string) (*nativeUI, *nativeLaunchRecorder, string, *atomic.Int32) {
	t.Helper()
	u, recorder, binary := nativeMuseTestUI(t, language)
	u.owner.mu.Lock()
	u.owner.apiKey, u.owner.config.OrgID = "synthetic-kilo-personal-key", "e2e-team"
	u.owner.publishUIProjectionLocked()
	u.owner.mu.Unlock()
	probes := &atomic.Int32{}
	u.owner.museVersionProbe = func(path string) (string, error) {
		probes.Add(1)
		if path != binary {
			t.Error("Muse preparation replaced the selected executable", path)
		}
		return "1.4.6-R6000.1", nil
	}
	model := modelInfo{ID: "vendor/one", Name: "Exact shared model", ContextWindow: 64000, MaxOutputTokens: 4000}
	u.models = []modelInfo{model}
	nativeSeedSharedForTest(t, u, model)
	u.saveMusePreferences(nativeMusePreferences{Version: 1, Connection: "kilo"})
	nativeTestWait(t, u, func() bool {
		return u.musePreferencesReady() && u.clientState().Muse.ProfileLoaded && !u.busy["GET"+nativeMuseEndpoint]
	})
	if reason := u.museReadinessReason(); reason != "" {
		t.Fatal("Kilo fixture did not reach confirmed readiness", reason)
	}
	if probes.Load() != 0 {
		t.Fatal("reading Muse readiness executed a version probe")
	}
	return u, recorder, binary, probes
}

func TestNativeMuseKiloLaunchPreparesExactSelectionAndExecutable(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder, binary, probes := nativeMuseKiloTestUI(t, language)
			project := t.TempDir()
			if err := u.uxhUseGlobalFolder(project); err != nil {
				t.Fatal(err)
			}
			before := u.owner.modelLibrary.snapshot()
			u.launchAgent("muse")
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" && recorder.count() == 1 })
			recorder.mu.Lock()
			plan := recorder.plans[0]
			recorder.mu.Unlock()
			if plan.Client != "muse" || plan.Executable != binary || plan.Directory != project || !reflect.DeepEqual(plan.Env, museKiloEnvironment(museTerminalHome(u.owner.dir))) {
				t.Fatal("Muse Kilo launch lost its selected executable, project or private profile", plan)
			}
			if probes.Load() != 1 || !u.clientState().Muse.Info.Prepared || !reflect.DeepEqual(*u.clientState().Muse.Info.Library, before.Library) {
				t.Fatal("native launch did not confirm the exact prepared Kilo selection", probes.Load(), u.clientState().Muse.Info)
			}
			if after := u.owner.modelLibrary.snapshot(); after.Revision != before.Revision || !reflect.DeepEqual(after.Library, before.Library) {
				t.Fatal("launch rewrote the shared Models selection")
			}
			u.owner.mu.Lock()
			started := u.owner.proxyServer != nil
			u.owner.mu.Unlock()
			if !started || u.agentsState().Preferences.LastOpened["muse"] == 0 {
				t.Fatal("Kilo Muse did not start its own gateway or remember successful opening")
			}
		})
	}
}

func TestNativeMuseKiloRejectsExternalModeChangeDuringPreparation(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder, _, probes := nativeMuseKiloTestUI(t, language)
			upstream, _ := url.Parse("http://" + u.owner.adminHost)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			gate, entered := make(chan struct{}), make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == nativeMuseEndpoint {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
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
			u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
			u.launchAgent("muse")
			nativeTestWait(t, u, func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			postNativeMuseMode(t, u, "native")
			close(gate)
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
			if recorder.count() != 0 || probes.Load() != 0 || u.notice != nativeMessage("The Muse Code connection changed. Reload its settings before preparing it.", language) {
				t.Fatal("Muse launched a stale Kilo mode or probed it before checking the current connection", u.notice, probes.Load())
			}
			u.owner.mu.Lock()
			started := u.owner.proxyServer != nil
			u.owner.mu.Unlock()
			if started {
				t.Fatal("stale Muse preparation started the gateway")
			}
		})
	}
}
