package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleEditorSelection() editorSelection {
	return editorSelection{Models: []editorModel{{ID: "vendor/one", Name: "One", Context: 64000, Output: 4000}, {ID: "vendor/two", Name: "Two", Context: 128000}}, Initial: "vendor/two"}
}
func TestEditorJSONCPreservation(t *testing.T) {
	for _, client := range []string{"opencode", "zed"} {
		original := []byte("// private comment\n{\n  \"large\": 900719925474099312345, // exact number\n  \"theme\":\"dark\",\n  \"permission\": {\"bash\": \"ask\"},\n  \"language_models\":{\"other\":{\"enabled\":true}},\n}\n")
		updated, err := mergeEditorSettings(original, client, exampleEditorSelection(), "http://127.0.0.1:8877/v1", "local-test-key")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"// private comment", "900719925474099312345, // exact number", `"theme":"dark"`, `"bash": "ask"`, `"other":{"enabled":true}`, "vendor/one", "vendor/two"} {
			if !bytes.Contains(updated, []byte(want)) {
				t.Errorf("%s: lost %s", client, want)
			}
		}
		if bytes.Contains(updated, []byte("local-test-key")) != (client == "opencode") {
			t.Errorf("%s: wrong credential storage", client)
		}
		twice, err := mergeEditorSettings(updated, client, exampleEditorSelection(), "http://127.0.0.1:8877/v1", "local-test-key")
		if err != nil || !bytes.Equal(twice, updated) {
			t.Fatalf("%s: no-op changed formatting", client)
		}
		changed, err := mergeEditorSettings(updated, client, editorSelection{Models: exampleEditorSelection().Models[:1], Initial: "vendor/one"}, "http://127.0.0.1:8899/v1", "new-local-key")
		wantURL := "http://127.0.0.1:8899/v1"
		if client == "zed" {
			wantURL = zedBaseURL(wantURL, "new-local-key")
		}
		if err != nil || bytes.Contains(changed, []byte("vendor/two")) || !bytes.Contains(changed, []byte(wantURL)) {
			t.Fatalf("%s: update failed", client)
		}
	}
	for _, bad := range []string{`[]`, `null`, `{"theme":1,"theme":2}`, `{"language_models":null}`, `{"language_models":[]}`, `{"theme":`} {
		if _, err := mergeEditorSettings([]byte(bad), "zed", exampleEditorSelection(), "http://localhost/v1", "key"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
func TestEditorOpenCodeDeclaresReasoningVariants(t *testing.T) {
	selection := editorSelection{Models: []editorModel{
		{ID: "vendor/one", Name: "One", Context: 64000, Output: 4000, Variants: []string{"low", "high", "max"}, ReasoningEffort: "high"},
		{ID: "vendor/two", Name: "Two", Context: 128000},
	}, Initial: "vendor/one"}
	baseURL, key := "http://127.0.0.1:8877/v1", "local-test-key"
	type variant struct {
		Model           string  `json:"model"`
		BaseURL         *string `json:"baseURL"`
		APIKey          *string `json:"apiKey"`
		Headers         any     `json:"headers"`
		ReasoningEffort string  `json:"reasoningEffort"`
	}
	var config struct {
		Provider map[string]struct {
			Options map[string]any `json:"options"`
			Models  map[string]struct {
				Options  map[string]any     `json:"options"`
				Headers  any                `json:"headers"`
				Variants map[string]variant `json:"variants"`
			} `json:"models"`
		} `json:"provider"`
	}
	updated, err := mergeEditorSettings(nil, "opencode", selection, baseURL, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(updated, &config); err != nil {
		t.Fatal(err)
	}
	provider := config.Provider["kilo-local"]
	if provider.Options["baseURL"] != baseURL || provider.Options["apiKey"] != key || len(provider.Options) != 2 {
		t.Fatalf("provider options must carry only the routed credentials: %v", provider.Options)
	}
	variants := provider.Models["vendor/one"].Variants
	if len(variants) != 3 {
		t.Fatalf("reasoning variants missing: %s", updated)
	}
	for _, level := range []string{"low", "high", "max"} {
		v, ok := variants[level]
		if !ok || v.Model != "vendor/one" || v.ReasoningEffort != level || v.BaseURL != nil || v.APIKey != nil || v.Headers != nil {
			t.Fatalf("bad %s variant: %+v", level, v)
		}
	}
	if provider.Models["vendor/one"].Headers != nil || len(provider.Models["vendor/one"].Options) != 1 {
		t.Fatalf("model-level provider options leak credentials into the request body: %+v", provider.Models["vendor/one"])
	}
	if config.Provider["kilo-local"].Models["vendor/two"].Variants != nil {
		t.Fatal("model without variants declared reasoning levels")
	}
	if got := config.Provider["kilo-local"].Models["vendor/one"].Options["reasoningEffort"]; got != "high" {
		t.Fatalf("default reasoning effort was not emitted: %v", got)
	}
	low, high, max := bytes.Index(updated, []byte(`"low":`)), bytes.Index(updated, []byte(`"high":`)), bytes.Index(updated, []byte(`"max":`))
	if low < 0 || high < 0 || max < 0 || !(low < high && high < max) {
		t.Fatalf("variant levels were not serialized in declaration order: %s", updated)
	}
	zed, err := mergeEditorSettings(nil, "zed", selection, baseURL, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(zed, []byte(`"variants"`)) {
		t.Fatal("Zed settings declared OpenCode reasoning variants")
	}
	twice, err := mergeEditorSettings(updated, "opencode", selection, baseURL, key)
	if err != nil || !bytes.Equal(twice, updated) {
		t.Fatal("reasoning variants are not idempotent")
	}
	if err := validateEditorSelection(selection); err != nil {
		t.Fatal(err)
	}
	for _, levels := range [][]string{{"bogus"}, {"low", "low"}} {
		bad := editorSelection{Models: append([]editorModel{}, selection.Models...), Initial: selection.Initial}
		bad.Models[0].Variants = levels
		if validateEditorSelection(bad) == nil {
			t.Fatalf("accepted reasoning levels %v", levels)
		}
	}
	bad := editorSelection{Models: append([]editorModel{}, selection.Models...), Initial: selection.Initial}
	bad.Models[0].ReasoningEffort = "medium"
	if validateEditorSelection(bad) == nil {
		t.Fatal("accepted a default reasoning effort outside the declared variants")
	}
}
func TestEditorOpenCodeRewritesLegacyModelCredentials(t *testing.T) {
	legacy := []byte(`{
		"provider":{"kilo-local":{"options":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"old-key"},
			"models":{"vendor/one":{"options":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"old-key","headers":{"Authorization":"Bearer old-key"}},
				"headers":{"Authorization":"Bearer old-key"},
				"variants":{"low":{"model":"vendor/one","baseURL":"http://127.0.0.1:1/v1","apiKey":"old-key","reasoningEffort":"low"}}}}}}
	}`)
	selection := editorSelection{Models: []editorModel{
		{ID: "vendor/one", Name: "One", Context: 64000, Variants: []string{"low", "high"}, ReasoningEffort: "high"},
	}, Initial: "vendor/one"}
	updated, err := mergeEditorSettings(legacy, "opencode", selection, "http://127.0.0.1:8877/v1", "new-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"old-key", "Bearer ", `"Authorization"`} {
		if bytes.Contains(updated, []byte(leaked)) {
			t.Fatalf("legacy model credentials survived re-prepare (%s): %s", leaked, updated)
		}
	}
	var parsed struct {
		Provider map[string]struct {
			Options map[string]any `json:"options"`
			Models  map[string]struct {
				Options map[string]any `json:"options"`
				Headers any            `json:"headers"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(updated, &parsed); err != nil {
		t.Fatal(err)
	}
	provider := parsed.Provider["kilo-local"]
	if provider.Options["apiKey"] != "new-key" || provider.Models["vendor/one"].Headers != nil || len(provider.Models["vendor/one"].Options) != 1 || provider.Models["vendor/one"].Options["reasoningEffort"] != "high" {
		t.Fatalf("re-prepare did not normalise the provider credentials: %s", updated)
	}
}
func TestEditorPrepareLoadBackupsAndIsolation(t *testing.T) {
	a := testApp(t)
	a.editorTestRoot = t.TempDir()
	body, _ := json.Marshal(exampleEditorSelection())
	for _, client := range []string{"opencode", "zed"} {
		endpoint := "editors/" + client + "/profile"
		if w := adminRequest(a, endpoint, string(body)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		_, path, choices, _ := a.editorPaths(client)
		first, _ := os.ReadFile(path)
		custom := append([]byte("// retained\n"), first...)
		if err := os.WriteFile(path, custom, 0600); err != nil {
			t.Fatal(err)
		}
		if w := adminRequest(a, endpoint, string(body)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		current, _ := os.ReadFile(path)
		if !bytes.Equal(current, custom) {
			t.Fatal("no-op changed comments")
		}
		s := exampleEditorSelection()
		s.Models[0].Name = "Renamed"
		update, _ := json.Marshal(s)
		if w := adminRequest(a, endpoint, string(update)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		backup, _ := os.ReadFile(path + ".bak")
		if !bytes.Equal(backup, custom) {
			t.Fatal("backup not exact")
		}
		if w := adminRequest(a, endpoint, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Renamed") {
			t.Fatal(w.Body.String())
		}
		if data, _ := os.ReadFile(choices); bytes.Contains(data, []byte(a.config.LocalKey)) {
			t.Fatal("selection leaks credential")
		}
		before, _ := os.ReadFile(path)
		if err := os.WriteFile(choices+".bak", []byte("old"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(choices + ".bak"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(choices+".bak", 0700); err != nil {
			t.Fatal(err)
		}
		if w := adminRequest(a, endpoint, string(body)); w.Code != 409 {
			t.Fatal(w.Code)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("partial update on unsafe backup")
		}
	}
}
func TestEditorRejectsSymlinkAndBadInputs(t *testing.T) {
	a := testApp(t)
	a.editorTestRoot = t.TempDir()
	body, _ := json.Marshal(exampleEditorSelection())
	dest := filepath.Join(a.editorTestRoot, ".opencode-kilo")
	if err := os.Symlink(t.TempDir(), dest); err != nil {
		t.Skip("symlinks unavailable")
	}
	if w := adminRequest(a, "editors/opencode/profile", string(body)); w.Code != 409 {
		t.Fatal(w.Code)
	}
	for _, s := range []editorSelection{{}, {Models: []editorModel{{ID: "model", Context: 100}}, Initial: "model"}, {Models: exampleEditorSelection().Models, Initial: "missing"}} {
		if validateEditorSelection(s) == nil {
			t.Fatal("invalid selection accepted")
		}
	}
}
