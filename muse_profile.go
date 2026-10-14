package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
)

type museProfileRequest struct {
	Preferences    musePreferences `json:"preferences"`
	Library        *modelLibrary   `json:"library,omitempty"`
	MuseExecutable string          `json:"museExecutable"`
}

func decodeMuseProfileRequest(w http.ResponseWriter, r *http.Request, input *museProfileRequest) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		jsonError(w, http.StatusUnsupportedMediaType, "JSON is required.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, modelLibraryLimit+1024)
	data, err := io.ReadAll(r.Body)
	var fields struct {
		Preferences    json.RawMessage `json:"preferences"`
		Library        json.RawMessage `json:"library"`
		MuseExecutable string          `json:"museExecutable"`
	}
	var unique map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err != nil || museManagedDecode(data, &unique) != nil || unique == nil || d.Decode(&fields) != nil || d.Decode(&struct{}{}) != io.EOF {
		jsonError(w, http.StatusBadRequest, "Invalid Muse Code preparation request.")
		return false
	}
	input.Preferences, err = decodeMusePreferences(fields.Preferences)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return false
	}
	input.MuseExecutable = fields.MuseExecutable
	if fields.Library != nil {
		library, err := decodeModelLibrary(fields.Library)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return false
		}
		input.Library = &library
	}
	return true
}

type museModelSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type museProfileInfo struct {
	Preferences         musePreferences    `json:"preferences"`
	Library             modelLibrary       `json:"library"`
	EffectiveModels     []museModelSummary `json:"effectiveModels"`
	ResolvedExecutable  string             `json:"resolvedExecutable"`
	Prepared            bool               `json:"prepared"`
	RequiresPreparation bool               `json:"requiresPreparation"`
	KiloSupported       bool               `json:"kiloSupported"`
	Reason              string             `json:"reason"`
	ProfileDir          string             `json:"profileDir"`
	Message             string             `json:"message"`
}

func (a *app) museProfileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	var input museProfileRequest
	if r.Method == http.MethodPost {
		if !decodeMuseProfileRequest(w, r, &input) {
			return
		}
		if err := validateMusePreferences(input.Preferences); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, http.StatusConflict, "Another agent is being prepared. Try again.")
			return
		}
		defer a.launchMu.Unlock()
	}
	rt := a.launchRuntime()
	binary, resolveErr := rt.resolve("muse", "")
	a.mu.Lock()
	prefs := normalizeMusePreferences(a.config.MuseCode)
	a.mu.Unlock()
	if r.Method == http.MethodPost {
		if prefs != input.Preferences {
			jsonError(w, http.StatusConflict, "The Muse Code connection changed. Reload its settings before preparing it.")
			return
		}
		if resolveErr != nil || input.MuseExecutable == "" || validateCLIExecutable(input.MuseExecutable) != nil || filepath.Clean(input.MuseExecutable) != filepath.Clean(binary) {
			jsonError(w, http.StatusConflict, "The Muse Code executable changed. Refresh installed apps and open it again.")
			return
		}
	}
	info := museProfileInfo{Preferences: prefs, EffectiveModels: []museModelSummary{}, ResolvedExecutable: binary}
	if prefs.Connection == "native" {
		// This is only a preflight. Native Muse retains its own account and does
		// not create a Kilo profile, inspect credentials, or start a process.
		info.Prepared = resolveErr == nil
		if resolveErr != nil {
			info.Reason = resolveErr.Error()
		}
		jsonResponse(w, http.StatusOK, info)
		return
	}
	state := a.modelLibrary.snapshot()
	info.Library = state.Library
	info.ProfileDir = museTerminalHome(a.dir)
	info.KiloSupported = resolveErr == nil
	info.RequiresPreparation = true
	if state.RecoveryRequired || state.Warning != "" || validateModelLibrary(state.Library) != nil || len(state.Library.Models) == 0 {
		info.Reason = "Open Models in Kilo Proxy and save a valid model selection first."
	} else if resolveErr != nil {
		info.Reason = resolveErr.Error()
	}
	if r.Method == http.MethodPost && (input.Library == nil || !reflect.DeepEqual(*input.Library, state.Library)) {
		jsonError(w, http.StatusConflict, errModelLibraryConflict.Error())
		return
	}
	var release string
	if r.Method == http.MethodPost && info.Reason == "" {
		var err error
		release, err = a.checkMuseKiloVersion(binary)
		if err != nil {
			info.Reason = err.Error()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if info.Reason == "" && !a.connectionReadyLocked() {
		info.Reason = "Connect the selected provider in Kilo Proxy first."
	}
	catalog := readNativeCatalogCache(a.dir, a.catalogScopeLocked())
	for _, model := range terminalLibraryChoices(state.Library, catalog) {
		name := model.DisplayName
		if name == "" {
			name = model.Model.Name
		}
		info.EffectiveModels = append(info.EffectiveModels, museModelSummary{ID: model.Model.ID, Name: name})
	}
	if info.Reason == "" && r.Method == http.MethodPost {
		a.modelLibrary.mu.Lock()
		defer a.modelLibrary.mu.Unlock()
		current := a.modelLibrary.snapshotLocked()
		if current.RecoveryRequired || current.Warning != "" || !reflect.DeepEqual(current.Library, state.Library) {
			jsonError(w, http.StatusConflict, errModelLibraryConflict.Error())
			return
		}
		if err := a.prepareMuseTerminalProfile(binary, release, state.Library); err != nil {
			info.Reason = err.Error()
		}
	}
	if info.Reason == "" {
		if err := a.museTerminalPrepared(binary, state.Library); err != nil {
			info.Reason = err.Error()
		} else {
			info.Prepared, info.RequiresPreparation = true, false
			info.Message = "Muse Code prepared with your Kilo models. No Meta login is required."
		}
	}
	if r.Method == http.MethodPost && !info.Prepared {
		jsonError(w, http.StatusConflict, info.Reason)
		return
	}
	jsonResponse(w, http.StatusOK, info)
}
