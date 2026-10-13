//go:build desktop

package main

import (
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

type nativeT3PreferencesFixture struct {
	u                 *nativeUI
	h                 *nativePointerHarness
	mu                sync.Mutex
	response          nativeT3CodePreferencesResponse
	posts             []nativeT3CodePreferences
	failGet, failPost bool
	postGate          chan struct{}
	profileGate       chan struct{}
	profileEntered    chan struct{}
	profile           nativeT3CodeInfo
	profilePosts      int
}

func newNativeT3PreferencesFixture(t *testing.T, language string) *nativeT3PreferencesFixture {
	t.Helper()
	u, _ := nativeLaunchTestUI(t, "t3-code", false, false)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	u.setLanguage(language)
	nativeTestWait(t, u, func() bool {
		return u.language == language && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
	})
	u.agentSetup("t3-code")
	var prefs nativeT3CodePreferences
	data, _ := json.Marshal(normalizeT3CodePreferences(t3CodePreferences{}))
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatal(err)
	}
	f := &nativeT3PreferencesFixture{u: u, response: nativeT3CodePreferencesResponse{Preferences: prefs, SupportedProviders: []nativeT3CodeProvider{{"cursor", "Cursor"}, {"opencode", "OpenCode"}, {"muse", "Muse Code"}}, OpenCode2Supported: true, RequiresPreparation: true}}
	upstream, err := url.Parse("http://" + u.owner.adminHost)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/clients/t3-code" && r.Method == http.MethodPost {
			f.mu.Lock()
			f.profilePosts++
			f.mu.Unlock()
		}
		if r.URL.Path == "/api/clients/t3-code" && r.Method == http.MethodGet {
			f.mu.Lock()
			gate, entered, profile := f.profileGate, f.profileEntered, f.profile
			f.mu.Unlock()
			if gate != nil {
				select {
				case entered <- struct{}{}:
				default:
				}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				jsonResponse(w, 200, profile)
				return
			}
		}
		if r.URL.Path != nativeT3CodePreferencesEndpoint {
			proxy.ServeHTTP(w, r)
			return
		}
		if !secureEqual(r.Header.Get("Authorization"), "Bearer "+u.owner.adminToken) {
			t.Error("settings request omitted admin auth")
			jsonError(w, 401, "missing auth")
			return
		}
		f.mu.Lock()
		fail, gate := f.failGet, (chan struct{})(nil)
		if r.Method == http.MethodPost {
			var next nativeT3CodePreferences
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				f.mu.Unlock()
				jsonError(w, 400, "bad settings")
				return
			}
			f.posts = append(f.posts, next)
			fail, gate = f.failPost, f.postGate
			if !fail {
				f.response.Preferences = next
			}
		}
		response := f.response
		f.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if fail {
			jsonError(w, 503, "Synthetic preferences unavailable")
			return
		}
		jsonResponse(w, 200, response)
	}))
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(server.Close)
	f.h = &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1000), now: time.Now()}
	f.h.frame()
	// Opening the harness starts profile and preference reads together. Finish
	// both before a test installs its own held profile response.
	nativeTestWait(t, u, func() bool {
		state := &u.agentsState().T3Code
		return state.PreferencesState.Loaded && state.Loaded && !u.busy["GET/api/clients/t3-code"]
	})
	f.h.frame()
	return f
}
func (f *nativeT3PreferencesFixture) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}
func (f *nativeT3PreferencesFixture) lastPost() nativeT3CodePreferences {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[len(f.posts)-1]
}

func TestNativeT3CodePreferencesSavePendingFailureAndRetry(t *testing.T) {
	f := newNativeT3PreferencesFixture(t, "en")
	u, h := f.u, f.h
	state := &u.agentsState().T3Code.PreferencesState
	before := state.Preferences
	f.mu.Lock()
	f.failPost = true
	f.postGate = make(chan struct{})
	gate := f.postGate
	f.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	h.reveal("Codex", semantic.Switch)
	h.click("Codex", semantic.Switch)
	nativeTestWait(t, u, func() bool { return f.postCount() == 1 })
	if !u.t3CodePreferencesBusy() || state.Pending == nil || !nativeT3CodePreferencesEqual(state.Preferences, before) {
		t.Fatal("pending write replaced confirmed selection")
	}
	if u.uxhLaunchState("t3-code").canOpen {
		t.Fatal("Open remains enabled while preferences are unconfirmed")
	}
	h.frame()
	for _, node := range h.nodes() {
		if node.Desc.Class == semantic.Switch && node.Desc.Label == "Codex" && node.Desc.Selected {
			t.Fatal("pending checkbox jumped back before confirmation")
		}
	}
	for _, node := range h.nodes() {
		if node.Desc.Class == semantic.Switch && node.Desc.Label == "Claude" && !node.Desc.Disabled {
			t.Fatal("another change is enabled during save")
		}
	}
	finished := false
	var prepareErr error
	u.prepareClientAfter("t3-code", func(err error) { finished, prepareErr = true, err })
	nativeTestWait(t, u, func() bool { return finished })
	if prepareErr == nil {
		t.Fatal("unconfirmed settings allowed preparing")
	}
	f.mu.Lock()
	profilePosts := f.profilePosts
	f.mu.Unlock()
	if profilePosts != 0 || !u.t3CodePreferencesBusy() || state.Pending == nil {
		t.Fatal("pending settings reached profile preparation or lost their gate")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.t3CodePreferencesBusy() })
	if state.Error == "" || state.Pending == nil || !nativeT3CodePreferencesEqual(state.Preferences, before) {
		t.Fatal("failed save was accepted or lost its retry policy")
	}
	if u.uxhLaunchState("t3-code").canOpen {
		t.Fatal("Open remains enabled after a failed preferences save")
	}
	f.mu.Lock()
	f.failPost = false
	f.postGate = nil
	f.mu.Unlock()
	h.frame()
	h.reveal("Retry saving", semantic.Button)
	h.click("Retry saving", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.t3CodePreferencesBusy() && state.Pending == nil })
	if f.postCount() != 2 || state.Preferences.Codex || !state.Preferences.Claude || !state.RequiresPreparation || state.Error != "" {
		t.Fatalf("retry did not confirm exact change: %+v", state)
	}
	expected := before
	expected.Codex = false
	if !nativeT3CodePreferencesEqual(f.lastPost(), expected) {
		t.Fatal("retry changed unrelated choices")
	}
}

func TestNativeT3CodePreferencesCapabilitiesAndOpenCodeVersion(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeT3PreferencesFixture(t, lang)
			u, h := f.u, f.h
			state := &u.agentsState().T3Code.PreferencesState
			h.reveal("Codex", semantic.Switch)
			h.target("Codex", semantic.Switch)
			h.target("Claude", semantic.Switch)
			labels := ""
			for _, node := range h.nodes() {
				labels += node.Desc.Label + "\n"
			}
			if strings.Contains(labels, "· Normal") || strings.Contains(labels, "Grok") {
				t.Fatal("duplicates or unsupported providers exposed")
			}
			nativeGridCapture(t, h, "native-t3-preferences-accounts-"+lang)
			h.reveal("OpenCode", semantic.Switch)
			h.click("OpenCode", semantic.Switch)
			nativeTestWait(t, u, func() bool { return f.postCount() == 1 && !u.t3CodePreferencesBusy() && state.Pending == nil })
			h.frame()
			h.reveal("OpenCode 2", semantic.RadioButton)
			h.click("OpenCode 2", semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 2 && !u.t3CodePreferencesBusy() && state.Pending == nil })
			if !state.Preferences.OpenCode2 || !reflect.DeepEqual(state.Preferences.Providers, []string{"opencode"}) {
				t.Fatal("OpenCode 2 was not selected exclusively")
			}
			h.frame()
			nativeGridCapture(t, h, "native-t3-preferences-opencode-"+lang)
			h.reveal("OpenCode", semantic.Switch)
			h.click("OpenCode", semantic.Switch)
			nativeTestWait(t, u, func() bool { return f.postCount() == 3 && !u.t3CodePreferencesBusy() && state.Pending == nil })
			if state.Preferences.OpenCode2 || len(state.Preferences.Providers) != 0 {
				t.Fatal("disabling OpenCode retained second runtime")
			}
		})
	}
}

func TestNativeT3CodePreferencesFailedLoadAndLateResponse(t *testing.T) {
	f := newNativeT3PreferencesFixture(t, "en")
	u := f.u
	s := &u.agentsState().T3Code.PreferencesState
	f.mu.Lock()
	f.failGet = true
	f.mu.Unlock()
	s.Loaded = false
	u.refreshT3CodePreferences()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeT3CodePreferencesEndpoint] })
	if s.Loaded || s.Error == "" {
		t.Fatal("unreadable preferences appeared loaded")
	}
	f.mu.Lock()
	f.failGet = false
	f.mu.Unlock()
	u.refreshT3CodePreferences()
	nativeTestWait(t, u, func() bool { return s.Loaded })
	old := s.Preferences
	u.refreshT3CodePreferences()
	s.Generation++ // A newer settings operation supersedes this GET.
	s.Preferences.Codex = false
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeT3CodePreferencesEndpoint] })
	if s.Preferences.Codex == old.Codex {
		t.Fatal("late GET overwrote newer preference state")
	}
}

func TestNativeT3CodePreferencesCanDisableChoicesUnavailableInNewBuild(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeT3PreferencesFixture(t, lang)
			u, h := f.u, f.h
			f.mu.Lock()
			f.response.Preferences.Providers = []string{"cursor", "opencode"}
			f.response.Preferences.OpenCode2 = true
			f.response.SupportedProviders = []nativeT3CodeProvider{{"opencode", "OpenCode"}, {"unknown", "Unknown provider"}}
			f.response.OpenCode2Supported = false
			f.mu.Unlock()
			u.refreshT3CodePreferences()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeT3CodePreferencesEndpoint] })
			h.frame()
			cursor := u.tr("Cursor · unavailable in this build", "Cursor · no disponible en esta compilación")
			h.reveal(cursor, semantic.Switch)
			if !h.selected(cursor, semantic.Switch) {
				t.Fatal("saved unsupported choice is hidden or cannot be switched off")
			}
			h.reveal("OpenCode 1", semantic.RadioButton)
			for _, node := range h.nodes() {
				if node.Desc.Label == "Unknown provider" {
					t.Fatal("unknown provider was offered")
				}
			}
			state := &u.agentsState().T3Code.PreferencesState
			if !state.Preferences.OpenCode2 || state.OpenCode2Supported {
				t.Fatal("saved OpenCode 2 choice was removed or falsely reported as supported")
			}
			nativeGridCapture(t, h, "native-t3-preferences-unavailable-"+lang)
			h.reveal(cursor, semantic.Switch)
			h.click(cursor, semantic.Switch)
			nativeTestWait(t, u, func() bool { return f.postCount() == 1 && !u.t3CodePreferencesBusy() })
			h.frame()
			h.reveal("OpenCode 1", semantic.RadioButton)
			h.click("OpenCode 1", semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 2 && !u.t3CodePreferencesBusy() })
			p := u.agentsState().T3Code.PreferencesState.Preferences
			if p.OpenCode2 || !reflect.DeepEqual(p.Providers, []string{"opencode"}) {
				t.Fatalf("unavailable choices did not clear independently: %+v", p)
			}
			h.frame()
			for _, node := range h.nodes() {
				if node.Desc.Label == cursor || node.Desc.Class == semantic.RadioButton && node.Desc.Label == "OpenCode 2" {
					t.Fatal("unavailable choices can be enabled again after removal")
				}
			}
		})
	}
}

func TestNativeT3CodeModelLimitAppliesToSharedAccountsOnly(t *testing.T) {
	u := nativeTestUI(t)
	models := make([]modelInfo, 40)
	for i := range models {
		models[i] = modelInfo{ID: "vendor/model-" + strconv.Itoa(i), Name: "Fixture model", ContextWindow: 128000}
	}
	nativeSeedSharedForTest(t, u, models...)
	payload, err := nativeClientPayload("t3-code", u.sharedClientSelection("t3-code"))
	if err != nil {
		t.Fatal("valid global library rejected", err)
	}
	library := payload.(map[string]any)["library"].(modelLibrary)
	p := nativeT3CodePreferences{Version: 1, KiloCodex: true, KiloClaude: true, CodexModels: "official", ClaudeModels: "official"}
	if err := nativeT3CodePreferenceLibraryError(p, library); err != nil {
		t.Fatal("official mode rejected unrelated shared library", err)
	}
	p.ClaudeModels = "shared"
	if nativeT3CodePreferenceLibraryError(p, library) == nil {
		t.Fatal("shared Claude account accepted more than32 models")
	}
	p.KiloClaude = false
	if err := nativeT3CodePreferenceLibraryError(p, library); err != nil {
		t.Fatal("disabled account imposed its limit", err)
	}
	p.CodexModels = "shared"
	if nativeT3CodePreferenceLibraryError(p, library) == nil {
		t.Fatal("shared Codex account accepted more than32 models")
	}
}

func TestNativeT3CodePreferencesIgnorePreparedProfileReadBeforeSettingsSave(t *testing.T) {
	f := newNativeT3PreferencesFixture(t, "en")
	u := f.u
	state := &u.agentsState().T3Code
	before := state.PreferencesState.Preferences
	library := u.modelLibraryValue()
	old := nativeT3CodeInfo{Prepared: true, Library: &library, Preferences: before}
	data, _ := json.Marshal(old)
	u.acceptT3CodeProfile(data, u.launchConnectionFingerprint())
	f.mu.Lock()
	f.profileGate = make(chan struct{})
	gate := f.profileGate
	f.profileEntered = make(chan struct{}, 1)
	f.profile = old
	f.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	u.refreshT3CodeProfile()
	select {
	case <-f.profileEntered:
	case <-time.After(time.Second):
		t.Fatal("profile read did not start")
	}
	next := before
	next.Codex = false
	u.saveT3CodePreferences(next)
	nativeTestWait(t, u, func() bool { return !u.t3CodePreferencesBusy() && state.PreferencesState.Pending == nil })
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/t3-code"] })
	if state.Info.Prepared || state.PreferencesState.Preferences.Codex || u.t3CodePrepared(u.sharedClientSelection("t3-code")) {
		t.Fatal("old prepared response erased the need to reprepare")
	}
}

func TestNativeT3ControlsLiveInHarnessGearAndLegacySettingsRedirect(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeT3PreferencesFixture(t, lang)
			u, h := f.u, f.h
			for _, width := range []int{1120, 780} {
				h.size = image.Pt(width, 1600)
				u.page, u.settingsTab = "settings", ""
				h.frame()
				for _, section := range uxsSections {
					if section.id == "t3-code" {
						t.Fatal("T3 remains in global settings index")
					}
				}
				for _, node := range h.nodes() {
					if node.Desc.Class == semantic.Switch && (node.Desc.Label == "Codex" || node.Desc.Label == "Claude" || node.Desc.Label == "OpenCode" || node.Desc.Label == "Muse Code") {
						t.Fatal("T3 controls duplicated in global settings", node.Desc.Label)
					}
				}
				u.page = "agents"
				h.frame()
				gear := u.tr("T3 Code settings", "Ajustes de T3 Code")
				h.reveal(gear, semantic.Button)
				h.click(gear, semantic.Button)
				if u.page != "clients" || u.client != "t3-code" {
					t.Fatal("T3 gear did not open its harness detail", u.page, u.client)
				}
				h.frame()
				h.reveal("Codex", semantic.Switch)
				h.target("Codex", semantic.Switch)
				h.target("Claude", semantic.Switch)
				for _, label := range []string{"OpenCode", "Muse Code"} {
					h.reveal(label, semantic.Switch)
					h.target(label, semantic.Switch)
				}
				h.reveal("Codex", semantic.Switch)
				nativeGridCapture(t, h, "native-t3-gear-accounts-"+strconv.Itoa(width)+"-"+lang)
				h.reveal("Muse Code", semantic.Switch)
				nativeGridCapture(t, h, "native-t3-gear-providers-"+strconv.Itoa(width)+"-"+lang)
				u.page, u.settingsTab = "settings", "t3-code"
				h.frame()
				h.frame()
				if u.page != "clients" || u.client != "t3-code" || u.settingsTab != "" {
					t.Fatal("legacy T3 settings link did not redirect", u.page, u.client, u.settingsTab)
				}
				u.page = "settings"
				u.uxs().jump = "t3-code"
				h.frame()
				h.frame()
				if u.page != "clients" || u.client != "t3-code" || u.uxs().jump != "" {
					t.Fatal("pending legacy T3 jump did not redirect")
				}
			}
			if f.postCount() != 0 {
				t.Fatal("navigation changed saved provider preferences")
			}
		})
	}
}
