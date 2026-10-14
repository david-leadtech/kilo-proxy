package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func synaraExplicitPreferencesFixture() synaraPreferences {
	p := defaultSynaraPreferences()
	p.CodexModels = "shared"
	p.ClaudeModels = "shared"
	return p
}

func TestSynaraPreferencesFreshDefaultsAndLegacyMigration(t *testing.T) {
	fresh, err := readSettings(t.TempDir())
	if err != nil || fresh.Synara.Version != 1 || fresh.Synara.ClaudeModels != "official" || fresh.Synara.CodexModels != "official" {
		t.Fatalf("fresh defaults: %+v %v", fresh.Synara, err)
	}
	a := synaraAccountPreferenceFixture(t)
	before := changeSynaraAccountSettings(t, a, func(settings map[string]any) {
		instances := settings["providerInstances"].(map[string]any)
		instances[synaraCodexNormalID].(map[string]any)["enabled"] = false
		instances[synaraClaudeNormalID].(map[string]any)["enabled"] = false
		instances[synaraClaudeProxyID].(map[string]any)["enabled"] = false
	})
	marker := filepath.Join(synaraPaths(a.dir).Root, "migration-chat-marker.json")
	if err = os.WriteFile(marker, []byte("synthetic chat stays"), 0600); err != nil {
		t.Fatal(err)
	}
	prefs, err := a.synaraPreferencesSnapshotLocked()
	if err != nil || prefs.Codex || prefs.Claude || prefs.KiloClaude || !prefs.KiloCodex || prefs.CodexModels != "shared" || prefs.ClaudeModels != "shared" {
		t.Fatalf("legacy migration: %+v %v", prefs, err)
	}
	raw, _ := json.Marshal(prefs)
	w := adminRequest(a, "clients/synara/preferences", string(raw))
	if w.Code != 200 {
		t.Fatalf("save migration: %d %s", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(synaraPaths(a.dir).Settings)
	if !bytes.Equal(before, after) {
		t.Fatal("saving preferences rewrote Synara registration")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "synthetic chat stays" {
		t.Fatal("migration changed conversation marker")
	}
	persisted, err := readSettings(a.dir)
	if err != nil || !reflect.DeepEqual(persisted.Synara, prefs) {
		t.Fatalf("not persisted: %+v %v", persisted.Synara, err)
	}
	if w = adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("migrated prepare: %d %s", w.Code, w.Body.String())
	}
	saved, err := a.readSynaraPrepared()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{synaraCodexNormalID: false, synaraClaudeNormalID: false, synaraCodexProxyID: true, synaraClaudeProxyID: false} {
		if saved.Providers[id].(map[string]any)["enabled"] != want {
			t.Fatalf("reactivated %s", id)
		}
	}
}

func TestSynaraPreferencesStrictInputLockAndSaveFailure(t *testing.T) {
	a := synaraTestApp(t, "macos")
	a.config.Synara = synaraExplicitPreferencesFixture()
	before := a.config.Synara
	valid, _ := json.Marshal(before)
	for _, raw := range []string{`{"version":2}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","typo":true}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","openCodeConnection":null}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","codex":null}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","codex":true,"codex":false}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","providers":["muse"]}`, `{"version":1,"codexModels":"shared","claudeModels":"shared","providers":["opencode","opencode"]}`} {
		if w := adminRequest(a, "clients/synara/preferences", raw); w.Code != 400 {
			t.Fatalf("accepted invalid preferences %s: %d", raw, w.Code)
		}
		if !reflect.DeepEqual(before, a.config.Synara) {
			t.Fatal("invalid request changed preferences")
		}
	}
	a.launchMu.Lock()
	w := adminRequest(a, "clients/synara/preferences", string(valid))
	a.launchMu.Unlock()
	if w.Code != 409 {
		t.Fatalf("preparation lock: %d", w.Code)
	}
	original := a.dir
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("own fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	a.dir = blocked
	next := before
	next.Codex = false
	raw, _ := json.Marshal(next)
	w = adminRequest(a, "clients/synara/preferences", string(raw))
	a.dir = original
	if w.Code != 500 || !reflect.DeepEqual(before, a.config.Synara) {
		t.Fatalf("failed save activated settings: %d %+v", w.Code, a.config.Synara)
	}
}

func TestSynaraPreferencesOmittedConnectionPreservesExplicitChoice(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := synaraExplicitPreferencesFixture()
	prefs.Providers = []string{"opencode"}
	prefs.OpenCodeConnection = "kilo"
	a.config.Synara = prefs
	raw := `{"version":1,"codex":true,"claude":true,"kiloCodex":true,"kiloClaude":true,"codexModels":"shared","claudeModels":"shared","providers":["opencode"],"openCode2":false}`
	if w := adminRequest(a, "clients/synara/preferences", raw); w.Code != 200 || a.config.Synara.OpenCodeConnection != "kilo" {
		t.Fatalf("omitted connection changed selection: %d %+v", w.Code, a.config.Synara)
	}
	raw = strings.TrimSuffix(raw, "}") + `,"openCodeConnection":""}`
	if w := adminRequest(a, "clients/synara/preferences", raw); w.Code != 200 || a.config.Synara.OpenCodeConnection != "native" {
		t.Fatalf("explicit automatic/native reset failed: %d %+v", w.Code, a.config.Synara)
	}
}

func TestSynaraNativeOnlyDoesNotRequireDisabledCLIsOrGateway(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "none"} {
		t.Run(kind, func(t *testing.T) {
			a := synaraTestApp(t, "macos")
			prefs := synaraExplicitPreferencesFixture()
			prefs.Codex = kind == "codex"
			prefs.Claude = kind == "claude"
			prefs.KiloCodex = false
			prefs.KiloClaude = false
			a.config.Synara = prefs
			a.apiKey = ""
			a.config.Port = 0
			old := a.launcher.resolve
			calls := map[string]int{}
			a.launcher.resolve = func(id, custom string) (string, error) {
				calls[id]++
				if id == "codex-cli" && !prefs.Codex || id == "claude" && !prefs.Claude {
					return "", errors.New("disabled fixture CLI unavailable")
				}
				return old(id, custom)
			}
			if _, err := a.modelLibrary.save(emptyModelLibrary(), a.modelLibrary.snapshot().Revision, false); err != nil {
				t.Fatal(err)
			}
			w := adminRequest(a, "clients/synara", `{}`)
			if w.Code != 200 {
				t.Fatalf("native-only prepare: %d %s", w.Code, w.Body.String())
			}
			saved, err := a.readSynaraPrepared()
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.Library.Models) != 0 || saved.Preferences == nil || !a.synaraReady(saved, mustSynaraFixtureBinary(t, a), a.launchRuntime()) {
				t.Fatal("native-only profile is not ready")
			}
			if !prefs.Codex && calls["codex-cli"] != 0 || !prefs.Claude && calls["claude"] != 0 {
				t.Fatalf("resolved disabled CLI: %+v", calls)
			}
			for path := range saved.Files {
				if strings.Contains(path, "profiles") || strings.Contains(path, "secrets") {
					t.Fatalf("native-only manifest contains Kilo file %s", path)
				}
			}
			plan, err := a.planClientLaunch(clientLaunchRequest{Client: "synara"}, a.launchRuntime())
			if err != nil || !plan.skipProxy {
				t.Fatalf("native-only launch requires proxy: %+v %v", plan, err)
			}
			if a.proxyListener != nil {
				t.Fatal("native preparation started proxy")
			}
		})
	}
}

func mustSynaraFixtureBinary(t *testing.T, a *app) string {
	t.Helper()
	binary, err := a.launchRuntime().resolve("synara", "")
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func TestSynaraModelPolicyNativeClaudeAndProviderIsolation(t *testing.T) {
	prefs := defaultSynaraPreferences()
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "openai/gpt-selected", Models: []modelLibraryItem{{ID: "openai/gpt-selected"}, {ID: "vendor/unrelated"}}}
	before := cloneModelLibrary(library)
	policy, err := deriveSynaraModelPolicy(prefs, library, t3CodeOfficialCatalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	picker := t3CodeClaudePickerLibrary(policy.ClaudeLibrary, policy.ClaudeRoutes)
	want := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1"}
	if len(picker.Models) != len(want) {
		t.Fatalf("wrong Claude count: %+v", picker)
	}
	for i, id := range want {
		if picker.Models[i].ID != id {
			t.Fatalf("custom Claude picker identity %s", picker.Models[i].ID)
		}
		exact, err := t3CodeResolveModel(policy.ClaudeLibrary, policy.ClaudeRoutes, id)
		if err != nil || exact != policy.ClaudeLibrary.Models[i].ID {
			t.Fatalf("wrong exact route: %s %v", exact, err)
		}
	}
	if len(policy.CodexLibrary.Models) != 1 || policy.CodexLibrary.Models[0].ID != "openai/gpt-selected" || !reflect.DeepEqual(library, before) {
		t.Fatal("provider policies changed shared selection")
	}
	prefs.Codex, prefs.KiloCodex, prefs.KiloClaude = false, false, false
	if _, err = deriveSynaraModelPolicy(prefs, emptyModelLibrary(), nil); err != nil {
		t.Fatal("native-only policy requires gateway models", err)
	}
}
