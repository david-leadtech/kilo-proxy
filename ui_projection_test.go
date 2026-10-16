package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUIProjectionCommittedConnection(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	initial := a.readUIProjection()
	payload := map[string]any{"apiKey": "synthetic-projection-key", "orgId": "projection-team", "port": initial.Port, "remember": false}
	data, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.saveConfig(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save failed: %s", w.Body.String())
	}
	p := a.readUIProjection()
	a.mu.Lock()
	encoded, _ := json.Marshal([]any{a.config.Port, a.config.OrgID, a.config.LocalKey, a.apiKey})
	a.mu.Unlock()
	if !p.HasKey || !p.HasOrg || !p.KiloReady || p.Fingerprint == initial.Fingerprint || p.Fingerprint != sha256.Sum256(encoded) {
		t.Fatal("successful config save did not publish its exact connection")
	}
	retained := p
	a.forget(httptest.NewRecorder())
	p = a.readUIProjection()
	if p.HasKey || p.KiloReady || p.Fingerprint == retained.Fingerprint || retained.Fingerprint != sha256.Sum256(encoded) {
		t.Fatal("forget did not revoke connection or mutated a retained snapshot")
	}
}

func TestUIProjectionReadsDoNotWaitForBackendLocks(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	a.chatgpt.mu.Lock()
	a.chatgpt.creds = chatGPTCredentials{Access: "synthetic-access", Refresh: "synthetic-refresh", Account: "synthetic-account", Expires: 1}
	a.chatgpt.publishUIProjectionLocked()
	a.chatgpt.mu.Unlock()
	a.mu.Lock()
	a.chatgpt.mu.Lock()
	done := make(chan struct{})
	go func() {
		_ = a.readUIProjection()
		if !a.uiConnectionReady() || a.uiCatalogScope() != "|chatgpt:synthetic-account" {
			t.Error("published subscription was lost")
		}
		close(done)
	}()
	blocked := false
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		blocked = true
	}
	a.chatgpt.mu.Unlock()
	a.mu.Unlock()
	<-done
	if blocked {
		t.Fatal("atomic UI reads waited for backend locks")
	}
}

func TestUIProjectionAuthenticationTransitions(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.apiKey, a.config.OrgID = "synthetic-key", "synthetic-team"
	session := &loginSession{Status: "starting", cancel: func() {}}
	a.login = session
	a.publishUIProjectionLocked()
	a.mu.Unlock()
	if a.uiConnectionReady() {
		t.Fatal("pending authentication appeared ready")
	}
	a.finishLogin(session, "error", "synthetic failure")
	if !a.uiConnectionReady() {
		t.Fatal("finished authentication was not published")
	}
	a.mu.Lock()
	a.login = &loginSession{Status: "pending", cancel: func() {}}
	a.publishUIProjectionLocked()
	a.mu.Unlock()
	a.cancelLogin()
	if !a.uiConnectionReady() {
		t.Fatal("cancelled authentication was not published")
	}
}

func TestUIProjectionPreferenceCommit(t *testing.T) {
	a, err := newApp(t.TempDir(), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, body string
		handler    http.HandlerFunc
	}{
		{"/api/tray-settings", `{"display":"spend"}`, a.traySettings},
		{"/api/image-transport-settings", `{"mode":"off","profile":"high"}`, a.imageTransportSettings},
	} {
		req := httptest.NewRequest(http.MethodPut, tc.path, bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		tc.handler(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %s", tc.path, w.Body.String())
		}
	}
	p := a.readUIProjection()
	if p.TrayDisplay != "spend" || p.ImageTransport.Mode != "off" {
		t.Fatal("confirmed preferences were not published")
	}
}
