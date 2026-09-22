package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func imageTransportSettingsRequest(a *app, method, body, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+a.adminHost+"/api/image-transport-settings", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.adminHandler().ServeHTTP(w, r)
	return w
}

func TestImageTransportSettingsDefaultAndPersistence(t *testing.T) {
	a := testApp(t)
	a.config.Language, a.config.OrgID = "es", "test-org"
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "openai/test-image"}
	before := a.config
	if a.config.ImageTransport.Mode != "off" {
		t.Fatal("image uploads must require opt-in")
	}
	if w := imageTransportSettingsRequest(a, http.MethodGet, "", a.adminToken); w.Code != 200 || !strings.Contains(w.Body.String(), `"mode":"off","profile":"high"`) {
		t.Fatalf("default image preference: %d %s", w.Code, w.Body.String())
	}
	for _, preference := range []imageTransportSettings{{"compress", "high"}, {"compress", "balanced"}, {"compress", "small"}, {"upload", "small"}, {"off", "small"}} {
		body, _ := json.Marshal(preference)
		w := imageTransportSettingsRequest(a, http.MethodPut, string(body), a.adminToken)
		if w.Code != 200 {
			t.Fatalf("save preference: %d %s", w.Code, w.Body.String())
		}
		before.ImageTransport = preference
		if !reflect.DeepEqual(a.config, before) {
			t.Fatal("image preference changed another setting")
		}
		restarted, err := newApp(a.dir, &fakeVault{values: map[string]string{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(restarted.stop)
		if !reflect.DeepEqual(restarted.config, before) {
			t.Fatal("reopening lost the image preference or another setting")
		}
		state := httptest.NewRecorder()
		a.state(state)
		var data map[string]any
		if json.Unmarshal(state.Body.Bytes(), &data) != nil || !reflect.DeepEqual(data["imageTransport"], map[string]any{"mode": preference.Mode, "profile": preference.Profile}) {
			t.Fatal("state does not reflect the preference")
		}
	}
}

func TestImageTransportSettingsRejectInvalidInputAndAuth(t *testing.T) {
	a := testApp(t)
	before := a.config
	for _, tc := range []struct {
		method, body, token string
		status              int
	}{
		{http.MethodGet, "", "wrong-token", 401},
		{http.MethodPut, `{"mode":"compress","profile":"high"}`, "wrong-token", 401},
		{http.MethodPost, `{"mode":"compress","profile":"high"}`, a.adminToken, 405},
		{http.MethodPut, `{}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"off"}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"invalid","profile":"high"}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"upload","profile":null}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":null,"profile":"high"}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"upload","profile":"invalid"}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"upload","profile":"high","port":8888}`, a.adminToken, 400},
		{http.MethodPut, `{"mode":"upload","profile":"high"} {}`, a.adminToken, 400},
	} {
		w := imageTransportSettingsRequest(a, tc.method, tc.body, tc.token)
		if w.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.body, w.Code, tc.status)
		}
	}
	if !reflect.DeepEqual(a.config, before) {
		t.Fatal("a rejected request changed settings")
	}
}

func TestImageTransportSettingsFailedWriteDoesNotApply(t *testing.T) {
	a := testApp(t)
	before := a.config
	if err := os.Mkdir(filepath.Join(a.dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	w := imageTransportSettingsRequest(a, http.MethodPut, `{"mode":"compress","profile":"high"}`, a.adminToken)
	if w.Code != 500 || !reflect.DeepEqual(a.config, before) {
		t.Fatalf("failed persistence applied a preference: %d", w.Code)
	}
}

func TestImageTransportSettingsLegacyDefaultsOff(t *testing.T) {
	a := testApp(t)
	encoded, _ := json.Marshal(a.config)
	var legacy map[string]any
	_ = json.Unmarshal(encoded, &legacy)
	delete(legacy, "imageTransport")
	encoded, _ = json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(a.dir, "settings.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSettings(a.dir)
	if err != nil || loaded.ImageTransport != (imageTransportSettings{Mode: "off", Profile: "high"}) {
		t.Fatalf("legacy settings enabled uploads: %+v %v", loaded, err)
	}
}

func TestImageTransportSettingsDisablingPreservesCleanupWarning(t *testing.T) {
	a := testApp(t)
	a.imageUploadWarning = "Synthetic cleanup warning"
	w := imageTransportSettingsRequest(a, http.MethodPut, `{"mode":"off","profile":"high"}`, a.adminToken)
	if w.Code != 200 || a.imageUploadWarning == "" {
		t.Fatal("disabling uploads discarded the cleanup warning")
	}
	state := httptest.NewRecorder()
	a.state(state)
	if !strings.Contains(state.Body.String(), `"imageUploadWarning":"Synthetic cleanup warning"`) {
		t.Fatal("state hid the cleanup warning")
	}
}

func TestImageTransportSettingsReadValidation(t *testing.T) {
	for _, preference := range []imageTransportSettings{{"invalid", "high"}, {"off", "invalid"}} {
		a := testApp(t)
		a.config.ImageTransport = preference
		if err := writeSettings(a.dir, a.config); err != nil {
			t.Fatal(err)
		}
		if _, err := readSettings(a.dir); err == nil {
			t.Fatal("invalid image settings accepted")
		}
	}
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{}
	if err := writeSettings(a.dir, a.config); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSettings(a.dir)
	if err != nil || loaded.ImageTransport != (imageTransportSettings{"off", "high"}) {
		t.Fatalf("empty values not normalized: %+v %v", loaded.ImageTransport, err)
	}
}
