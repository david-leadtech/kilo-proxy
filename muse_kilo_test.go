package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func museTestLibrary() modelLibrary {
	return modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/nested/reasoning", Models: []modelLibraryItem{
		{ID: "vendor/nested/reasoning", DisplayName: "My model", ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "low", ContextPreset: "custom", ContextWindow: 32768, MaxOutputTokens: 2048},
		{ID: "vendor/no-reasoning", ReasoningCustom: true, ReasoningLevels: []string{}},
	}}
}

func TestMuseKiloCataloguePreservesExactSelectionAndEmptyReasoning(t *testing.T) {
	library := museTestLibrary()
	for i := len(library.Models); i < 50; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/model-%d", i), ReasoningCustom: true})
	}
	settings, auth, err := buildMuseKiloProfile(library, nil, "http://127.0.0.1:8877/muse/v1", museProxyToken("synthetic", "terminal"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Model  string                 `json:"model"`
		Models []museKiloCatalogModel `json:"model_catalog"`
	}
	if json.Unmarshal(settings, &config) != nil || config.Model != library.DefaultModel || len(config.Models) != 50 {
		t.Fatal("selection truncated or default changed", string(settings))
	}
	for i, row := range config.Models {
		if row.ModelID != library.Models[i].ID || row.IsDefault != (i == 0) || row.ProviderID != "meta" || row.ProfileID != "tbh" {
			t.Fatal("Muse gateway identity changed", row)
		}
	}
	if config.Models[0].DisplayLabel != "My model" || config.Models[0].ContextLimit != 32768 || config.Models[0].OutputLimit != 2048 || config.Models[1].ReasoningEffortVariants == nil || len(config.Models[1].ReasoningEffortVariants) != 0 {
		t.Fatal("saved limits/names/empty reasoning changed", config.Models[:2])
	}
	if strings.Contains(string(settings), "release_date") || strings.Contains(string(settings), "api_key") || !strings.Contains(string(auth), museProxyToken("synthetic", "terminal")) || strings.Contains(string(auth), "synthetic") {
		t.Fatal("invented metadata or leaked root credential")
	}
	var credentials struct {
		SchemaVersion int `json:"schema_version"`
	}
	if json.Unmarshal(auth, &credentials) != nil || credentials.SchemaVersion != 1 {
		t.Fatal("Muse rejects auth files without schema version 1")
	}
}

func TestMuseScopedCredentialsAreRevokedAndCannotCrossConnections(t *testing.T) {
	terminal, t3 := museProxyToken("root", "terminal"), museProxyToken("root", "t3")
	if terminal == t3 || museProxyScope(terminal, "root") != "terminal" || museProxyScope(t3, "root") != "t3" || museProxyScope(terminal, "rotated") != "" || museProxyScope("root", "root") != "" || museProxyScope(museProxyToken("", "terminal"), "") != "" {
		t.Fatal("Muse scope credentials are not isolated or revocable")
	}
	for _, base := range []string{"https://127.0.0.1:8877", "http://localhost:8877", "http://127.0.0.1:8877/v1", "http://127.0.0.1:8877?key=private", "http://evil.example:8877"} {
		if _, _, err := buildMuseKiloProfile(museTestLibrary(), nil, base, terminal); err == nil {
			t.Fatal("unsafe endpoint accepted", base)
		}
	}
}

func TestMuseRequestsRejectUnselectedModelsAndPreserveToolsAndExactIDs(t *testing.T) {
	for _, body := range []string{
		`{"model":"other/forged","input":[]}`,
		`{"model":"vendor/nested/reasoning","reasoning":{"effort":"unknown"}}`,
		`{"model":"vendor/nested/reasoning","model":"other/forged"}`,
		`{"model":"vendor/nested/reasoning","reasoning":true}`,
	} {
		r := httptest.NewRequest("POST", "/responses", strings.NewReader(body))
		if prepareMuseModelRequest(r, museTestLibrary(), nil) == nil {
			t.Fatal("invalid model/effort accepted", body)
		}
	}
	r := httptest.NewRequest("POST", "/responses", strings.NewReader(`{"model":"vendor/nested/reasoning","input":[{"type":"function_call_output","call_id":"call1","output":"safe fixture"}],"tools":[{"type":"function","name":"read_fixture","parameters":{"type":"object"}}],"stream":true}`))
	if err := prepareMuseModelRequest(r, museTestLibrary(), nil); err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(r.Body)
	var result map[string]json.RawMessage
	json.Unmarshal(data, &result)
	if string(result["model"]) != `"vendor/nested/reasoning"` || string(result["reasoning"]) != `{"effort":"low"}` || !strings.Contains(string(result["input"]), "call1") || !strings.Contains(string(result["tools"]), "read_fixture") || string(result["stream"]) != "true" {
		t.Fatal("Responses identity, tools or default reasoning changed", string(data))
	}
	r = httptest.NewRequest("POST", "/responses", strings.NewReader(`{"model":"vendor/no-reasoning","reasoning":{"effort":"high","summary":"auto"},"input":[]}`))
	if err := prepareMuseModelRequest(r, museTestLibrary(), nil); err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(r.Body)
	var noReasoning map[string]json.RawMessage
	json.Unmarshal(data, &noReasoning)
	if _, present := noReasoning["reasoning"]; present {
		t.Fatal("inherited session effort leaked to nonreasoning model", string(data))
	}
}
