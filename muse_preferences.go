package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const musePreferencesEndpoint = "/api/clients/muse/preferences"
const museProfileEndpoint = "/api/clients/muse"

// The connection choice never changes the selected installation or the normal
// Meta account. Kilo prepares its own state only on an explicit prepare/open.
type musePreferences struct {
	Version    int    `json:"version"`
	Connection string `json:"connection"`
}

func normalizeMusePreferences(p musePreferences) musePreferences {
	if p.Version == 0 && p.Connection == "" {
		return musePreferences{Version: 1, Connection: "kilo"}
	}
	return p
}

func validateMusePreferences(p musePreferences) error {
	if p.Version != 1 || (p.Connection != "kilo" && p.Connection != "native") {
		return errors.New("Choose Kilo Proxy models or your own Meta account for Muse Code.")
	}
	return nil
}

func decodeMusePreferences(raw json.RawMessage) (musePreferences, error) {
	var p musePreferences
	decoder := json.NewDecoder(bytes.NewReader(raw))
	invalid := errors.New("Invalid Muse Code connection settings.")
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return p, invalid
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return p, invalid
		}
		seen[name] = true
		switch name {
		case "version":
			err = decoder.Decode(&p.Version)
		case "connection":
			err = decoder.Decode(&p.Connection)
		default:
			return p, invalid
		}
		if err != nil {
			return p, invalid
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || decoder.Decode(&struct{}{}) != io.EOF {
		return p, errors.New("Invalid Muse Code connection settings.")
	}
	return p, validateMusePreferences(p)
}

func (a *app) musePreferencesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var input musePreferences
	if r.Method == http.MethodPost {
		var raw json.RawMessage
		if !decodeBody(w, r, &raw) {
			return
		}
		var err error
		input, err = decodeMusePreferences(raw)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, http.StatusConflict, "Another agent is being prepared. Try again.")
			return
		}
		defer a.launchMu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	prefs := normalizeMusePreferences(a.config.MuseCode)
	if r.Method == http.MethodPost && input != prefs {
		next := a.config
		next.MuseCode = input
		if err := writeSettings(a.dir, next); err != nil {
			jsonError(w, http.StatusInternalServerError, "Could not save Muse Code settings. Your previous settings are unchanged.")
			return
		}
		a.config, prefs = next, input
	}
	jsonResponse(w, http.StatusOK, map[string]any{"preferences": prefs})
}
