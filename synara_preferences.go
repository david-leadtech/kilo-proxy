package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
)

const synaraPreferencesEndpoint = "/api/clients/synara/preferences"

// Synara has its own preferences and account identities. Version zero retains
// the existing shared-model integration until the user saves these controls.
type synaraPreferences struct {
	Version            int      `json:"version"`
	Codex              bool     `json:"codex"`
	Claude             bool     `json:"claude"`
	KiloCodex          bool     `json:"kiloCodex"`
	KiloClaude         bool     `json:"kiloClaude"`
	CodexModels        string   `json:"codexModels"`
	ClaudeModels       string   `json:"claudeModels"`
	Providers          []string `json:"providers"`
	OpenCode2          bool     `json:"openCode2"`
	OpenCodeConnection string   `json:"openCodeConnection"`
}

func normalizeSynaraPreferences(p synaraPreferences) synaraPreferences {
	if p.Version == 0 {
		return synaraPreferences{Version: 1, Codex: true, Claude: true, KiloCodex: true, KiloClaude: true, CodexModels: "shared", ClaudeModels: "shared", Providers: []string{}, OpenCodeConnection: "native"}
	}
	if p.Providers == nil {
		p.Providers = []string{}
	} else {
		p.Providers = append([]string{}, p.Providers...)
		sort.Strings(p.Providers)
	}
	if p.OpenCodeConnection == "" {
		p.OpenCodeConnection = "native"
	}
	return p
}

func synaraT3Preferences(p synaraPreferences) t3CodePreferences {
	p = normalizeSynaraPreferences(p)
	return t3CodePreferences{Version: 1, Codex: p.Codex, Claude: p.Claude, KiloCodex: p.KiloCodex, KiloClaude: p.KiloClaude, CodexModels: p.CodexModels, ClaudeModels: p.ClaudeModels, Providers: append([]string{}, p.Providers...), OpenCode2: p.OpenCode2, OpenCodeConnection: p.OpenCodeConnection, MuseConnection: "native"}
}

func validateSynaraPreferences(p synaraPreferences) error {
	if p.Version != 1 || (p.CodexModels != "official" && p.CodexModels != "shared") || (p.ClaudeModels != "official" && p.ClaudeModels != "shared") {
		return errors.New("Invalid Synara provider or model settings.")
	}
	if p.OpenCodeConnection != "" && p.OpenCodeConnection != "native" && p.OpenCodeConnection != "kilo" {
		return errors.New("Choose the native OpenCode account or Kilo Proxy models.")
	}
	seen := map[string]bool{}
	for _, id := range p.Providers {
		if seen[id] || (id != "cursor" && id != "grok" && id != "opencode" && id != "antigravity" && id != "pi") {
			return errors.New("Choose unique supported Synara providers.")
		}
		seen[id] = true
	}
	if p.OpenCode2 && !seen["opencode"] {
		return errors.New("Enable OpenCode in Synara before choosing OpenCode 2.")
	}
	return nil
}

func synaraUsesKilo(p synaraPreferences) bool {
	p = normalizeSynaraPreferences(p)
	return p.KiloCodex || p.KiloClaude || synaraOpenCodeKiloEnabled(p)
}

func defaultSynaraPreferences() synaraPreferences {
	p := normalizeSynaraPreferences(synaraPreferences{})
	p.CodexModels = "official"
	p.ClaudeModels = "official"
	return p
}

func synaraCoreT3Preferences(p synaraPreferences) t3CodePreferences {
	result := synaraT3Preferences(p)
	result.Providers = []string{}
	result.OpenCode2 = false
	result.OpenCodeConnection = "native"
	return result
}

func preserveSynaraConnection(p synaraPreferences, raw json.RawMessage, current synaraPreferences) synaraPreferences {
	object, _ := decodeClaudeDesktopObject(raw)
	if _, exists := object["openCodeConnection"]; !exists {
		p.OpenCodeConnection = normalizeSynaraPreferences(current).OpenCodeConnection
	}
	return normalizeSynaraPreferences(p)
}

func decodeSynaraPreferences(raw json.RawMessage) (synaraPreferences, error) {
	var p synaraPreferences
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return p, errors.New("Invalid Synara preferences.")
	}
	object, err := decodeClaudeDesktopObject(raw)
	if err != nil {
		return p, errors.New("Invalid Synara preferences.")
	}
	for _, value := range object {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return p, errors.New("Invalid Synara preferences.")
		}
	}
	return p, validateSynaraPreferences(p)
}

// Read only the owned provider registration, never CLI configuration or login.
// Legacy per-account toggles become the initial controls without rewriting any
// conversation, draft, disabled profile or secret.
func (a *app) synaraPreferencesSnapshotLocked() (synaraPreferences, error) {
	if a.config.Synara.Version != 0 {
		p := normalizeSynaraPreferences(a.config.Synara)
		return p, validateSynaraPreferences(p)
	}
	p := normalizeSynaraPreferences(synaraPreferences{})
	data, err := readCatalogFile(synaraPaths(a.dir).Settings)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, errors.New("Cannot safely read the private Synara settings.")
	}
	_, settings, err := synaraSettingsDocument(data)
	if err != nil {
		return p, err
	}
	var instances map[string]map[string]json.RawMessage
	if raw, ok := settings["providerInstances"]; ok && json.Unmarshal(raw, &instances) != nil {
		return p, errors.New("Invalid private Synara provider settings.")
	}
	for id, target := range map[string]*bool{synaraCodexNormalID: &p.Codex, synaraClaudeNormalID: &p.Claude, synaraCodexProxyID: &p.KiloCodex, synaraClaudeProxyID: &p.KiloClaude} {
		if raw, ok := instances[id]["enabled"]; ok {
			if json.Unmarshal(raw, target) != nil {
				return p, errors.New("Invalid private Synara provider settings.")
			}
		}
	}
	return p, nil
}

func (a *app) saveSynaraPreferencesLocked(p synaraPreferences) error {
	p = normalizeSynaraPreferences(p)
	if err := validateSynaraPreferences(p); err != nil {
		return err
	}
	if reflect.DeepEqual(a.config.Synara, p) {
		return nil
	}
	next := a.config
	next.Synara = p
	if err := writeSettings(a.dir, next); err != nil {
		return errors.New("Could not save Synara settings. Your previous settings are unchanged.")
	}
	a.config = next
	return nil
}

func (a *app) synaraPreferencesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input synaraPreferences
	var raw json.RawMessage
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &raw) {
			return
		}
		var err error
		input, err = decodeSynaraPreferences(raw)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another agent is being prepared. Try again.")
			return
		}
		defer a.launchMu.Unlock()
	}
	rt := a.launchRuntime()
	binary, resolveErr := rt.resolve("synara", "")
	a.mu.Lock()
	defer a.mu.Unlock()
	prefs, err := a.synaraPreferencesSnapshotLocked()
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	if r.Method == http.MethodPost {
		input = preserveSynaraConnection(input, raw, prefs)
		if err = a.saveSynaraPreferencesLocked(input); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
		prefs = input
	}
	saved, readErr := a.readSynaraPrepared()
	ready := resolveErr == nil && readErr == nil && a.synaraReady(saved, binary, rt)
	policy, _ := deriveSynaraModelPolicy(prefs, a.modelLibrary.snapshot().Library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()))
	response := map[string]any{"preferences": prefs, "requiresPreparation": !ready, "supportedProviders": []any{}, "openCode2Supported": false, "openCodeKiloSupported": false, "museKiloSupported": false, "effectiveCodexModels": synaraModelSummaries(policy.CodexLibrary), "effectiveClaudeModels": synaraModelSummaries(policy.ClaudeLibrary), "effectiveOpenCodeModels": synaraModelSummaries(policy.OpenCodeLibrary)}
	if caps, err := synaraProviderCapabilities(binary, rt.platform); err == nil {
		for key, value := range caps {
			response[key] = value
		}
	}
	jsonResponse(w, 200, response)
}

func synaraResponseCapabilities(response map[string]any, binary, platform string) map[string]any {
	response["supportedProviders"] = []t3CodeProviderSupport{}
	response["openCode2Supported"], response["openCodeKiloSupported"], response["claudePickerSupported"] = false, false, false
	if capabilities, err := synaraProviderCapabilities(binary, platform); err == nil {
		for _, name := range []string{"supportedProviders", "openCode2Supported", "openCodeKiloSupported", "claudePickerSupported"} {
			if value, ok := capabilities[name]; ok {
				response[name] = value
			}
		}
	}
	return response
}
