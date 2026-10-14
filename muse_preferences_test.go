package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMuseConnectionPreferencesPersistWithoutPreparingAccounts(t *testing.T) {
	a := testApp(t)
	a.launcher = &clientLaunchRuntime{resolve: func(string, string) (string, error) {
		t.Fatal("connection preferences must not discover or execute an installation")
		return "", nil
	}}
	for _, connection := range []string{"native", "kilo"} {
		response := adminRequest(a, "clients/muse/preferences", `{"version":1,"connection":"`+connection+`"}`)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var result struct {
			Preferences musePreferences `json:"preferences"`
		}
		if json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Preferences.Connection != connection {
			t.Fatal("save did not confirm the exact connection", response.Body.String())
		}
		saved, err := readSettings(a.dir)
		if err != nil || saved.MuseCode != result.Preferences {
			t.Fatal("connection was not persisted before confirmation", saved.MuseCode, err)
		}
		if a.proxyServer != nil {
			t.Fatal("changing a connection started the proxy")
		}
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "settings.json" {
		t.Fatal("saving a connection prepared or modified an account", entries, err)
	}
}

func TestMuseConnectionPreferencesRejectAmbiguousDocumentsAndConcurrentPrepare(t *testing.T) {
	a := testApp(t)
	for _, body := range []string{
		`null`, `{}`, `{"version":1,"connection":null}`, `{"version":1,"connection":""}`,
		`{"version":1,"connection":"other"}`, `{"version":1,"connection":"kilo","path":"/tmp/other"}`,
		`{"version":1,"connection":"kilo","connection":"native"}`, `{"version":1,"version":2,"connection":"kilo"}`,
		`{"version":1,"connection":"kilo"} {}`, `{"version":"1","connection":"kilo"}`,
	} {
		if response := adminRequest(a, "clients/muse/preferences", body); response.Code != http.StatusBadRequest {
			t.Fatalf("ambiguous document accepted: %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(a.dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("invalid preferences wrote settings", err)
	}
	a.launchMu.Lock()
	response := adminRequest(a, "clients/muse/preferences", `{"version":1,"connection":"native"}`)
	a.launchMu.Unlock()
	if response.Code != http.StatusConflict {
		t.Fatal("connection changed during another preparation", response.Code)
	}
}

func TestMuseConnectionFailedSaveKeepsPreviousChoice(t *testing.T) {
	a := testApp(t)
	a.config.MuseCode = musePreferences{Version: 1, Connection: "native"}
	// A settings destination that is a directory makes the atomic rename fail.
	if err := os.Mkdir(filepath.Join(a.dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(a.dir, "settings.json", "keep")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	response := adminRequest(a, "clients/muse/preferences", `{"version":1,"connection":"kilo"}`)
	if response.Code != http.StatusInternalServerError || a.config.MuseCode.Connection != "native" {
		t.Fatal("failed save changed the active choice", response.Code, a.config.MuseCode)
	}
	request := httptest.NewRequest(http.MethodGet, musePreferencesEndpoint, nil)
	recorder := httptest.NewRecorder()
	a.musePreferencesAPI(recorder, request)
	var result struct {
		Preferences musePreferences `json:"preferences"`
	}
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &result) != nil || result.Preferences.Connection != "native" {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unchanged" {
		t.Fatal("failed save replaced the existing destination", err)
	}
}
