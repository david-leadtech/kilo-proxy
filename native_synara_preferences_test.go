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
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

type nativeSynaraPreferencesFixture struct {
	u                      *nativeUI
	h                      *nativePointerHarness
	mu                     sync.Mutex
	response               nativeSynaraPreferencesResponse
	posts                  []nativeSynaraPreferences
	failGet, failPost      bool
	omitOpenCodeConnection bool
	postGate               chan struct{}
	profileGate            chan struct{}
	profileEntered         chan struct{}
	profile                nativeSynaraInfo
	profilePosts           int
}

func newNativeSynaraPreferencesFixture(t *testing.T, language string) *nativeSynaraPreferencesFixture {
	t.Helper()
	u, _ := nativeLaunchTestUI(t, "synara", false, false)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
	u.setLanguage(language)
	nativeTestWait(t, u, func() bool {
		return u.language == language && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
	})
	u.agentSetup("synara")
	var prefs nativeSynaraPreferences
	data, _ := json.Marshal(normalizeSynaraPreferences(synaraPreferences{}))
	if err := json.Unmarshal(data, &prefs); err != nil {
		t.Fatal(err)
	}
	f := &nativeSynaraPreferencesFixture{u: u, response: nativeSynaraPreferencesResponse{Preferences: prefs, SupportedProviders: []nativeSynaraProvider{{"cursor", "Cursor"}, {"opencode", "OpenCode"}}, OpenCode2Supported: true, OpenCodeKiloSupported: true, ClaudePickerSupported: true, RequiresPreparation: true}}
	upstream, err := url.Parse("http://" + u.owner.adminHost)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/clients/synara" && r.Method == http.MethodPost {
			f.mu.Lock()
			f.profilePosts++
			f.mu.Unlock()
		}
		if r.URL.Path == "/api/clients/synara" && r.Method == http.MethodGet {
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
		if r.URL.Path != nativeSynaraPreferencesEndpoint {
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
			var next nativeSynaraPreferences
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
		omitConnection := f.omitOpenCodeConnection
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
		if omitConnection {
			data, _ := json.Marshal(response)
			var legacy map[string]any
			_ = json.Unmarshal(data, &legacy)
			delete(legacy["preferences"].(map[string]any), "openCodeConnection")
			jsonResponse(w, 200, legacy)
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
		state := &u.agentsState().Synara
		return state.PreferencesState.Loaded && state.Loaded && !u.busy["GET/api/clients/synara"]
	})
	f.h.frame()
	return f
}
func (f *nativeSynaraPreferencesFixture) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}
func (f *nativeSynaraPreferencesFixture) lastPost() nativeSynaraPreferences {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts[len(f.posts)-1]
}

func (f *nativeSynaraPreferencesFixture) waitForSaveResponses(t *testing.T, posts int) {
	t.Helper()
	// A confirmed save also refreshes the profile and launcher status. Finish
	// both reads before the next frame captures coordinates that they can move.
	u := f.u
	nativeTestWait(t, u, func() bool {
		return f.postCount() == posts && !u.synaraPreferencesBusy() && u.agentsState().Synara.PreferencesState.Pending == nil &&
			!u.busy["GET/api/clients/synara"] && !u.busy["GET"+nativeLaunchEndpoint] &&
			u.clientState().LaunchChecked && !u.clientState().LaunchRefreshPending
	})
}

func TestNativeSynaraPreferencesSavePendingFailureAndRetry(t *testing.T) {
	f := newNativeSynaraPreferencesFixture(t, "en")
	u, h := f.u, f.h
	state := &u.agentsState().Synara.PreferencesState
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
	if !u.synaraPreferencesBusy() || state.Pending == nil || !nativeSynaraPreferencesEqual(state.Preferences, before) {
		t.Fatal("pending write replaced confirmed selection")
	}
	if u.uxhLaunchState("synara").canOpen {
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
	u.prepareClientAfter("synara", func(err error) { finished, prepareErr = true, err })
	nativeTestWait(t, u, func() bool { return finished })
	if prepareErr == nil {
		t.Fatal("unconfirmed settings allowed preparing")
	}
	f.mu.Lock()
	profilePosts := f.profilePosts
	f.mu.Unlock()
	if profilePosts != 0 || !u.synaraPreferencesBusy() || state.Pending == nil {
		t.Fatal("pending settings reached profile preparation or lost their gate")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() })
	if state.Error == "" || state.Pending == nil || !nativeSynaraPreferencesEqual(state.Preferences, before) {
		t.Fatal("failed save was accepted or lost its retry policy")
	}
	if u.uxhLaunchState("synara").canOpen {
		t.Fatal("Open remains enabled after a failed preferences save")
	}
	f.mu.Lock()
	f.failPost = false
	f.postGate = nil
	f.mu.Unlock()
	h.frame()
	h.reveal("Retry saving", semantic.Button)
	h.click("Retry saving", semantic.Button)
	nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() && state.Pending == nil })
	if f.postCount() != 2 || state.Preferences.Codex || !state.Preferences.Claude || !state.RequiresPreparation || state.Error != "" {
		t.Fatalf("retry did not confirm exact change: %+v", state)
	}
	expected := before
	expected.Codex = false
	if !nativeSynaraPreferencesEqual(f.lastPost(), expected) {
		t.Fatal("retry changed unrelated choices")
	}
}

func TestNativeSynaraPreferencesCapabilitiesAndOpenCodeVersion(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, lang)
			u, h := f.u, f.h
			state := &u.agentsState().Synara.PreferencesState
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
			nativeGridCapture(t, h, "native-synara-preferences-accounts-"+lang)
			h.reveal("OpenCode", semantic.Switch)
			h.click("OpenCode", semantic.Switch)
			f.waitForSaveResponses(t, 1)
			h.frame()
			h.reveal("OpenCode 2", semantic.RadioButton)
			h.click("OpenCode 2", semantic.RadioButton)
			f.waitForSaveResponses(t, 2)
			if !state.Preferences.OpenCode2 || !reflect.DeepEqual(state.Preferences.Providers, []string{"opencode"}) {
				t.Fatal("OpenCode 2 was not selected exclusively")
			}
			h.frame()
			nativeGridCapture(t, h, "native-synara-preferences-opencode-"+lang)
			h.reveal("OpenCode", semantic.Switch)
			h.click("OpenCode", semantic.Switch)
			f.waitForSaveResponses(t, 3)
			if state.Preferences.OpenCode2 || len(state.Preferences.Providers) != 0 {
				t.Fatal("disabling OpenCode retained second runtime")
			}
		})
	}
}

func TestNativeSynaraPreferencesFailedLoadAndLateResponse(t *testing.T) {
	f := newNativeSynaraPreferencesFixture(t, "en")
	u := f.u
	s := &u.agentsState().Synara.PreferencesState
	f.mu.Lock()
	f.failGet = true
	f.mu.Unlock()
	s.Loaded = false
	u.refreshSynaraPreferences()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeSynaraPreferencesEndpoint] })
	if s.Loaded || s.Error == "" {
		t.Fatal("unreadable preferences appeared loaded")
	}
	f.mu.Lock()
	f.failGet = false
	f.mu.Unlock()
	u.refreshSynaraPreferences()
	nativeTestWait(t, u, func() bool { return s.Loaded })
	old := s.Preferences
	u.refreshSynaraPreferences()
	s.Generation++ // A newer settings operation supersedes this GET.
	s.Preferences.Codex = false
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeSynaraPreferencesEndpoint] })
	if s.Preferences.Codex == old.Codex {
		t.Fatal("late GET overwrote newer preference state")
	}
}

func TestNativeSynaraPreferencesCanDisableChoicesUnavailableInNewBuild(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, lang)
			u, h := f.u, f.h
			f.mu.Lock()
			f.response.Preferences.Providers = []string{"cursor", "opencode"}
			f.response.Preferences.OpenCode2 = true
			f.response.SupportedProviders = []nativeSynaraProvider{{"opencode", "OpenCode"}, {"unknown", "Unknown provider"}}
			f.response.OpenCode2Supported = false
			f.mu.Unlock()
			u.refreshSynaraPreferences()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeSynaraPreferencesEndpoint] })
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
			state := &u.agentsState().Synara.PreferencesState
			if !state.Preferences.OpenCode2 || state.OpenCode2Supported {
				t.Fatal("saved OpenCode 2 choice was removed or falsely reported as supported")
			}
			if u.synaraProviderSelectionError() == nil || u.uxhLaunchState("synara").canOpen {
				t.Fatal("unavailable saved choices remained launchable")
			}
			nativeGridCapture(t, h, "native-synara-preferences-unavailable-"+lang)
			h.reveal(cursor, semantic.Switch)
			h.click(cursor, semantic.Switch)
			nativeTestWait(t, u, func() bool { return f.postCount() == 1 && !u.synaraPreferencesBusy() })
			h.frame()
			h.reveal("OpenCode 1", semantic.RadioButton)
			h.click("OpenCode 1", semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 2 && !u.synaraPreferencesBusy() })
			p := u.agentsState().Synara.PreferencesState.Preferences
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

func TestNativeSynaraModelLimitAppliesToSharedAccountsOnly(t *testing.T) {
	u := nativeTestUI(t)
	models := make([]modelInfo, 40)
	for i := range models {
		models[i] = modelInfo{ID: "vendor/model-" + strconv.Itoa(i), Name: "Fixture model", ContextWindow: 128000}
	}
	nativeSeedSharedForTest(t, u, models...)
	payload, err := nativeClientPayload("synara", u.sharedClientSelection("synara"))
	if err != nil {
		t.Fatal("valid global library rejected", err)
	}
	library := payload.(map[string]any)["library"].(modelLibrary)
	p := nativeSynaraPreferences{Version: 1, KiloCodex: true, KiloClaude: true, CodexModels: "official", ClaudeModels: "official"}
	if err := nativeSynaraPreferenceLibraryError(p, library); err != nil {
		t.Fatal("official mode rejected unrelated shared library", err)
	}
	p.ClaudeModels = "shared"
	if nativeSynaraPreferenceLibraryError(p, library) == nil {
		t.Fatal("shared Claude account accepted more than32 models")
	}
	p.KiloClaude = false
	if err := nativeSynaraPreferenceLibraryError(p, library); err != nil {
		t.Fatal("disabled account imposed its limit", err)
	}
	p.CodexModels = "shared"
	if nativeSynaraPreferenceLibraryError(p, library) == nil {
		t.Fatal("shared Codex account accepted more than32 models")
	}
}

func TestNativeSynaraPreferencesIgnorePreparedProfileReadBeforeSettingsSave(t *testing.T) {
	f := newNativeSynaraPreferencesFixture(t, "en")
	u := f.u
	state := &u.agentsState().Synara
	before := state.PreferencesState.Preferences
	library := u.modelLibraryValue()
	old := nativeSynaraInfo{Prepared: true, Library: &library, Preferences: before}
	data, _ := json.Marshal(old)
	u.acceptSynaraProfile(data, u.launchConnectionFingerprint())
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
	u.refreshSynaraProfile()
	select {
	case <-f.profileEntered:
	case <-time.After(time.Second):
		t.Fatal("profile read did not start")
	}
	next := before
	next.Codex = false
	u.saveSynaraPreferences(next)
	nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() && state.PreferencesState.Pending == nil })
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/synara"] })
	if state.Info.Prepared || state.PreferencesState.Preferences.Codex || u.synaraPrepared(u.sharedClientSelection("synara")) {
		t.Fatal("old prepared response erased the need to reprepare")
	}
}

func TestNativeSynaraOpenCodeConnectionCompatibilityAndGatewayGate(t *testing.T) {
	p := nativeSynaraPreferences{Version: 1, CodexModels: "official", ClaudeModels: "official", Providers: []string{"opencode"}}
	explicit := p
	explicit.OpenCodeConnection = "native"
	if !validNativeSynaraPreferences(p) || !nativeSynaraPreferencesEqual(p, explicit) || nativeSynaraOpenCodeUsesKilo(p) {
		t.Fatal("old preferences lost native compatibility")
	}
	p.OpenCodeConnection = "other"
	if validNativeSynaraPreferences(p) {
		t.Fatal("invalid OpenCode connection accepted")
	}
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, lang)
			u := f.u
			state := &u.agentsState().Synara.PreferencesState
			state.Preferences = explicit
			if !u.synaraNativeOnly() {
				t.Fatal("own OpenCode connection requires Kilo")
			}
			state.Preferences.OpenCodeConnection = "kilo"
			if u.synaraNativeOnly() {
				t.Fatal("Kilo OpenCode bypassed the gateway")
			}
			nativeSeedSharedForTest(t, u)
			if u.uxhLaunchState("synara").canOpen {
				t.Fatal("Kilo OpenCode opened without shared models")
			}
			u.owner.mu.Lock()
			u.owner.apiKey = ""
			u.owner.mu.Unlock()
			nativeSeedSharedForTest(t, u, u.models[0])
			if launch := u.uxhLaunchState("synara"); launch.canOpen || !strings.Contains(launch.reason, u.tr("Connect Kilo or ChatGPT", "Conecta Kilo o ChatGPT")) {
				t.Fatalf("Kilo OpenCode ignored its connection: %+v", launch)
			}
		})
	}
}

func TestNativeSynaraOpenCodeConnectionSavesSummaryAndResponsiveLayout(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, lang)
			u, h := f.u, f.h
			state := &u.agentsState().Synara.PreferencesState
			h.reveal("OpenCode", semantic.Switch)
			h.click("OpenCode", semantic.Switch)
			f.waitForSaveResponses(t, 1)
			h.frame()
			kilo := u.tr("Kilo Proxy · shared models", "Kilo Proxy · modelos compartidos")
			h.reveal(kilo, semantic.RadioButton)
			h.click(kilo, semantic.RadioButton)
			f.waitForSaveResponses(t, 2)
			if state.Preferences.OpenCodeConnection != "kilo" || f.lastPost().OpenCodeConnection != "kilo" || state.Preferences.OpenCode2 {
				t.Fatal("connection did not save independently of major")
			}
			h.frame()
			h.reveal("OpenCode 2", semantic.RadioButton)
			h.click("OpenCode 2", semantic.RadioButton)
			f.waitForSaveResponses(t, 3)
			if state.Preferences.OpenCodeConnection != "kilo" || !state.Preferences.OpenCode2 {
				t.Fatal("major change lost the connection")
			}
			gate := make(chan struct{})
			close(gate)
			f.mu.Lock()
			f.profileGate = gate
			f.profile = nativeSynaraInfo{Preferences: state.Preferences, EffectiveOpenCodeModels: []nativeSynaraModelSummary{{ID: "vendor/one", Name: "Shared coding model"}}}
			f.mu.Unlock()
			u.refreshSynaraProfile()
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/clients/synara"] })
			for _, width := range []int{1120, 780} {
				h.size = image.Pt(width, 1400)
				h.frame()
				h.reveal(kilo, semantic.RadioButton)
				h.target(kilo, semantic.RadioButton)
				h.reveal(u.tr("Own OpenCode account", "Cuenta propia de OpenCode"), semantic.RadioButton)
				if !nativeUpdateHasText(h, "Shared coding model · vendor/one") {
					t.Fatal("confirmed OpenCode Kilo summary absent")
				}
				nativeGridCapture(t, h, "native-synara-opencode-connection-"+strconv.Itoa(width)+"-"+lang)
			}
			own := u.tr("Own OpenCode account", "Cuenta propia de OpenCode")
			h.reveal(own, semantic.RadioButton)
			h.click(own, semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 4 && !u.synaraPreferencesBusy() && state.Pending == nil })
			if state.Preferences.OpenCodeConnection != "native" || !state.Preferences.OpenCode2 {
				t.Fatal("own connection reset the selected major")
			}
			h.frame()
			if nativeUpdateHasText(h, "Shared coding model · vendor/one") {
				t.Fatal("own connection shows a stale Kilo model summary")
			}
			for _, width := range []int{1120, 780} {
				h.size = image.Pt(width, 1400)
				h.frame()
				h.reveal(own, semantic.RadioButton)
				loginNote := u.tr("OpenCode 2 has a separate profile that may need its own login.", "OpenCode 2 tiene un perfil separado que puede requerir su propio inicio de sesión.")
				explained := false
				for _, node := range h.nodes() {
					explained = explained || strings.Contains(node.Desc.Label, loginNote)
				}
				if !explained {
					t.Fatal("native OpenCode 2 login isolation was not explained")
				}
				nativeGridCapture(t, h, "native-synara-opencode-native-v2-"+strconv.Itoa(width)+"-"+lang)
			}
		})
	}
}

func TestNativeSynaraOpenCodeConnectionRequiresExactConfirmationAndSupportedDriver(t *testing.T) {
	f := newNativeSynaraPreferencesFixture(t, "en")
	u := f.u
	state := &u.agentsState().Synara.PreferencesState
	f.mu.Lock()
	f.omitOpenCodeConnection = true
	f.mu.Unlock()
	u.refreshSynaraPreferences()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeSynaraPreferencesEndpoint] })
	next := state.Preferences
	next.Providers, next.OpenCodeConnection = []string{"opencode"}, "kilo"
	u.saveSynaraPreferences(next)
	nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() })
	if state.Pending == nil || state.Error == "" || nativeSynaraOpenCodeUsesKilo(state.Preferences) {
		t.Fatal("unconfirmed Kilo connection replaced the saved choice")
	}
	f.mu.Lock()
	f.omitOpenCodeConnection = false
	f.response.OpenCodeKiloSupported = false
	f.mu.Unlock()
	u.saveSynaraPreferences(*state.Pending)
	nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() && state.Pending == nil })
	if state.Preferences.OpenCodeConnection != "kilo" || u.synaraOpenCodeConnectionError() == nil || u.uxhLaunchState("synara").canOpen {
		t.Fatal("unsupported saved Kilo choice was silently changed or launchable")
	}
	if reason := u.synaraOpenCodeConnectionError().Error(); !strings.Contains(reason, "compatible macOS build") {
		t.Fatal("unsupported Kilo connection omitted the compatible macOS requirement", reason)
	}
	f.h.frame()
	f.h.reveal("Own OpenCode account", semantic.RadioButton)
	f.h.click("Own OpenCode account", semantic.RadioButton)
	nativeTestWait(t, u, func() bool { return f.postCount() == 3 && !u.synaraPreferencesBusy() && state.Pending == nil })
	if state.Preferences.OpenCodeConnection != "native" || u.synaraOpenCodeConnectionError() != nil {
		t.Fatal("own account still depends on Kilo driver capability")
	}
}

func TestNativeSynaraNativeAccountsWithoutKiloAndTransition(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u, recorder := nativeLaunchTestUI(t, "synara", false, false)
			u.owner.mu.Lock()
			p := normalizeSynaraPreferences(u.owner.config.Synara)
			p.Claude, p.KiloCodex, p.KiloClaude = false, false, false
			u.owner.config.Synara = p
			u.owner.apiKey, u.owner.config.OrgID, u.owner.config.Language = "", "", language
			u.owner.mu.Unlock()
			u.authenticated, u.language, u.models = false, language, nil
			nativeSeedSharedForTest(t, u)
			u.refreshSynaraPreferences()
			u.detectLaunchers()
			nativeTestWait(t, u, func() bool {
				return u.agentsState().Synara.PreferencesState.Loaded && !u.busy["GET"+nativeLaunchEndpoint]
			})
			before := u.owner.modelLibrary.snapshot().Revision
			if u.agentConnectionReady() || len(u.library.selection.Models) != 0 || !u.synaraNativeOnly() {
				t.Fatal("fixture unexpectedly has Kilo requirements")
			}
			if state := u.uxhLaunchState("synara"); !state.canOpen || state.reason != "" {
				t.Fatalf("own Synara account blocked by Kilo: %+v", state)
			}
			u.agentSetup("synara")
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
			h.frame()
			if !nativeUpdateHasText(h, u.tr("Native provider catalogs", "Catálogos nativos de los proveedores")) || nativeUpdateHasText(h, u.tr("Edit in Models", "Editar en Modelos")) {
				t.Fatal("native-only Synara shows Kilo library")
			}
			h.click(u.tr("Open Synara", "Abrir Synara"), semantic.Button)
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
			if recorder.count() != 1 || recorder.prepares.Load() != 1 || !u.synaraPrepared(u.sharedClientSelection("synara")) {
				t.Fatalf("native-only Synara did not prepare/open: %s", u.notice)
			}
			recorder.mu.Lock()
			plan := recorder.plans[0]
			recorder.mu.Unlock()
			u.owner.mu.Lock()
			started := u.owner.proxyServer != nil
			u.owner.mu.Unlock()
			if started || !plan.skipProxy || u.owner.modelLibrary.snapshot().Revision != before {
				t.Fatal("native-only Synara started gateway or rewrote Models")
			}
			u.owner.mu.Lock()
			u.owner.config.Synara.KiloCodex = true
			u.owner.mu.Unlock()
			u.refreshSynaraPreferences()
			nativeTestWait(t, u, func() bool {
				return !u.busy["GET"+nativeSynaraPreferencesEndpoint] && !u.busy["GET"+nativeLaunchEndpoint] && u.clientState().LaunchChecked
			})
			if u.synaraNativeOnly() || u.synaraPrepared(u.sharedClientSelection("synara")) || u.uxhLaunchState("synara").canOpen {
				t.Fatal("Kilo account reused native-only readiness")
			}
			u.launchAgent("synara")
			if recorder.count() != 1 || recorder.prepares.Load() != 1 || u.notice == "" {
				t.Fatal("Kilo Synara launched without Models")
			}
			u.models = nativeClientModelsForTest()
			nativeSeedSharedForTest(t, u, u.models[0])
			if state := u.uxhLaunchState("synara"); state.canOpen || !strings.Contains(state.reason, u.tr("Connect Kilo or ChatGPT", "Conecta Kilo o ChatGPT")) {
				t.Fatalf("Kilo account ignored connection: %+v", state)
			}
		})
	}
}

func TestNativeSynaraAccountsLiveInHarnessGear(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, language)
			u, h := f.u, f.h
			for _, width := range []int{1120, 780} {
				h.size = image.Pt(width, 1500)
				u.page, u.settingsTab = "settings", ""
				h.frame()
				for _, node := range h.nodes() {
					if node.Desc.Class == semantic.Switch && (node.Desc.Label == "Codex" || node.Desc.Label == "Claude" || node.Desc.Label == "OpenCode") {
						t.Fatal("Synara controls duplicated in global Settings")
					}
				}
				u.page = "agents"
				h.frame()
				gear := u.tr("Synara settings", "Ajustes de Synara")
				h.reveal(gear, semantic.Button)
				h.click(gear, semantic.Button)
				if u.page != "clients" || u.client != "synara" {
					t.Fatal("Synara gear did not open its detail", u.page, u.client)
				}
				h.frame()
				h.reveal("Codex", semantic.Switch)
				h.target("Claude", semantic.Switch)
				if !nativeUpdateHasText(h, u.tr("Accounts and models", "Cuentas y modelos")) {
					t.Fatal("Synara gear omitted account controls")
				}
				for _, node := range h.nodes() {
					if strings.Contains(node.Desc.Label, "· Normal") || node.Desc.Label == "Muse Code" {
						t.Fatal("duplicate or unsupported account", node.Desc.Label)
					}
				}
				nativeGridCapture(t, h, "native-synara-gear-accounts-"+strconv.Itoa(width)+"-"+language)
			}
			if f.postCount() != 0 {
				t.Fatal("navigation changed saved accounts")
			}
		})
	}
}

func TestNativeSynaraClaudePickerCapabilityBlocksOnlyKiloClaude(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeSynaraPreferencesFixture(t, language)
			u := f.u
			s := &u.agentsState().Synara.PreferencesState
			f.mu.Lock()
			f.response.ClaudePickerSupported = false
			f.mu.Unlock()
			u.refreshSynaraPreferences()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeSynaraPreferencesEndpoint] })
			for _, scope := range []string{"shared", "official"} {
				s.Preferences.ClaudeModels = scope
				if u.synaraProviderSelectionError() == nil || u.uxhLaunchState("synara").canOpen {
					t.Fatal("unsupported Claude picker opened", scope)
				}
			}
			next := s.Preferences
			next.KiloClaude = false
			u.saveSynaraPreferences(next)
			nativeTestWait(t, u, func() bool { return !u.synaraPreferencesBusy() && s.Pending == nil })
			if !s.Preferences.Claude || !s.Preferences.KiloCodex || u.synaraProviderSelectionError() != nil {
				t.Fatal("native Claude or Codex depends on Kilo Claude picker")
			}
		})
	}
}

func TestNativeSynaraSavedAccountsRefreshAvailabilityAfterOlderDetection(t *testing.T) {
	f := newNativeSynaraPreferencesFixture(t, "en")
	u := f.u
	old := u.clientState().LaunchInfo
	data, _ := json.Marshal(old)
	var latest nativeLaunchInfo
	_ = json.Unmarshal(data, &latest)
	oldClient := old.Clients["synara"]
	oldClient.Available, oldClient.Reason = false, "Synthetic previous account unavailable"
	old.Clients = map[string]nativeLaunchClient{"synara": oldClient}
	oldData, _ := json.Marshal(old)
	newClient := latest.Clients["synara"]
	newClient.Available, newClient.Reason = true, ""
	latest.Clients["synara"] = newClient
	newData, _ := json.Marshal(latest)
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == nativeLaunchEndpoint {
			if reads.Add(1) == 1 {
				entered <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(oldData)
			} else {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(newData)
			}
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	u.detectLaunchers()
	nativeTestWait(t, u, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	next := u.agentsState().Synara.PreferencesState.Preferences
	next.KiloClaude = false
	u.saveSynaraPreferences(next)
	nativeTestWait(t, u, func() bool {
		return !u.synaraPreferencesBusy() && u.agentsState().Synara.PreferencesState.Pending == nil
	})
	if u.clientState().LaunchChecked || !u.clientState().LaunchRefreshPending {
		t.Fatal("saved accounts reused older availability")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return u.clientState().LaunchChecked && !u.busy["GET"+nativeLaunchEndpoint] })
	if reads.Load() != 2 || !u.nativeLaunchAvailable("synara") || u.clientState().LaunchInfo.Clients["synara"].Reason != "" {
		t.Fatal("saved account recovery retained old detection", reads.Load())
	}
}
