package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
)

const omnigentPreferencesEndpoint = "/api/clients/omnigent/preferences"

// Each harness has one connection. Account changes apply to new sessions in
// the private Omnigent workspace, never to the user's normal installation.
type omnigentPreferences struct {
	Version          int    `json:"version"`
	Codex            bool   `json:"codex"`
	Claude           bool   `json:"claude"`
	CodexConnection  string `json:"codexConnection"`
	ClaudeConnection string `json:"claudeConnection"`
	CodexModels      string `json:"codexModels"`
	ClaudeModels     string `json:"claudeModels"`
}

func normalizeOmnigentPreferences(p omnigentPreferences) omnigentPreferences {
	if p.Version == 0 {
		return omnigentPreferences{Version: 1, Codex: true, Claude: true, CodexConnection: "kilo", ClaudeConnection: "kilo", CodexModels: "shared", ClaudeModels: "shared"}
	}
	return p
}

func validateOmnigentPreferences(p omnigentPreferences) error {
	if p.Version != 1 || (p.CodexConnection != "native" && p.CodexConnection != "kilo") || (p.ClaudeConnection != "native" && p.ClaudeConnection != "kilo") || (p.CodexModels != "official" && p.CodexModels != "shared") || (p.ClaudeModels != "official" && p.ClaudeModels != "shared") {
		return errors.New("Choose a native or Kilo connection and supported models for each Omnigent agent.")
	}
	return nil
}

func omnigentUsesKilo(p omnigentPreferences) bool {
	p = normalizeOmnigentPreferences(p)
	return p.Codex && p.CodexConnection == "kilo" || p.Claude && p.ClaudeConnection == "kilo"
}

func decodeOmnigentPreferences(raw json.RawMessage) (omnigentPreferences, error) {
	var p omnigentPreferences
	// Reject duplicate and null members before decoding typed preferences.
	object, err := decodeClaudeDesktopObject(raw)
	if err != nil || len(object) != 7 {
		return p, errors.New("Invalid Omnigent preferences.")
	}
	for _, field := range []string{"version", "codex", "claude", "codexConnection", "claudeConnection", "codexModels", "claudeModels"} {
		value, exists := object[field]
		if !exists || string(value) == "null" {
			return p, errors.New("Invalid Omnigent preferences.")
		}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, errors.New("Invalid Omnigent preferences.")
	}
	return p, validateOmnigentPreferences(p)
}

func (a *app) omnigentPreferencesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input omnigentPreferences
	if r.Method == http.MethodPost {
		var raw json.RawMessage
		if !decodeBody(w, r, &raw) {
			return
		}
		var err error
		input, err = decodeOmnigentPreferences(raw)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another launch is already being prepared.")
			return
		}
		defer a.launchMu.Unlock()
		if err := a.omnigentIdle(); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	prefs := normalizeOmnigentPreferences(a.config.Omnigent)
	if r.Method == http.MethodPost && !reflect.DeepEqual(input, prefs) {
		next := a.config
		next.Omnigent = input
		if writeSettings(a.dir, next) != nil {
			jsonError(w, 500, "Could not save Omnigent settings. Your previous settings are unchanged.")
			return
		}
		a.config, prefs = next, input
	}
	saved, err := a.readOmnigentPrepared()
	ready := err == nil && a.omnigentReady(saved, a.launchRuntime())
	jsonResponse(w, 200, map[string]any{"preferences": prefs, "requiresPreparation": !ready})
}
