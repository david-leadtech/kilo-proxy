package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func t3MusePublisherPolicyFixture() modelLibrary {
	return modelLibrary{SchemaVersion: 1, DefaultModel: "qwen/unrelated", Models: []modelLibraryItem{
		{ID: "qwen/unrelated", DisplayName: "Meta Muse Spark"},
		{ID: "meta/muse-spark-1.3", ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "low"},
		{ID: "meta-llama/llama-4-maverick"},
		{ID: "meta/muse-wonder"},
		{ID: "meta-other/muse-spark"},
		{ID: "vendor/meta/muse-wonder"},
	}, Roles: map[string]string{"qwen/unrelated": "coding"}}
}

func TestT3MuseModelPolicyScopesOriginalPublishersAndKeepsOtherLibraries(t *testing.T) {
	library := t3MusePublisherPolicyFixture()
	before := cloneModelLibrary(library)
	prefs := t3CodeMuseOnlyPreferences("kilo")
	prefs.MuseModels = "official"
	prefs.Providers = []string{"muse", "opencode"}
	prefs.OpenCodeConnection = "kilo"
	catalog := []modelInfo{
		{ID: "qwen/unrelated", Provider: "meta", Name: "Meta Muse Wonder", ReasoningEfforts: []string{"max"}},
		{ID: "meta/muse-spark-1.3", Provider: "qwen", ReasoningEfforts: []string{"max"}},
		{ID: "meta/muse-wonder", ReasoningEfforts: []string{"medium", "high", "max"}},
		{ID: "meta/unselected"},
	}
	policy, err := deriveT3CodeModelPolicy(prefs, library, catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"meta/muse-spark-1.3", "meta-llama/llama-4-maverick", "meta/muse-wonder"}
	var ids []string
	for _, m := range policy.MuseLibrary.Models {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, want) || policy.MuseLibrary.DefaultModel != want[0] {
		t.Fatal("wrong Meta subset/default", policy.MuseLibrary)
	}
	if !reflect.DeepEqual(policy.OpenCodeLibrary, before) || !reflect.DeepEqual(library, before) {
		t.Fatal("Muse changed another selection")
	}
	if !reflect.DeepEqual(policy.MuseReasoningLevels, map[string][]string{want[0]: {"low", "high"}, want[1]: {}, want[2]: {"medium", "high", "max"}}) {
		t.Fatal("reasoning escaped effective selection", policy.MuseReasoningLevels)
	}
	policy.MuseLibrary.Models[0].ReasoningLevels[0] = "max"
	policy.OpenCodeLibrary.Models[1].ReasoningLevels[0] = "max"
	if !reflect.DeepEqual(library, before) {
		t.Fatal("effective libraries share mutable reasoning")
	}
	library.DefaultModel = "meta/muse-wonder"
	policy, err = deriveT3CodeModelPolicy(prefs, library, catalog)
	if err != nil || policy.MuseLibrary.DefaultModel != "meta/muse-wonder" {
		t.Fatal("eligible default replaced", policy, err)
	}
	prefs.MuseModels = "shared"
	policy, err = deriveT3CodeModelPolicy(prefs, library, catalog)
	if err != nil || !reflect.DeepEqual(policy.MuseLibrary, library) {
		t.Fatal("explicit all-shared lost models", policy, err)
	}
}

func TestT3MuseModelPolicyEmptyDisabledNativeAndExistingLimit(t *testing.T) {
	prefs := t3CodeMuseOnlyPreferences("kilo")
	prefs.MuseModels = "official"
	unrelated := modelLibrary{SchemaVersion: 1, DefaultModel: "qwen/only", Models: []modelLibraryItem{{ID: "qwen/only"}}}
	if _, err := deriveT3CodeModelPolicy(prefs, unrelated, []modelInfo{{ID: "meta/catalog-only"}}); err == nil || !strings.Contains(err.Error(), "Select at least one Meta model") {
		t.Fatal("empty Meta selection fell back", err)
	}
	prefs.MuseModels = "shared"
	if _, err := deriveT3CodeModelPolicy(prefs, unrelated, nil); err != nil {
		t.Fatal("all shared removed unrelated models", err)
	}
	for _, policy := range []string{"official", "shared"} {
		prefs.MuseModels = policy
		prefs.MuseConnection = "native"
		result, err := deriveT3CodeModelPolicy(prefs, emptyModelLibrary(), nil)
		if err != nil || len(result.MuseLibrary.Models) != 0 {
			t.Fatal("native Meta acquired Kilo model dependency", result, err)
		}
		prefs.MuseConnection = "kilo"
		prefs.Providers = []string{}
		if _, err := deriveT3CodeModelPolicy(prefs, emptyModelLibrary(), nil); err != nil {
			t.Fatal("disabled Muse acquired model dependency", err)
		}
		prefs.Providers = []string{"muse"}
	}
	library := emptyModelLibrary()
	for i := 0; i < 40; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("meta/model-%d", i)})
	}
	library.DefaultModel = library.Models[0].ID
	prefs.MuseModels = "official"
	if policy, err := deriveT3CodeModelPolicy(prefs, library, nil); err != nil || len(policy.MuseLibrary.Models) != 40 {
		t.Fatal("new unrelated custom-model limit", err)
	}
}

func TestT3MuseModelPolicyMigrationAndStrictRequests(t *testing.T) {
	fresh := normalizeT3CodePreferences(t3CodePreferences{})
	if fresh.MuseModels != "official" {
		t.Fatal("new accounts do not default to Meta", fresh)
	}
	legacy := fresh
	legacy.MuseModels = ""
	legacy.MuseConnection = "kilo"
	if p := normalizeT3CodePreferences(legacy); p.MuseModels != "shared" {
		t.Fatal("legacy Kilo selection lost availability", p)
	}
	legacy.MuseConnection = "native"
	if p := normalizeT3CodePreferences(legacy); p.MuseModels != "official" {
		t.Fatal("new Muse selection lacks Meta default", p)
	}
	legacy.MuseModels = "shared"
	if p := normalizeT3CodePreferences(legacy); p.MuseModels != "shared" {
		t.Fatal("explicit shared choice reset", p)
	}
	data, _ := json.Marshal(fresh)
	var fields map[string]any
	json.Unmarshal(data, &fields)
	delete(fields, "museModels")
	data, _ = json.Marshal(fields)
	p, present, err := decodeT3CodePreferencesDocument(data)
	if err != nil || present.MuseModels || normalizeT3CodePreferences(p).MuseModels != "official" {
		t.Fatal("legacy request rejected", p, present, err)
	}
	for _, value := range []any{nil, "", "all", false, 2, []string{"shared"}} {
		fields["museModels"] = value
		data, _ = json.Marshal(fields)
		if _, _, err := decodeT3CodePreferencesDocument(data); err == nil {
			t.Fatal("invalid policy accepted", value)
		}
	}
	fields["museModels"] = "shared"
	data, _ = json.Marshal(fields)
	p, present, err = decodeT3CodePreferencesDocument(data)
	if err != nil || !present.MuseModels || p.MuseModels != "shared" {
		t.Fatal("explicit policy lost", err)
	}
	duplicate := strings.TrimSuffix(string(data), "}") + "," + `"museModels":"official"}`
	if _, _, err := decodeT3CodePreferencesDocument([]byte(duplicate)); err == nil {
		t.Fatal("duplicate policy accepted")
	}
}

func TestT3MuseModelPolicyLegacySaveKeepsExplicitChoice(t *testing.T) {
	a := t3CodeTestApp(t, "linux", t3CodeNightlyVersion)
	prefs := t3CodeMuseOnlyPreferences("kilo")
	prefs.MuseModels = "shared"
	a.config.T3Code = prefs
	raw, _ := json.Marshal(prefs)
	var document map[string]any
	json.Unmarshal(raw, &document)
	delete(document, "museModels")
	document["codex"] = true
	raw, _ = json.Marshal(document)
	response := adminRequest(a, "clients/t3-code/preferences", string(raw))
	if response.Code != 200 || a.config.T3Code.MuseModels != "shared" {
		t.Fatal("older UI reset saved shared choice", response.Code, response.Body.String())
	}
	document["museModels"] = "official"
	raw, _ = json.Marshal(document)
	response = adminRequest(a, "clients/t3-code/preferences", string(raw))
	if response.Code != 200 || a.config.T3Code.MuseModels != "official" {
		t.Fatal("explicit Meta policy did not save", response.Code, response.Body.String())
	}
	delete(document, "museModels")
	document["codex"] = false
	raw, _ = json.Marshal(document)
	response = adminRequest(a, "clients/t3-code/preferences", string(raw))
	if response.Code != 200 || a.config.T3Code.MuseModels != "official" {
		t.Fatal("legacy update reset explicit Meta", response.Code, response.Body.String())
	}
}

func TestT3MuseModelPolicyPreparedProfileScopesCatalogAuthAndRequests(t *testing.T) {
	a, binary, _ := t3CodeMuseKiloTestApp(t)
	a.config.T3Code.MuseModels = "official"
	library := t3MusePublisherPolicyFixture()
	state := a.modelLibrary.snapshot()
	if _, err := a.modelLibrary.save(library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	normal := filepath.Join(a.launcher.home, ".config", "muse")
	if err := os.MkdirAll(normal, 0700); err != nil {
		t.Fatal(err)
	}
	nativeSettings := []byte("synthetic native Meta configuration; preserve")
	if err := os.WriteFile(filepath.Join(normal, "settings.json"), nativeSettings, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("META_API_KEY", "synthetic-personal-meta-never-copy")
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) || !reflect.DeepEqual(a.modelLibrary.snapshot().Library, library) {
		t.Fatal("filtered preparation changed global selection")
	}
	_, settingsPath, authPath := t3CodeMusePaths(t3CodePaths(a.dir).Root)
	settings, _ := os.ReadFile(settingsPath)
	auth, _ := os.ReadFile(authPath)
	var profile struct {
		Model   string                 `json:"model"`
		Catalog []museKiloCatalogModel `json:"model_catalog"`
	}
	if json.Unmarshal(settings, &profile) != nil || profile.Model != "meta/muse-spark-1.3" || len(profile.Catalog) != 3 {
		t.Fatal("wrong generated Meta catalog", string(settings))
	}
	for _, row := range profile.Catalog {
		if !strings.HasPrefix(row.ModelID, "meta/") && !strings.HasPrefix(row.ModelID, "meta-llama/") {
			t.Fatal("unrelated model passed shim", row)
		}
	}
	token := museProxyToken(a.config.LocalKey, "t3")
	if !bytes.Contains(auth, []byte(token)) || bytes.Contains(auth, []byte(a.config.LocalKey)) || bytes.Contains(auth, []byte("synthetic-personal-meta-never-copy")) {
		t.Fatal("credential boundary changed")
	}
	nativeAfter, _ := os.ReadFile(filepath.Join(normal, "settings.json"))
	if !bytes.Equal(nativeAfter, nativeSettings) {
		t.Fatal("normal Meta configuration modified")
	}
	effective, catalog, err := a.museScopeLibrary("t3")
	if err != nil || !reflect.DeepEqual(effective, saved.MuseLibrary) {
		t.Fatal("proxy did not use filtered selection", err)
	}
	for id, allowed := range map[string]bool{"meta/muse-wonder": true, "qwen/unrelated": false} {
		req := httptest.NewRequest("POST", "http://127.0.0.1/t3-code/muse/v1/responses", strings.NewReader(fmt.Sprintf("{\"model\":%q}", id)))
		if err := prepareMuseModelRequest(req, effective, catalog); (err == nil) != allowed {
			t.Fatal("request escaped exact selection", id, err)
		}
	}
	a.config.T3Code.MuseModels = "shared"
	if a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("policy change reused old preparation")
	}
	prepareT3CodeFixture(t, a)
	saved = mustReadT3CodePrepared(t, a)
	if !reflect.DeepEqual(saved.MuseLibrary, library) {
		t.Fatal("all-shared reprepare removed models")
	}

	// A legacy settings document must not narrow or invalidate this account's
	// existing shared preparation when its omitted field is recovered from disk.
	if err := writeSettings(a.dir, a.config); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(a.dir, "settings.json"))
	var document map[string]json.RawMessage
	json.Unmarshal(raw, &document)
	var fields map[string]json.RawMessage
	json.Unmarshal(document["t3Code"], &fields)
	delete(fields, "museModels")
	document["t3Code"], _ = json.Marshal(fields)
	raw, _ = json.Marshal(document)
	if err := os.WriteFile(filepath.Join(a.dir, "settings.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := readSettings(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	a.config = recovered
	if a.config.T3Code.MuseModels != "shared" || !a.t3CodeReady(saved, binary, a.launchRuntime()) || !reflect.DeepEqual(a.modelLibrary.snapshot().Library, library) {
		t.Fatal("legacy settings lost prepared shared availability")
	}
}

func TestT3MuseModelPolicyErrorsUseSavedLanguage(t *testing.T) {
	for _, message := range []string{"Choose Meta models or all shared models for Muse Code.", "Select at least one Meta model in Models, choose all shared models, or disable Muse Code · Kilo.", "Invalid Muse model policy."} {
		spanish := nativeMessage(message, "es")
		if spanish == message || nativeMessage(spanish, "en") != message {
			t.Fatal("missing model policy translation", message)
		}
	}
}

func TestT3MuseModelPolicyReadSettingsLegacyDocument(t *testing.T) {
	for _, tc := range []struct {
		name, connection, policy, want string
		absent                         bool
	}{
		{"fresh settings", "", "", "official", true},
		{"legacy Kilo", "kilo", "", "shared", false},
		{"legacy native", "native", "", "official", false},
		{"explicit Meta", "kilo", "official", "official", false},
		{"explicit shared", "native", "shared", "shared", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prefs := normalizeT3CodePreferences(t3CodePreferences{})
			prefs.Providers = []string{"muse"}
			prefs.MuseConnection = tc.connection
			raw, _ := json.Marshal(prefs)
			var fields map[string]any
			json.Unmarshal(raw, &fields)
			delete(fields, "museModels")
			if tc.policy != "" {
				fields["museModels"] = tc.policy
			}
			document := map[string]any{}
			if !tc.absent {
				document["t3Code"] = fields
			}
			raw, _ = json.Marshal(document)
			path := filepath.Join(dir, "settings.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			saved, err := readSettings(dir)
			if err != nil || saved.T3Code.MuseModels != tc.want {
				t.Fatalf("raw persisted %s: got %q want %q: %v", tc.name, saved.T3Code.MuseModels, tc.want, err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(after, raw) {
				t.Fatal("reading migration rewrote config")
			}
		})
	}
}

func TestT3MuseModelPolicyReadSettingsRejectsExplicitInvalidPolicy(t *testing.T) {
	for _, policy := range []any{nil, "", "all", false} {
		dir := t.TempDir()
		prefs := normalizeT3CodePreferences(t3CodePreferences{})
		raw, _ := json.Marshal(prefs)
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		fields["museModels"] = policy
		raw, _ = json.Marshal(map[string]any{"t3Code": fields})
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSettings(dir); err == nil {
			t.Fatal("explicit persisted invalid policy accepted", policy)
		}
	}
}
