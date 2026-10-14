package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func t3CodeMuseOnlyPreferences(connection string) t3CodePreferences {
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	prefs.Providers, prefs.MuseConnection = []string{"muse"}, connection
	return prefs
}

func TestT3CodeMusePreferencesLegacyAndActiveConnection(t *testing.T) {
	if defaults := normalizeT3CodePreferences(t3CodePreferences{}); defaults.MuseConnection != "native" || t3CodeMuseKiloEnabled(defaults) {
		t.Fatal("legacy defaults enabled a Kilo Muse account", defaults)
	}
	for _, tc := range []struct {
		connection string
		enabled    bool
	}{
		{"", false}, {"native", false}, {"kilo", true},
	} {
		prefs := t3CodeMuseOnlyPreferences(tc.connection)
		if err := validateT3CodePreferences(prefs); err != nil || t3CodeMuseKiloEnabled(prefs) != tc.enabled || t3CodeUsesKilo(prefs) != tc.enabled {
			t.Fatal("Muse connection did not determine its Kilo dependency", prefs, err)
		}
		prefs.Providers = []string{}
		if t3CodeMuseKiloEnabled(prefs) || t3CodeUsesKilo(prefs) {
			t.Fatal("disabled Muse required the Kilo connection", prefs)
		}
		if _, err := deriveT3CodeModelPolicy(prefs, emptyModelLibrary(), nil); err != nil {
			t.Fatal("disabled Kilo Muse required a shared model", err)
		}
	}
	if _, err := deriveT3CodeModelPolicy(t3CodeMuseOnlyPreferences("native"), emptyModelLibrary(), nil); err != nil {
		t.Fatal("native Meta account required shared models", err)
	}
	if _, err := deriveT3CodeModelPolicy(t3CodeMuseOnlyPreferences("kilo"), emptyModelLibrary(), nil); err == nil {
		t.Fatal("enabled Kilo Muse accepted an empty shared selection")
	}
}

func TestT3CodeMusePreferencesSaveOmissionAndStrictJSON(t *testing.T) {
	a := t3CodeTestApp(t, runtime.GOOS)
	prefs := t3CodeMuseOnlyPreferences("kilo")
	prefs.OpenCodeConnection = "kilo"
	root := t3CodePaths(a.dir).Root
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conversation-fixture"), []byte("synthetic conversation; preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	before := clientInstallationTree(t, root)
	library := a.modelLibrary.snapshot()
	data, _ := json.Marshal(prefs)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	legacy := map[string]any{}
	json.Unmarshal(data, &legacy)
	delete(legacy, "museConnection")
	delete(legacy, "openCodeConnection")
	legacy["codex"] = true
	data, _ = json.Marshal(legacy)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 || a.config.T3Code.MuseConnection != "kilo" || a.config.T3Code.OpenCodeConnection != "kilo" || !a.config.T3Code.Codex {
		t.Fatal("older settings save reset a connection choice", response.Code, response.Body.String())
	}
	previous := a.config.T3Code
	for _, value := range []any{nil, "guess", false, 2, []any{"kilo"}, map[string]any{"value": "kilo"}} {
		input := map[string]any{}
		json.Unmarshal(data, &input)
		input["museConnection"] = value
		invalid, _ := json.Marshal(input)
		response := adminRequest(a, "clients/t3-code/preferences", string(invalid))
		if response.Code != 400 || !reflect.DeepEqual(previous, a.config.T3Code) {
			t.Fatal("invalid Muse choice changed preferences", response.Code, response.Body.String())
		}
	}
	for _, invalid := range []string{
		strings.TrimSuffix(string(data), "}") + `,"museConnection":"native","museConnection":"kilo"}`,
		strings.TrimSuffix(string(data), "}") + `,"museConnetion":"native"}`,
		string(data) + `{}`,
	} {
		if _, _, err := decodeT3CodePreferencesDocument([]byte(invalid)); err == nil {
			t.Fatal("ambiguous or unknown Muse settings document accepted", invalid)
		}
	}
	legacy["museConnection"] = ""
	data, _ = json.Marshal(legacy)
	response := adminRequest(a, "clients/t3-code/preferences", string(data))
	var result struct {
		Preferences       t3CodePreferences `json:"preferences"`
		MuseKiloSupported bool              `json:"museKiloSupported"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.Preferences.MuseConnection != "native" || result.MuseKiloSupported {
		t.Fatal("explicit empty choice or unproven capability was misreported", response.Code, response.Body.String())
	}
	saved, err := readSettings(a.dir)
	if err != nil || saved.T3Code.MuseConnection != "native" || !reflect.DeepEqual(before, clientInstallationTree(t, t3CodePaths(a.dir).Root)) || !reflect.DeepEqual(library, a.modelLibrary.snapshot()) {
		t.Fatal("saving a connection changed models or prepared accounts", err)
	}
}

func TestT3CodeMuseModelPolicyExactSelectionAndReasoning(t *testing.T) {
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/default", Models: []modelLibraryItem{
		{ID: "vendor/catalog", DisplayName: "A catalog model", ContextWindow: 64000, ReasoningEffort: "high"},
		{ID: "vendor/default", ReasoningCustom: true, ReasoningLevels: []string{"high", "low"}, ReasoningEffort: "low"},
		{ID: "vendor/disabled-reasoning", ReasoningCustom: true},
		{ID: "vendor/unknown"},
	}, Roles: map[string]string{"vendor/default": "coding"}}
	catalog := []modelInfo{
		{ID: "vendor/catalog", ReasoningEfforts: []string{"medium", "high", "ultra"}},
		{ID: "vendor/default", ReasoningEfforts: []string{"medium", "max"}},
		{ID: "vendor/disabled-reasoning", ReasoningEfforts: []string{"low", "high"}},
		{ID: "vendor/not-selected", ReasoningEfforts: []string{"max"}},
	}
	before := cloneModelLibrary(library)
	policy, err := deriveT3CodeModelPolicy(t3CodeMuseOnlyPreferences("kilo"), library, catalog)
	wantLevels := map[string][]string{"vendor/catalog": {"medium", "high", "ultra"}, "vendor/default": {"low", "high"}, "vendor/disabled-reasoning": {}, "vendor/unknown": {}}
	wantDefaults := map[string]string{"vendor/catalog": "high", "vendor/default": "low", "vendor/disabled-reasoning": "", "vendor/unknown": ""}
	if err != nil || !reflect.DeepEqual(policy.MuseLibrary, before) || !reflect.DeepEqual(policy.MuseReasoningLevels, wantLevels) || !reflect.DeepEqual(policy.MuseReasoningDefaults, wantDefaults) || len(policy.CodexLibrary.Models) != 0 || len(policy.ClaudeLibrary.Models) != 0 || len(policy.OpenCodeLibrary.Models) != 0 {
		t.Fatal("Muse changed exact identities, default or declared reasoning", policy, err)
	}
	policy.MuseLibrary.Models[1].ReasoningLevels[0] = "max"
	policy.MuseLibrary.Roles["vendor/default"] = "changed"
	policy.MuseReasoningLevels["vendor/catalog"][0] = "none"
	if !reflect.DeepEqual(before, library) || catalog[0].ReasoningEfforts[0] != "medium" {
		t.Fatal("Muse policy mutated the shared library or catalog")
	}
}

func TestT3CodeMuseModelPolicyKeepsMoreThan32Models(t *testing.T) {
	library := emptyModelLibrary()
	for i := 0; i < 50; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/model-%d", i)})
	}
	library.DefaultModel = library.Models[37].ID
	policy, err := deriveT3CodeModelPolicy(t3CodeMuseOnlyPreferences("kilo"), library, nil)
	if err != nil || !reflect.DeepEqual(policy.MuseLibrary, library) || len(policy.MuseReasoningLevels) != len(library.Models) {
		t.Fatal("Muse acquired another driver's 32-model limit or lost its selected default", policy, err)
	}
}
