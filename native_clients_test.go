//go:build desktop

package main

import (
	"encoding/json"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func nativeClientModelsForTest() []modelInfo {
	tools := true
	one, two := 1.0, 2.0
	return []modelInfo{
		{ID: "vendor/one", Name: "Very Long First Model", ContextWindow: 64000, MaxOutputTokens: 4000, ReasoningEfforts: []string{"low", "high"}, Tools: &tools, OutputModalities: []string{"text"}, InputPrice: &one, OutputPrice: &two},
		{ID: "anthropic/claude-sonnet-4.6", Name: "Claude Sonnet", ContextWindow: 128000, Tools: &tools, OutputModalities: []string{"text"}},
	}
}

func nativeQueueModeFromTOML(t *testing.T, data []byte) (string, bool) {
	t.Helper()
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	value, ok := tomlAt(config, []string{"desktop", "followUpQueueMode"})
	mode, ok := value.(string)
	return mode, ok
}

func TestNativeClientProfileSelectionIsolationAndValidation(t *testing.T) {
	c := nativeClients{}
	for _, key := range []string{"codex", "codex-cli", "claude", "opencode", "omp", "zed", "xcode-chat", "xcode-codex", "xcode-claude"} {
		s := c.selection(key)
		for _, m := range nativeClientModelsForTest() {
			if err := s.add(m, 50); err != nil {
				t.Fatal(err)
			}
		}
		s.choice("vendor/one").DisplayName = key
		s.Initial = "anthropic/claude-sonnet-4.6"
		s.Aliases["haiku"] = "vendor/one"
	}
	s := c.selection("codex")
	s.remove("anthropic/claude-sonnet-4.6")
	s.remove("vendor/one")
	if len(s.Models) != 0 || s.Initial != "" || len(s.Aliases) != 0 {
		t.Fatal("removed model remained as default or alias")
	}
	for key, s := range c.Selections {
		if key != "codex" && (len(s.Models) != 2 || s.Models[0].DisplayName != key) {
			t.Fatal("another client changed", key)
		}
	}
	if err := s.add(modelInfo{ID: "bad id"}, 50); err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := s.add(modelInfo{ID: "manual/model"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.add(modelInfo{ID: "manual/other"}, 1); err == nil {
		t.Fatal("model limit ignored")
	}
	limits, err := contextPolicyForChoice(s.Models[0])
	if err != nil || limits.ContextWindow != 272000 || limits.MaximumKnown || s.Models[0].ContextPreset != contextPresetRecommended {
		t.Fatal("unknown model should use Recommended without inventing catalog capacity")
	}
}

func TestNativeClientsPayloadAndLoadRoundTrips(t *testing.T) {
	for _, key := range []string{"codex", "codex-cli", "claude", "opencode", "omp", "zed", "xcode-chat", "xcode-codex", "xcode-claude"} {
		t.Run(key, func(t *testing.T) {
			s := (&nativeClients{}).selection(key)
			for _, m := range nativeClientModelsForTest() {
				if err := s.add(m, 50); err != nil {
					t.Fatal(err)
				}
			}
			s.Initial = "anthropic/claude-sonnet-4.6"
			s.Models[0].DisplayName = "Short"
			s.Models[0].DefaultReasoning = "high"
			s.Mode = "modern"
			if key == "codex" {
				s.QueueMode = codexQueueModeSteer
			}
			if key == "claude" || key == "xcode-claude" {
				s.Models[1].ClaudeEffort = "high"
				s.Aliases["haiku"] = "vendor/one"
			}
			payload, err := nativeClientPayload(key, s)
			if err != nil {
				t.Fatal(err)
			}
			var response any = payload
			if key == "opencode" || key == "zed" || key == "omp" {
				response = map[string]any{"selection": payload, "configPath": "temporary-profile"}
			}
			if key == "xcode-codex" {
				response = payload.(map[string]any)["catalog"]
			}
			data, _ := json.Marshal(response)
			loaded, err := decodeNativeClientSelection(key, data, nativeClientModelsForTest())
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Models) != 2 || loaded.Initial != s.Initial || loaded.choice("vendor/one").DisplayName != "Short" {
				t.Fatalf("lost model identity/default/label: %+v", loaded)
			}
			if loaded.Saved != "" {
				t.Fatal("loaded profile incorrectly ready for current credentials")
			}
			if key == "codex" && loaded.QueueMode != codexQueueModeSteer {
				t.Fatal("queue mode lost")
			}
			if strings.Contains(key, "codex") {
				_, effort := nativeReasoningFor(*loaded.choice("vendor/one"))
				if effort != "high" {
					t.Fatal("reasoning default lost")
				}
			}
			if key == "opencode" || key == "zed" || key == "omp" {
				m := loaded.choice("vendor/one")
				if m.Model.ContextWindow != 64000 || m.Model.MaxOutputTokens != 4000 {
					t.Fatal("limits lost")
				}
			}
			if nativeClientEndpoint(key) == "" {
				t.Fatal("client cannot reach its existing backend")
			}
		})
	}
	if _, err := decodeNativeClientSelection("zed", []byte(`{"selection":{"models":[]}}`), nil); err == nil {
		t.Fatal("invalid saved selection accepted")
	}
}

func TestNativeClientsFilteringAndDirtyState(t *testing.T) {
	catalog := nativeClientModelsForTest()
	catalog = append(catalog, modelInfo{ID: "image/one", Name: "Image"})
	s := (&nativeClients{}).selection("codex")
	_ = s.add(catalog[0], 50)
	s.Models[0].DisplayName = "Short"
	if got := nativeVisibleModels(catalog, s, "short", false, true, "codeModeRank", ""); len(got) != 1 || got[0].ID != "vendor/one" {
		t.Fatal("saved-name search failed")
	}
	if got := nativeVisibleModels(catalog, s, "", true, false, "codeModeRank", ""); len(got) != 1 {
		t.Fatal("selected-only filter failed")
	}
	if got := nativeVisibleModels(catalog, s, "", false, true, "codeModeRank", ""); len(got) != 2 {
		t.Fatal("tool/text filter failed")
	}
	if nativeModelPrice(catalog[0]) != "$1 / $2 USD / 1M" || nativeModelPrice(catalog[2]) != "— / — USD / 1M" {
		t.Fatal("unknown pricing misrepresented")
	}
	before := nativeSelectionFingerprint("codex", s, "local", "key", claudeCapabilities{})
	s.QueueMode = codexQueueModeSteer
	if nativeSelectionFingerprint("codex", s, "local", "key", claudeCapabilities{}) == before {
		t.Fatal("queue mode did not mark selection dirty")
	}
	before = nativeSelectionFingerprint("codex", s, "local", "key", claudeCapabilities{})
	s.Models[0].DefaultReasoning = "high"
	if nativeSelectionFingerprint("codex", s, "local", "key", claudeCapabilities{}) == before {
		t.Fatal("reasoning did not mark selection dirty")
	}
	before = nativeSelectionFingerprint("codex", s, "local", "key", claudeCapabilities{})
	if nativeSelectionFingerprint("codex", s, "other-port", "key", claudeCapabilities{}) == before || nativeSelectionFingerprint("codex", s, "local", "new-key", claudeCapabilities{}) == before {
		t.Fatal("connection changes did not invalidate prepared profile")
	}
}

// These invoke Gio's real programmatic Clickable action, then the production
// asynchronous authenticated API. File effects happen only in the fixture root.
func TestNativeClientsWidgetActionsPrepareEveryEditor(t *testing.T) {
	for _, key := range []string{"codex", "codex-cli", "claude", "opencode", "omp", "zed", "xcode-chat", "xcode-codex", "xcode-claude"} {
		t.Run(key, func(t *testing.T) {
			u := nativeTestUI(t)
			u.page = "models"
			u.expanded["library.catalog"] = true
			u.models = nativeClientModelsForTest()
			if key == "claude" || key == "xcode-claude" {
				// Claude's configurable compaction window starts at 100K.
				u.models[0].ContextWindow = 128000
			}
			u.client = key
			if strings.HasPrefix(key, "xcode-") {
				u.client = "xcode"
				u.clientState().Variant = strings.TrimPrefix(key, "xcode-")
				u.clients.Xcode = u.owner.xcodeInfo()
				u.clients.XcodeChecked = true
				u.clients.XcodeDetectStarted = true
			}
			if key == "claude" {
				u.setValue("clients-claude-mode", "modern")
				u.clientState().ClaudeDetectStarted = true
			}
			nativeTestFrame(t, u)
			uxmClickMenuItem(t, u, "models.filters", "client:shared:select-all")
			shared := u.library.selection
			if len(shared.Models) != 2 {
				t.Fatalf("native select-results did not select both models: %d", len(shared.Models))
			}
			u.clickable("models.done").Click()
			nativeTestFrame(t, u)
			uxmClickMenuItem(t, u, "models.row:vendor/one", "client:shared:edit:vendor/one")
			u.setValue(nativeClientField(sharedModelKey, "vendor/one", "name"), "Short Native")
			if strings.Contains(key, "codex") {
				field := nativeClientField(sharedModelKey, "vendor/one", "reasoning")
				u.clickable(field + ".toggle").Click()
				nativeTestFrame(t, u)
				u.clickable(field + ".option.high").Click()
				nativeTestFrame(t, u)
			}
			u.clickable("client:shared:initial:vendor/one").Click()
			nativeTestFrame(t, u)
			u.flushModelLibrary()
			u.page = "clients"
			nativeTestFrame(t, u)
			if key == "codex" {
				// "If you type while Codex works" offers the two modes as option cards.
				u.clickable("client:codex:queue-mode.steer").Click()
				nativeTestFrame(t, u)
			}
			s := u.sharedClientSelection(key)
			u.syncClientSelection(key, s)
			if key == "codex" || key == "codex-cli" {
				exported, err := u.clientExport(key, s, false)
				if err != nil {
					t.Fatal(err)
				}
				mode, hasMode := nativeQueueModeFromTOML(t, []byte(exported))
				if key == "codex" && (!hasMode || mode != codexQueueModeSteer) {
					t.Fatalf("native desktop export lost its selected queue mode (selection %q, control %q)", s.QueueMode, u.value("client:codex:queue-mode"))
				}
				if key == "codex-cli" && hasMode {
					t.Fatal("Codex CLI export included desktop queue mode")
				}
			}
			if key == "opencode" {
				// The first client frame starts an asynchronous settings read.
				// Wait for its real callback before clicking the enabled control.
				nativeTestWait(t, u, u.openCodePreferencesReady)
				nativeTestFrame(t, u)
			}
			if nativeCLIPathClient(key) {
				if !u.clientState().CLIPreferences.Requested {
					u.refreshCLIPreferencesAfter(nil)
				}
				nativeTestWait(t, u, u.cliPreferencesReady)
				nativeTestFrame(t, u)
			}
			u.clickable("client:" + key + ":prepare").Click()
			nativeTestFrame(t, u)
			nativeTestWait(t, u, func() bool { return s.Saved != "" })
			if s.Path == "" {
				t.Fatal("prepared profile has no path")
			}
			if key == "codex" || key == "codex-cli" || key == "xcode-codex" {
				data, err := os.ReadFile(filepath.Join(s.Path, "models.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "Short Native") {
					t.Fatal("native label did not reach saved catalog")
				}
				config, err := os.ReadFile(filepath.Join(s.Path, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(config), "kilo-local") {
					t.Fatal("native prepare did not configure provider")
				}
				mode, hasMode := nativeQueueModeFromTOML(t, config)
				if key == "codex" && (!hasMode || mode != codexQueueModeSteer) {
					t.Fatal("native prepare did not save the selected queue mode")
				}
				if key != "codex" && hasMode {
					t.Fatal("non-desktop Codex profile included the queue mode")
				}
			} else if key == "opencode" || key == "zed" || key == "omp" {
				data, err := os.ReadFile(s.Path)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), "Short Native") {
					t.Fatal("native label did not reach editor settings")
				}
				if strings.Contains(string(data), "kl_local_") != (key == "opencode" || key == "omp") {
					t.Fatal("incorrect editor credential storage")
				}
			} else if key == "xcode-chat" {
				if _, err := os.ReadFile(s.Path); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := os.ReadFile(filepath.Join(s.Path, "settings.json")); err != nil {
					t.Fatal(err)
				}
			}
			if stored := u.owner.modelLibrary.snapshot().Library; stored.DefaultModel != "vendor/one" || len(stored.Models) != 2 || nativeSavedLibraryItem(t, stored, "vendor/one").DisplayName != "Short Native" {
				t.Fatalf("preparation changed the shared library: %#v", stored)
			}
		})
	}
}

func TestNativeClientsPrepareWaitsForConfirmedCLIPaths(t *testing.T) {
	for _, key := range []string{"codex-cli", "claude", "xcode-codex", "xcode-claude"} {
		for _, language := range []string{"en", "es"} {
			t.Run(key+"/"+language, func(t *testing.T) {
				u := nativeTestUI(t)
				u.setLanguage(language)
				nativeTestWait(t, u, func() bool {
					return u.language == language && u.languageTarget == "" && len(u.busy) == 0
				})
				u.models = nativeClientModelsForTest()
				u.models[0].ContextWindow = 128000
				model := u.models[0]
				nativeSeedSharedForTest(t, u, model)
				u.page, u.client = "clients", key
				if strings.HasPrefix(key, "xcode-") {
					u.client = "xcode"
					u.clientState().Variant = strings.TrimPrefix(key, "xcode-")
					u.clients.Xcode = u.owner.xcodeInfo()
					u.clients.XcodeChecked, u.clients.XcodeDetectStarted = true, true
				}
				if key == "claude" {
					u.setValue("clients-claude-mode", "modern")
				}
				entered, release := make(chan struct{}, 1), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				var prepares atomic.Int32
				handler := u.owner.adminHandler()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == nativeCLIPreferencesEndpoint {
						select {
						case entered <- struct{}{}:
						default:
						}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					if r.Method == http.MethodPost && r.URL.Path == nativeClientEndpoint(key) {
						prepares.Add(1)
					}
					handler.ServeHTTP(w, r)
				}))
				t.Cleanup(func() { unblock(); server.Close() })
				u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
				h := &nativePointerHarness{t: t, u: u, size: image.Pt(1280, 5000), now: time.Now()}
				h.frame()
				if !u.clientState().CLIPreferences.Requested {
					// Xcode can inherit an in-flight read started in Settings.
					u.refreshCLIPreferencesAfter(nil)
				}
				nativeTestWait(t, u, func() bool {
					select {
					case <-entered:
						return true
					default:
						return false
					}
				})
				h.frame()
				if u.cliPreferencesReady() || !u.cliPreferencesBusy() {
					t.Fatal("the retained settings response did not keep CLI paths unconfirmed")
				}
				s := u.sharedClientSelection(key)
				u.clickable("client:" + key + ":prepare").Click()
				h.frame()
				if prepares.Load() != 0 || s.Saved != "" || !strings.Contains(u.notice, u.tr("Load or finish saving CLI executable settings before opening.", "Carga o termina de guardar las rutas de las CLI antes de abrir.")) {
					t.Fatalf("a click while CLI paths were loading did not preserve the guard: posts=%d saved=%q notice=%q", prepares.Load(), s.Saved, u.notice)
				}
				unblock()
				nativeTestWait(t, u, u.cliPreferencesReady)
				h.frame()
				u.clickable("client:" + key + ":prepare").Click()
				h.frame()
				nativeTestWait(t, u, func() bool { return s.Saved != "" && !u.cliPreferencesBusy() })
				if prepares.Load() != 1 || !u.uxhProfileReady(key, s) || s.Path == "" {
					t.Fatalf("confirmed paths did not prepare exactly one current profile: posts=%d saved=%q path=%q", prepares.Load(), s.Saved, s.Path)
				}
			})
		}
	}
}

func TestNativeOpenCodePrepareWaitsForConfirmedPreferences(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			u := nativeTestUI(t)
			u.setLanguage(language)
			nativeTestWait(t, u, func() bool {
				return u.language == language && u.languageTarget == "" && len(u.busy) == 0
			})
			model := nativeClientModelsForTest()[0]
			shared := nativeSeedSharedForTest(t, u, model)
			shared.choice(model.ID).DisplayName = "Slow load model"
			u.seedClientChoice(sharedModelKey, *shared.choice(model.ID))
			u.persistLibraryEdits()
			u.flushModelLibrary()
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var prepares atomic.Int32
			handler := u.owner.adminHandler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == nativeOpenCodePreferencesEndpoint {
					select {
					case entered <- struct{}{}:
					default:
					}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if r.Method == http.MethodPost && r.URL.Path == nativeClientEndpoint("opencode") {
					prepares.Add(1)
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(func() { unblock(); server.Close() })
			u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
			u.page, u.client = "clients", "opencode"
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(1280, 5000), now: time.Now()}
			h.frame()
			nativeTestWait(t, u, func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			h.frame()
			s := u.sharedClientSelection("opencode")
			detail := u.openCodeDetail("opencode", s)
			if u.openCodePreferencesReady() || detail.canPrepare == nil || *detail.canPrepare || !nativeUpdateHasText(h, u.tr("Loading version…", "Cargando versión…")) {
				t.Fatal("pending preferences did not expose a loading state with preparation disabled")
			}
			u.clickable("client:opencode:prepare").Click()
			h.frame()
			if prepares.Load() != 0 || s.Saved != "" {
				t.Fatal("a click while settings were loading reached profile preparation")
			}
			unblock()
			nativeTestWait(t, u, u.openCodePreferencesReady)
			h.frame()
			detail = u.openCodeDetail("opencode", s)
			if detail.canPrepare == nil || !*detail.canPrepare || nativeUpdateHasText(h, u.tr("Loading version…", "Cargando versión…")) {
				t.Fatal("confirmed preferences did not enable preparation and clear loading")
			}
			u.clickable("client:opencode:prepare").Click()
			h.frame()
			nativeTestWait(t, u, func() bool { return s.Saved != "" && !u.openCodePreferencesBusy() })
			if prepares.Load() != 1 {
				t.Fatalf("the enabled control prepared %d times; want exactly once", prepares.Load())
			}
			data, err := os.ReadFile(s.Path)
			if err != nil || !strings.Contains(string(data), "Slow load model") || !strings.Contains(string(data), "vendor/one") {
				t.Fatalf("the confirmed control did not save the chosen model: %v", err)
			}
		})
	}
}

func TestNativeClientsExportMasksPreviewsAndCopiesRealLocalKey(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "clients"
	u.client = "opencode"
	u.models = nativeClientModelsForTest()
	nativeSeedSharedForTest(t, u, u.models...)
	nativeTestFrame(t, u)
	s := u.sharedClientSelection("opencode")
	_, local, _ := u.clientBase()
	preview, err := u.clientExport("opencode", s, false)
	if err != nil {
		t.Fatal(err)
	}
	full, err := u.clientExport("opencode", s, true)
	if err != nil {
		t.Fatal(err)
	}
	if local == "" || strings.Contains(preview, local) || !strings.Contains(full, local) {
		t.Fatal("masked preview / full configuration boundary broken")
	}
	u.expanded["client:opencode:show-config"] = true
	nativeTestFrame(t, u)
	u.clickable("client:opencode:config-copy").Click()
	nativeTestFrame(t, u)
	bridge := u.owner.desktop.(*nativeRecordingBridge)
	bridge.mu.Lock()
	copied := bridge.Text
	bridge.mu.Unlock()
	if copied != full {
		t.Fatal("native Copy complete configuration did not use actual local credential")
	}
	for _, key := range []string{"codex", "codex-cli", "claude", "opencode", "omp", "zed"} {
		if nativeClientEndpoint(key) == "" {
			t.Fatal("missing route")
		}
	}
	// A remembered API key belongs to Kilo Proxy only, never an editor export.
	if strings.Contains(full, "synthetic-kilo-personal-key") {
		t.Fatal("upstream credential leaked into editor config")
	}
}

func TestNativeClientsMalformedProfileIsNotOverwritten(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "clients"
	u.client = "zed"
	u.models = nativeClientModelsForTest()
	nativeSeedSharedForTest(t, u, u.models...)
	dir := filepath.Join(u.owner.editorTestRoot, ".config", "zed")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	original := []byte("{ invalid team settings")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	nativeTestFrame(t, u)
	u.clickable("client:zed:prepare").Click()
	nativeTestFrame(t, u)
	nativeTestWait(t, u, func() bool { return !u.busy["POST/api/editors/zed/profile"] })
	if !strings.Contains(u.notice, "valid JSON") {
		t.Fatalf("profile error was hidden: %s", u.notice)
	}
	if u.clientState().selection("zed").Saved != "" {
		t.Fatal("failed profile incorrectly marked ready")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("malformed user settings overwritten")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatal("validation failure wrote a misleading backup")
	}
}

func TestNativeClientsManualModelsAreSharedAcrossAgents(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "models"
	u.expanded["library.catalog"] = true
	nativeTestFrame(t, u)
	u.clickable("client:shared:exact").Click()
	nativeTestFrame(t, u)
	u.setValue("client:shared:manual", "vendor/one")
	u.clickable("client:shared:add").Click()
	nativeTestFrame(t, u)
	s := u.library.selection
	if len(s.Models) != 1 || s.Initial != "vendor/one" {
		t.Fatal("manual model was not selected")
	}
	u.setValue("client:shared:manual", "invalid model id")
	u.clickable("client:shared:add").Click()
	nativeTestFrame(t, u)
	if len(s.Models) != 1 || s.Initial != "vendor/one" || u.value("client:shared:manual") != "invalid model id" {
		t.Fatal("invalid manual input destroyed the current selection or editable input")
	}
	u.setValue("client:shared:manual", "vendor/second")
	u.clickable("client:shared:add").Click()
	nativeTestFrame(t, u)
	if len(s.Models) != 2 || s.Initial != "vendor/one" {
		t.Fatal("adding another shared model discarded a choice or replaced the initial model")
	}
	for _, key := range []string{"generic", "codex-cli", "claude", "opencode", "omp"} {
		u.page, u.client = "clients", key
		nativeTestFrame(t, u)
		if selected := u.sharedClientSelection(key); !reflect.DeepEqual(selected.ids(), s.ids()) || selected.Initial != s.Initial {
			t.Fatal("agent navigation lost the shared selection", key)
		}
	}
	u.clickable("agents.models.edit").Click()
	nativeTestFrame(t, u)
	if u.page != "models" || u.library.selection != s {
		t.Fatal("returning to Models discarded the shared selection")
	}
}

func TestNativeClientsCatalogRefreshPreservesEditsAndUpdatesMetadata(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "models"
	u.expanded["library.catalog"] = true
	u.client = "codex"
	u.models = nativeClientModelsForTest()
	nativeTestFrame(t, u)
	u.clickable("client:shared:exact").Click()
	nativeTestFrame(t, u)
	u.setValue("client:shared:manual", "vendor/one")
	u.clickable("client:shared:add").Click()
	nativeTestFrame(t, u)
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "name"), "My short name")
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "reasoning"), "high")
	u.setChecked(nativeClientField(sharedModelKey, "vendor/one", "custom"), true)
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "levels"), "low,high")
	newPrice := 4.0
	u.models[0].InputPrice = &newPrice
	u.models[0].ReasoningEfforts = []string{"low"}
	u.models[0].ContextWindow = 99000
	nativeTestFrame(t, u)
	s := u.library.selection
	m := s.choice("vendor/one")
	if m.DisplayName != "My short name" || m.Model.ContextWindow != 99000 || m.ContextPreset != contextPresetRecommended {
		t.Fatal("metadata refresh replaced choices or retained a stale capacity")
	}
	if *m.Model.InputPrice != 4 || !reflect.DeepEqual(m.Model.ReasoningEfforts, []string{"low"}) {
		t.Fatal("selected model retained stale price/capabilities")
	}
	levels, initial := nativeReasoningFor(*m)
	if !m.ReasoningCustom || !reflect.DeepEqual(levels, []string{"low", "high"}) || initial != "high" {
		t.Fatal("catalog refresh discarded explicit reasoning customization")
	}
	visible := nativeVisibleModels(u.models, s, "My short name", true, false, "codeModeRank", "")
	if len(visible) != 1 || *visible[0].InputPrice != 4 {
		t.Fatal("model row did not use refreshed pricing")
	}
}

func TestNativeClientsImportRequiresExplicitReplacement(t *testing.T) {
	u := nativeTestUI(t)
	u.page = "models"
	s := nativeSeedSharedForTest(t, u, u.models...)
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "name"), "Saved profile name")
	nativeTestFrame(t, u)
	u.prepareClient("zed")
	nativeTestWait(t, u, func() bool { return u.clientState().selection("zed").Saved != "" })
	u.importModelLibrary("zed")
	// Import responses are candidates only, including when a shared edit happens
	// before the API completion is drained onto the UI thread.
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "name"), "Keep my latest edit")
	nativeTestFrame(t, u)
	nativeTestWait(t, u, func() bool { return u.library.pendingImport != nil })
	if u.library.selection != s || s.choice("vendor/one").DisplayName != "Keep my latest edit" {
		t.Fatal("import response erased a newer shared edit")
	}
	if u.library.pendingImport.choice("vendor/one").DisplayName != "Saved profile name" {
		t.Fatal("import preview did not show the actual saved profile")
	}
	nativeTestFrame(t, u)
	u.clickable("models.import.cancel").Click()
	nativeTestFrame(t, u)
	if u.library.pendingImport != nil || u.library.selection != s {
		t.Fatal("cancelling import changed the shared library")
	}
	u.importModelLibrary("zed")
	nativeTestWait(t, u, func() bool { return u.library.pendingImport != nil })
	nativeTestFrame(t, u)
	u.clickable("models.import.accept").Click()
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	if u.library.selection == s || u.library.selection.choice("vendor/one").DisplayName != "Saved profile name" || u.owner.modelLibrary.snapshot().Library.Models[0].DisplayName != "Saved profile name" {
		t.Fatal("explicit acceptance did not persist the imported selection")
	}
}
