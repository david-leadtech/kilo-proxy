package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSynaraExplicitProfileBindsActiveFilesAndEffectiveLibrary(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := synaraExplicitPreferencesFixture()
	prefs.Codex = false
	prefs.Claude = false
	prefs.KiloClaude = false
	a.config.Synara = prefs
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	binary := mustSynaraFixtureBinary(t, a)
	saved, err := a.readSynaraPrepared()
	if err != nil || !a.synaraReady(saved, binary, a.launchRuntime()) {
		t.Fatal("prepared active profile is not ready", err)
	}
	root := synaraPaths(a.dir).Root
	for _, relative := range []string{filepath.Join("profiles", "codex-kilo", "config.toml"), filepath.Join("profiles", "codex-kilo", "models.json"), filepath.Join("data", "userdata", "secrets", t3CodeSecretName(synaraCodexProxyID, "KILO_LOCAL_API_KEY"))} {
		if saved.Files[relative] == "" {
			t.Fatalf("missing required active file %s", relative)
		}
	}
	inactive := filepath.Join(root, "profiles", "claude-kilo", "settings.json")
	if err = os.MkdirAll(filepath.Dir(inactive), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(inactive, []byte("retained invalid inactive settings; never read"), 0600); err != nil {
		t.Fatal(err)
	}
	if !a.synaraReady(saved, binary, a.launchRuntime()) {
		t.Fatal("inactive retained profile invalidates active account")
	}
	path := filepath.Join(root, "profiles", "codex-kilo", "models.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(original, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if a.synaraReady(saved, binary, a.launchRuntime()) {
		t.Fatal("active file tamper was accepted")
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	saved.CodexLibrary = cloneModelLibrary(saved.CodexLibrary)
	saved.CodexLibrary.Models[0].DisplayName = "unbound metadata"
	if a.synaraReady(saved, binary, a.launchRuntime()) {
		t.Fatal("manifest effective library was not bound to current policy")
	}
}

func TestSynaraNativeOnlyIgnoresDisabledHomesAndProxyChanges(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := synaraExplicitPreferencesFixture()
	prefs.Codex = false
	prefs.Claude = false
	prefs.KiloCodex = false
	prefs.KiloClaude = false
	a.config.Synara = prefs
	t.Setenv("CODEX_HOME", "relative-disabled")
	t.Setenv("CLAUDE_CONFIG_DIR", "relative-disabled")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "relative-disabled")
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("prepare disabled homes: %d %s", w.Code, w.Body.String())
	}
	saved, err := a.readSynaraPrepared()
	if err != nil {
		t.Fatal(err)
	}
	binary := mustSynaraFixtureBinary(t, a)
	a.config.LocalKey = "different unrelated local key"
	a.config.Port = 45678
	a.config.OrgID = "different-unrelated-team"
	t.Setenv("CODEX_HOME", "different-relative-disabled")
	t.Setenv("CLAUDE_CONFIG_DIR", "different-relative-disabled")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "different-relative-disabled")
	if !a.synaraReady(saved, binary, a.launchRuntime()) {
		t.Fatal("unrelated disabled home or Kilo connection staled native-only profile")
	}
}

func TestSynaraExplicitClaudePickerRoutesOnlyItsEffectiveModels(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := defaultSynaraPreferences()
	prefs.Codex = false
	prefs.Claude = false
	prefs.KiloCodex = false
	a.config.Synara = prefs
	catalog := nativeCachedCatalog{SchemaVersion: 1, OrgID: a.catalogScopeLocked(), Models: t3CodeOfficialCatalogFixture()}
	raw, _ := json.Marshal(catalog)
	if err := os.WriteFile(filepath.Join(a.dir, "model-catalog.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("prepare official Claude: %d %s", w.Code, w.Body.String())
	}
	saved, err := a.readSynaraPrepared()
	if err != nil {
		t.Fatal(err)
	}
	models := saved.Providers[synaraClaudeProxyID].(map[string]any)["config"].(map[string]any)["customModels"]
	want := []any{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("native picker identities: %#v", models)
	}
	request := httptest.NewRequest("POST", "http://127.0.0.1/synara/v1/messages", bytes.NewBufferString(`{"model":"claude-sonnet-5-5","output_config":{"effort":"high"},"messages":[]}`)).WithContext(context.WithValue(context.Background(), synaraRequestContextKey{}, true))
	prepared, err := a.prepareSynaraRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(prepared.Body)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if json.Unmarshal(body, &object) != nil || object["model"] != "anthropic/claude-sonnet-5.5" || object["output_config"].(map[string]any)["effort"] != "high" {
		t.Fatalf("native model/effort routed incorrectly: %s", body)
	}
	request = httptest.NewRequest("POST", "http://127.0.0.1/synara/v1/messages", bytes.NewBufferString(`{"model":"openai/gpt-selected","messages":[]}`)).WithContext(context.WithValue(context.Background(), synaraRequestContextKey{}, true))
	if _, err = a.prepareSynaraRequest(request); err == nil {
		t.Fatal("Claude accepted a model outside its effective library")
	}
}

func TestSynaraExplicitManifestRejectsMissingInactiveAndUnexpectedEntries(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := synaraExplicitPreferencesFixture()
	prefs.Codex = false
	prefs.Claude = false
	prefs.KiloClaude = false
	a.config.Synara = prefs
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	selection := synaraPaths(a.dir).Selection
	original, err := os.ReadFile(selection)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*synaraPrepared){
		"missing-active-config": func(saved *synaraPrepared) {
			delete(saved.Files, filepath.Join("profiles", "codex-kilo", "config.toml"))
		},
		"inactive-profile": func(saved *synaraPrepared) {
			saved.Files[filepath.Join("profiles", "claude-kilo", "settings.json")] = openDesignHash([]byte("retained"))
		},
		"inactive-unknown-profile": func(saved *synaraPrepared) {
			saved.Files[filepath.Join("profiles", "claude-kilo", "extra.json")] = openDesignHash([]byte("retained"))
		},
		"core-enabled-drift": func(saved *synaraPrepared) { saved.Providers[synaraCodexProxyID].(map[string]any)["enabled"] = false },
		"unsupported-provider": func(saved *synaraPrepared) {
			saved.Providers["muse"] = map[string]any{"driver": "muse", "enabled": true}
		},
		"inactive-opencode-secret": func(saved *synaraPrepared) {
			saved.Files[filepath.Join("data", "userdata", "secrets", t3CodeSecretName(synaraOpenCodeProxyID, synaraOpenCodeKey))] = openDesignHash([]byte("retained"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			var saved synaraPrepared
			if json.Unmarshal(original, &saved) != nil {
				t.Fatal("invalid original")
			}
			mutate(&saved)
			raw, _ := json.Marshal(saved)
			if err = os.WriteFile(selection, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = a.readSynaraPrepared(); err == nil {
				t.Fatal("accepted corrupt manifest")
			}
			if err = os.WriteFile(selection, original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSynaraPrepareRejectsStalePreferenceSnapshotWithoutChanges(t *testing.T) {
	a := synaraTestApp(t, "macos")
	captured := synaraExplicitPreferencesFixture()
	a.config.Synara = captured
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatalf("initial prepare: %d %s", w.Code, w.Body.String())
	}
	next := captured
	next.Claude = false
	next.KiloClaude = false
	raw, _ := json.Marshal(next)
	if w := adminRequest(a, "clients/synara/preferences", string(raw)); w.Code != 200 {
		t.Fatalf("save newer settings: %d %s", w.Code, w.Body.String())
	}
	paths := []string{filepath.Join(a.dir, "settings.json"), synaraPaths(a.dir).Settings, synaraPaths(a.dir).Selection}
	before := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
	}
	raw, _ = json.Marshal(map[string]any{"preferences": captured})
	w := adminRequest(a, "clients/synara", string(raw))
	if w.Code != 409 || !reflect.DeepEqual(a.config.Synara, next) {
		t.Fatalf("stale preparation replaced newer preferences: %d %+v", w.Code, a.config.Synara)
	}
	for path, data := range before {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("stale prepare changed %s", path)
		}
	}
}

func TestSynaraOfficialEffectiveLibrariesDoNotApplyLegacyGlobalLimit(t *testing.T) {
	a := synaraTestApp(t, "macos")
	a.config.Synara = defaultSynaraPreferences()
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "openai/gpt-selected", Models: []modelLibraryItem{{ID: "openai/gpt-selected"}}}
	for i := 0; i < 32; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/extra-%d", i)})
	}
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.catalogScopeLocked(), Models: t3CodeOfficialCatalogFixture()})
	if err := os.WriteFile(filepath.Join(a.dir, "model-catalog.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	w := adminRequest(a, "clients/synara", `{}`)
	if w.Code != 200 {
		t.Fatalf("official effective libraries incorrectly use global32: %d %s", w.Code, w.Body.String())
	}
	saved, err := a.readSynaraPrepared()
	if err != nil || len(saved.Library.Models) != 33 || len(saved.CodexLibrary.Models) != 1 || len(saved.ClaudeLibrary.Models) != 3 {
		t.Fatalf("wrong effective scopes: %+v %v", saved, err)
	}
}
