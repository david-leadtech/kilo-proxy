//go:build desktop

package main

import (
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeOmnigentPreferenceContract(t *testing.T) {
	p := nativeOmnigentPreferences{Version: 1, Codex: true, Claude: true, CodexConnection: "kilo", ClaudeConnection: "kilo", CodexModels: "shared", ClaudeModels: "shared"}
	if !validNativeOmnigentPreferences(p) || !nativeOmnigentUsesKilo(p) {
		t.Fatal("valid Kilo preferences rejected")
	}
	raw, _ := json.Marshal(p)
	for _, key := range []string{"codex", "claude"} {
		for _, invalid := range []string{"null", "\"true\"", "1"} {
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			fields[key] = json.RawMessage(invalid)
			bad, _ := json.Marshal(fields)
			var decoded nativeOmnigentPreferences
			if json.Unmarshal(bad, &decoded) == nil {
				t.Fatalf("accepted %s=%s", key, invalid)
			}
		}
	}
	p.Claude = false
	p.CodexConnection = "native"
	if nativeOmnigentUsesKilo(p) {
		t.Fatal("disabled Kilo connection blocked native account")
	}
	p.ClaudeConnection = ""
	if validNativeOmnigentPreferences(p) {
		t.Fatal("missing connection was silently accepted")
	}
}

func TestNativeOmnigentSavedLegacyLibraryRoundTrip(t *testing.T) {
	a := omnigentTestApp(t)
	library := omnigentLegacySelectionFixture()
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	selection := &nativeClientSelection{Initial: library.DefaultModel}
	for _, item := range library.Models {
		selection.Models = append(selection.Models, contextChoiceFromLibrary(item, modelInfo{ID: item.ID}))
	}
	payload, err := nativeClientPayload("omnigent", selection)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(payload)
	w := adminRequest(a, "clients/omnigent", string(raw))
	if w.Code != 200 {
		t.Fatalf("real native payload rejected the unchanged saved library: %d %s", w.Code, w.Body.String())
	}
	var confirmed nativeOmnigentInfo
	if json.Unmarshal(w.Body.Bytes(), &confirmed) != nil || !confirmed.Prepared || confirmed.Library == nil || !omnigentSameModelSelection(*confirmed.Library, payload.(map[string]any)["library"].(modelLibrary)) {
		t.Fatal("native confirmation failed after successful preparation")
	}
}

func TestNativeOmnigentWheelSaveRecoveryAndStop(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, _ := nativeLaunchTestUI(t, "codex-cli", false, false)
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
			u.setLanguage(language)
			nativeTestWait(t, u, func() bool {
				return u.language == language && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
			})
			original, _ := url.Parse("http://" + u.owner.adminHost)
			proxy := httputil.NewSingleHostReverseProxy(original)
			var mu sync.Mutex
			prefs := nativeOmnigentPreferences{Version: 1, Codex: true, Claude: true, CodexConnection: "kilo", ClaudeConnection: "kilo", CodexModels: "shared", ClaudeModels: "shared"}
			posts, stops := 0, 0
			fail, running := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != nativeOmnigentPreferencesEndpoint && r.URL.Path != "/api/clients/omnigent" && r.URL.Path != "/api/clients/omnigent/stop" && r.URL.Path != nativeLaunchEndpoint {
					proxy.ServeHTTP(w, r)
					return
				}
				if !secureEqual(r.Header.Get("Authorization"), "Bearer "+u.owner.adminToken) {
					t.Error("missing local admin auth")
					jsonError(w, 401, "missing auth")
					return
				}
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case nativeLaunchEndpoint:
					jsonResponse(w, 200, nativeLaunchInfo{Platform: "macos", Clients: map[string]nativeLaunchClient{"omnigent": {Available: true, Installed: true, Name: "Omnigent · Kilo", Kind: "desktop", Path: "/synthetic/omnigent"}}})
				case nativeOmnigentPreferencesEndpoint:
					if r.Method == http.MethodPost {
						posts++
						var next nativeOmnigentPreferences
						if json.NewDecoder(r.Body).Decode(&next) != nil {
							jsonError(w, 400, "bad preferences")
							return
						}
						if fail {
							jsonError(w, 503, "synthetic disk error")
							return
						}
						prefs = next
					}
					jsonResponse(w, 200, nativeOmnigentPreferencesResponse{Preferences: prefs, RequiresPreparation: posts > 0})
				case "/api/clients/omnigent/stop":
					if r.Method != http.MethodPost {
						t.Error("unexpected stop method")
					}
					stops++
					running = false
					jsonResponse(w, 200, map[string]bool{"ok": true})
				default:
					if r.Method != http.MethodGet {
						t.Error("unexpected preparation while testing preferences")
					}
					jsonResponse(w, 200, nativeOmnigentInfo{Preferences: prefs, Running: running, Fingerprint: "synthetic-profile"})
				}
			}))
			t.Cleanup(server.Close)
			u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
			u.agentSetup("omnigent")
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1000), now: time.Now()}
			h.frame()
			nativeTestWait(t, u, func() bool {
				return u.agentsState().Omnigent.PreferencesState.Loaded && u.agentsState().Omnigent.Loaded && !u.busy["GET/api/clients/omnigent"]
			})
			for _, size := range []image.Point{image.Pt(1180, 1000), image.Pt(780, 820)} {
				h.size = size
				h.frame()
				h.reveal(u.tr("Own account", "Cuenta propia"), semantic.RadioButton)
				nativeGridCapture(t, h, "native-omnigent-preferences-"+language+"-"+strconv.Itoa(size.X))
			}
			next := u.agentsState().Omnigent.PreferencesState.Preferences
			next.Claude = false
			next.CodexConnection = "native"
			mu.Lock()
			fail = true
			mu.Unlock()
			u.saveOmnigentPreferences(next)
			nativeTestWait(t, u, func() bool { return !u.omnigentPreferencesBusy() })
			state := &u.agentsState().Omnigent.PreferencesState
			if state.Pending == nil || state.Error == "" || u.omnigentNativeOnly() || u.uxhLaunchState("omnigent").canOpen {
				t.Fatal("failed save became confirmed or launchable")
			}
			mu.Lock()
			fail = false
			mu.Unlock()
			u.saveOmnigentPreferences(*state.Pending)
			nativeTestWait(t, u, func() bool {
				return !u.omnigentPreferencesBusy() && state.Pending == nil && !u.busy["GET/api/clients/omnigent"] && !u.busy["GET"+nativeLaunchEndpoint] && u.clientState().LaunchChecked && !u.clientState().LaunchRefreshPending
			})
			if state.Preferences != next || !u.omnigentNativeOnly() {
				t.Fatal("confirmed native settings not applied")
			}
			mu.Lock()
			running = true
			mu.Unlock()
			u.refreshOmnigentProfile()
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/omnigent"] })
			h.frame()
			h.reveal(u.tr("Stop Omnigent · Kilo", "Detener Omnigent · Kilo"), semantic.Button)
			u.stopOmnigent()
			nativeTestWait(t, u, func() bool {
				return !u.busy["POST/api/clients/omnigent/stop"] && !u.busy["GET/api/clients/omnigent"] && !u.busy["GET"+nativeOmnigentPreferencesEndpoint] && !u.busy["GET"+nativeLaunchEndpoint]
			})
			mu.Lock()
			defer mu.Unlock()
			if stops != 1 || posts != 2 || running || u.agentsState().Omnigent.Info.Running {
				t.Fatalf("stop/save scope mismatch: stops=%d posts=%d running=%v", stops, posts, running)
			}
		})
	}
}

func TestNativeOmnigentMessagesTranslateWithoutChangingDetails(t *testing.T) {
	for _, english := range []string{
		"Choose the official Omnigent 0.18.0 Python CLI installation. Its interpreter and source contract must be available; standalone desktop launchers are not supported.",
		"Stop Omnigent · Kilo before changing its connections or prepared models.",
		"Omnigent is prepared with your selected connections and models.",
		"The private Omnigent workspace could not start. Check the selected installation and open it again.",
		"Omnigent requires the selected native Codex CLI or its matching official npm package. Choose its native executable or repair that selected installation; no other Codex installation will be used.",
	} {
		spanish := nativeMessage(english, "es")
		if spanish == english || nativeMessage(spanish, "en") != english {
			t.Fatalf("missing exact translation: %q", english)
		}
	}
	if raw := "unrecognized diagnostic /synthetic/path"; nativeMessage(raw, "es") != raw {
		t.Fatal("unknown diagnostic changed")
	}
}

func TestNativeOmnigentLaunchStatusRefreshAndBound(t *testing.T) {
	u, _ := nativeLaunchTestUI(t, "codex-cli", false, false)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	original, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(original)
	var mu sync.Mutex
	running, message, gets, stops := false, "", 0, 0
	var statusGate <-chan struct{}
	var statusSeen chan struct{}
	prefs := nativeOmnigentPreferences{Version: 1, Codex: true, CodexConnection: "native", ClaudeConnection: "native", CodexModels: "shared", ClaudeModels: "shared"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/clients/omnigent" && r.URL.Path != nativeOmnigentPreferencesEndpoint && r.URL.Path != "/api/clients/omnigent/stop" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/clients/omnigent/stop" {
			if r.Method != http.MethodPost || !secureEqual(r.Header.Get("Authorization"), "Bearer "+u.owner.adminToken) {
				t.Error("unexpected stop mutation")
			}
			mu.Lock()
			stops++
			running = false
			mu.Unlock()
			jsonResponse(w, 200, map[string]bool{"ok": true})
			return
		}
		if r.Method != http.MethodGet || !secureEqual(r.Header.Get("Authorization"), "Bearer "+u.owner.adminToken) {
			t.Error("status observation must be an authenticated GET")
			jsonError(w, 400, "unexpected status mutation")
			return
		}
		mu.Lock()
		if r.URL.Path == nativeOmnigentPreferencesEndpoint {
			mu.Unlock()
			jsonResponse(w, 200, nativeOmnigentPreferencesResponse{Preferences: prefs})
			return
		}
		gets++
		info := nativeOmnigentInfo{Preferences: prefs, Prepared: true, Running: running, Fingerprint: "synthetic-profile", Message: message}
		gate, seen := statusGate, statusSeen
		statusSeen = nil
		mu.Unlock()
		if seen != nil {
			close(seen)
		}
		if gate != nil {
			<-gate
		}
		jsonResponse(w, 200, info)
	}))
	t.Cleanup(server.Close)
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	u.agentSetup("omnigent")
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1000), now: time.Now()}
	h.frame()
	nativeTestWait(t, u, func() bool {
		return u.agentsState().Omnigent.Loaded && u.agentsState().Omnigent.PreferencesState.Loaded && !u.busy["GET/api/clients/omnigent"]
	})
	mu.Lock()
	running = true
	mu.Unlock()
	u.omnigentLaunchConfirmed()
	nativeTestWait(t, u, func() bool { return u.agentsState().Omnigent.Info.Running && !u.busy["GET/api/clients/omnigent"] })
	h.frame()
	h.reveal("Stop Omnigent · Kilo", semantic.Button)
	_ = h.target("Stop Omnigent · Kilo", semantic.Button)
	s := &u.agentsState().Omnigent
	if s.WatchUntil.IsZero() {
		t.Fatal("launch acknowledgement did not observe startup status")
	}
	gate, seen := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(unblock)
	mu.Lock()
	statusGate, statusSeen = gate, seen
	mu.Unlock()
	u.pollOmnigentLaunchStatus(s.WatchNext.Add(time.Second))
	select {
	case <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("slow status read did not reach the fixture")
	}
	h.frame()
	h.reveal("Stop Omnigent · Kilo", semantic.Button)
	h.click("Stop Omnigent · Kilo", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.busy["POST/api/clients/omnigent/stop"] && s.WatchUntil.IsZero() })
	mu.Lock()
	if stops != 1 {
		t.Error("slow read blocked or duplicated Stop")
	}
	statusGate = nil
	mu.Unlock()
	unblock()
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/omnigent"] })
	mu.Lock()
	running = true
	mu.Unlock()
	u.omnigentLaunchConfirmed()
	nativeTestWait(t, u, func() bool { return s.Info.Running && !u.busy["GET/api/clients/omnigent"] })
	mu.Lock()
	running, message = false, "The private Omnigent workspace could not start. Check the selected installation and open it again."
	mu.Unlock()
	u.pollOmnigentLaunchStatus(s.WatchNext.Add(time.Second))
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/omnigent"] && u.agentsState().Omnigent.Info.Message != "" })
	if s.Info.Running || !s.WatchUntil.IsZero() {
		t.Fatal("delayed startup failure was not reflected and stopped")
	}
	u.omnigentLaunchConfirmed()
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/omnigent"] })
	s.WatchUntil = time.Now().Add(-time.Second)
	mu.Lock()
	before := gets
	mu.Unlock()
	u.pollOmnigentLaunchStatus(time.Now())
	if !s.WatchUntil.IsZero() || u.busy["GET/api/clients/omnigent"] {
		t.Fatal("expired startup observation sent another request")
	}
	mu.Lock()
	defer mu.Unlock()
	if gets != before {
		t.Fatal("expired startup observation was not bounded")
	}
}
