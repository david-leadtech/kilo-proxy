package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMuseNativeProfilePreflightDoesNotPrepareKiloOrReadModels(t *testing.T) {
	a := launchTestApp(t)
	a.config.MuseCode = musePreferences{Version: 1, Connection: "native"}
	a.apiKey, a.config.OrgID = "", ""
	a.modelLibrary = nil // Native mode must not even read the Kilo selection.
	binary := filepath.Join(a.launcher.home, "synthetic", "client")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var w *httptest.ResponseRecorder
		if method == http.MethodPost {
			body, _ := json.Marshal(museProfileRequest{Preferences: a.config.MuseCode, MuseExecutable: binary})
			w = adminRequest(a, "clients/muse", string(body))
		} else {
			w = httptest.NewRecorder()
			a.museProfileAPI(w, httptest.NewRequest(method, museProfileEndpoint, nil))
		}
		var info museProfileInfo
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || !info.Prepared || info.RequiresPreparation || info.ResolvedExecutable != binary || info.Preferences.Connection != "native" {
			t.Fatal("native preflight changed its contract", w.Code, w.Body.String())
		}
		if info.ProfileDir != "" || len(info.EffectiveModels) != 0 || a.proxyListener != nil {
			t.Fatal("native preflight prepared a private account or gateway", info)
		}
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("native preflight wrote a Kilo profile", entries, err)
	}
}

func TestMuseProfilePreflightRejectsStaleConnectionAndExecutable(t *testing.T) {
	a := launchTestApp(t)
	a.config.MuseCode = musePreferences{Version: 1, Connection: "native"}
	binary := filepath.Join(a.launcher.home, "synthetic", "client")
	for _, input := range []museProfileRequest{
		{Preferences: musePreferences{Version: 1, Connection: "kilo"}, MuseExecutable: binary},
		{Preferences: a.config.MuseCode, MuseExecutable: binary + "-stale"},
		{Preferences: a.config.MuseCode},
	} {
		body, _ := json.Marshal(input)
		w := adminRequest(a, "clients/muse", string(body))
		if w.Code != http.StatusConflict || a.proxyListener != nil {
			t.Fatal("stale native preflight accepted", w.Code, w.Body.String())
		}
	}
}
