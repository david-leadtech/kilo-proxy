package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureManagedToolCatalog struct {
	calls   atomic.Int32
	channel string
	gate    <-chan struct{}
}

func (c *fixtureManagedToolCatalog) Latest(ctx context.Context, client, platform, arch, channel string) (managedToolRelease, error) {
	c.calls.Add(1)
	if c.gate != nil {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return managedToolRelease{}, errors.New("Version check cancelled.")
		}
	}
	if c.channel != "" && channel != c.channel {
		return managedToolRelease{}, errors.New("The chosen channel is not available.")
	}
	return managedToolRelease{Client: client, Version: "1.2.3", Channel: channel, URL: "https://github.com/vendor/tool/releases/download/v1.2.3/tool.zip", SHA256: strings.Repeat("a", 64), Format: "zip"}, nil
}

func managedToolsFixture(t *testing.T) (*app, *managedToolsManager, *fixtureManagedToolCatalog) {
	t.Helper()
	a := testApp(t)
	a.launcher = &clientLaunchRuntime{platform: "linux", resolve: func(string, string) (string, error) { return "", errors.New("not installed") }, terminal: func() (bool, string) { return true, "" }}
	m := a.toolsManager()
	catalog := &fixtureManagedToolCatalog{}
	m.catalog = catalog
	t.Cleanup(m.cancel)
	return a, m, catalog
}

func waitManagedTool(t *testing.T, m *managedToolsManager, client string) managedToolStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := m.snapshot().Clients[client]
		if !s.Busy {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("tool operation did not finish")
	return managedToolStatus{}
}

func TestManagedToolAPIPlanAuthenticationAndFixedCommands(t *testing.T) {
	a, m, catalog := managedToolsFixture(t)
	for _, path := range []string{managedToolsStatusEndpoint, managedToolsCheckEndpoint, managedToolsInstallEndpoint, "/api/tools/cancel"} {
		r := httptest.NewRequest(http.MethodPost, "http://"+a.adminHost+path, strings.NewReader(`{"client":"codex-cli"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.adminHandler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	for _, body := range []string{`{"client":"unknown"}`, `{"client":"codex-cli","command":"bad"}`, `{"client":"codex-cli","url":"https://evil.example/tool"}`, `{"client":"codex-cli","channel":"../../main"}`} {
		w := adminRequest(a, "tools/check", body)
		if w.Code < 400 {
			t.Fatal("accepted arbitrary installer input", body, w.Code)
		}
	}
	if catalog.calls.Load() != 0 {
		t.Fatal("invalid requests contacted vendor")
	}
	w := adminRequest(a, "tools/status", "")
	if w.Code != 200 || catalog.calls.Load() != 0 {
		t.Fatal("status must not contact vendors")
	}
	w = adminRequest(a, "tools/check", `{"client":"codex-cli"}`)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s := waitManagedTool(t, m, "codex-cli")
	if !s.CanInstall || s.CanUpdate || s.PlanID == "" || s.LatestVersion != "1.2.3" || s.Managed {
		t.Fatalf("bad installation plan: %+v", s)
	}
	for _, body := range []string{`{"client":"codex-cli","planID":"wrong","confirm":true}`, `{"client":"codex-cli","planID":"` + s.PlanID + `"}`} {
		w = adminRequest(a, "tools/install", body)
		if w.Code != 409 {
			t.Fatal("installation did not require exact plan and confirmation", w.Code)
		}
	}
	var installs atomic.Int32
	m.install = func(ctx context.Context, a *app, r managedToolRelease, phase func(string)) (managedToolRecord, error) {
		installs.Add(1)
		return managedToolRecord{Client: r.Client, Version: r.Version, Channel: r.Channel, Executable: "/synthetic/owned/codex"}, nil
	}
	request, _ := json.Marshal(map[string]any{"client": "codex-cli", "planID": s.PlanID, "confirm": true})
	w = adminRequest(a, "tools/install", string(request))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s = waitManagedTool(t, m, "codex-cli")
	if installs.Load() != 1 || !s.Managed || !s.Installed || s.InstalledVersion != "1.2.3" || s.Phase != "complete" || s.PlanID != "" {
		t.Fatalf("bad completed installation: %+v", s)
	}
	w = adminRequest(a, "tools/install", string(request))
	if w.Code != 409 || installs.Load() != 1 {
		t.Fatal("replayed installation plan")
	}
}

func TestManagedToolsChannelAndConcurrentInstallation(t *testing.T) {
	a, m, catalog := managedToolsFixture(t)
	catalog.channel = "nightly"
	if err := m.check("t3-code", "nightly"); err != nil {
		t.Fatal(err)
	}
	s := waitManagedTool(t, m, "t3-code")
	if s.Channel != "nightly" || s.PlanID == "" {
		t.Fatalf("wrong channel: %+v", s)
	}
	gate := make(chan struct{})
	entered := make(chan struct{})
	m.install = func(ctx context.Context, a *app, r managedToolRelease, phase func(string)) (managedToolRecord, error) {
		close(entered)
		phase("verifying")
		select {
		case <-gate:
			return managedToolRecord{Version: r.Version, Channel: r.Channel}, nil
		case <-ctx.Done():
			return managedToolRecord{}, ctx.Err()
		}
	}
	if err := m.start("t3-code", s.PlanID); err != nil {
		t.Fatal(err)
	}
	<-entered
	if a.launchMu.TryLock() {
		a.launchMu.Unlock()
		t.Fatal("installation did not serialize with profile preparation and launch")
	}
	if err := m.check("t3-code", "stable"); err == nil {
		t.Fatal("changed plan during installation")
	}
	w := adminRequest(a, "tools/cancel", `{"client":"synara"}`)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	if !m.snapshot().Clients["t3-code"].Busy {
		t.Fatal("cancelled a different tool's operation")
	}
	w = adminRequest(a, "tools/cancel", `{"client":"t3-code"}`)
	if w.Code != 202 {
		t.Fatal(w.Code)
	}
	s = waitManagedTool(t, m, "t3-code")
	if s.Phase != "failed" || !strings.Contains(s.Error, "cancelled") {
		t.Fatalf("bad cancellation: %+v", s)
	}
}

func TestManagedToolPlanNeverDowngradesWithinInstalledChannel(t *testing.T) {
	for _, tc := range []struct {
		version, channel, selected string
		update                     bool
	}{
		{"1.2.4", "stable", "stable", false},
		{"1.2.3", "stable", "stable", false},
		{"1.2.2", "stable", "stable", true},
		{"1.2.4-nightly.1", "nightly", "stable", true},
	} {
		t.Run(tc.version+"/"+tc.channel, func(t *testing.T) {
			a, m, _ := managedToolsFixture(t)
			record := managedToolRegistryFixture(t, a.dir, "codex-cli", "existing")
			record.Version, record.Channel = tc.version, tc.channel
			if err := writeManagedTool(a.dir, record); err != nil {
				t.Fatal(err)
			}
			if err := m.check("codex-cli", tc.selected); err != nil {
				t.Fatal(err)
			}
			status := waitManagedTool(t, m, "codex-cli")
			if status.CanUpdate != tc.update || status.CanInstall || (status.PlanID != "") != tc.update {
				t.Fatalf("incorrect update plan: %+v", status)
			}
		})
	}
}

func TestManagedToolsRejectChangedInstallationAndWindows(t *testing.T) {
	a, m, _ := managedToolsFixture(t)
	if err := m.check("codex-cli", ""); err != nil {
		t.Fatal(err)
	}
	s := waitManagedTool(t, m, "codex-cli")
	a.launcher.resolve = func(string, string) (string, error) { return "/synthetic/moved/codex", nil }
	var installs atomic.Int32
	m.install = func(context.Context, *app, managedToolRelease, func(string)) (managedToolRecord, error) {
		installs.Add(1)
		return managedToolRecord{}, nil
	}
	if err := m.start("codex-cli", s.PlanID); err != nil {
		t.Fatal(err)
	}
	s = waitManagedTool(t, m, "codex-cli")
	if installs.Load() != 0 || s.Phase != "failed" || !strings.Contains(s.Error, "changed") {
		t.Fatal("changed installation was replaced", s)
	}
	m.platform = "windows"
	if err := m.start("codex-cli", "anything"); err == nil || !strings.Contains(err.Error(), "manually") {
		t.Fatal("Windows installation was offered", err)
	}
}
