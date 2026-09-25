package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestContextPoliciesResolveWorkingBudgetAndReserve(t *testing.T) {
	for _, test := range []struct {
		name, preset                                     string
		custom, maximum, output, wantContext, wantOutput int
		wantError                                        bool
	}{
		{"recommended large model", "recommended", 0, 1050000, 128000, 272000, 68000, false},
		{"low large model", "low", 0, 1050000, 128000, 128000, 32000, false},
		{"maximum", "maximum", 0, 1050000, 128000, 1050000, 128000, false},
		{"small model caps recommended", "recommended", 0, 64000, 4096, 64000, 4096, false},
		{"small model caps low", "low", 0, 32000, 64000, 32000, 8000, false},
		{"custom over maximum", "custom", 400000, 200000, 0, 200000, 8192, false},
		{"unknown recommended is budget", "recommended", 0, 0, 0, 272000, 8192, false},
		{"unknown custom is budget", "custom", 64000, 0, 4096, 64000, 4096, false},
		{"unknown maximum", "maximum", 0, 0, 0, 0, 0, true},
		{"invalid maximum", "maximum", 0, 100000001, 0, 0, 0, true},
		{"missing custom", "custom", 0, 1050000, 0, 0, 0, true},
		{"too small custom", "custom", 1023, 0, 0, 0, 0, true},
		{"negative output", "low", 0, 1050000, -1, 0, 0, true},
		{"unknown preset", "agent-default", 0, 1050000, 0, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveContextPolicy(test.preset, test.custom, test.maximum, test.output)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			if err != nil {
				return
			}
			if got.ContextWindow != test.wantContext || got.MaxOutputTokens != test.wantOutput {
				t.Fatalf("resolved %#v, want context %d output %d", got, test.wantContext, test.wantOutput)
			}
			if got.AutoCompactTokenLimit <= 0 || got.AutoCompactTokenLimit > got.ContextWindow*9/10 || got.AutoCompactTokenLimit+got.MaxOutputTokens >= got.ContextWindow {
				t.Fatalf("compaction left no output/tool headroom: %#v", got)
			}
			if got.MaximumKnown != (test.maximum >= 1024 && test.maximum <= 100000000) {
				t.Fatal("invented catalog maximum")
			}
		})
	}
}

func TestContextLibraryPreservesLegacyAndRefreshesMaximum(t *testing.T) {
	legacy := modelLibraryItem{ID: "vendor/model", ContextWindow: 200000, MaxOutputTokens: 64000}
	first := contextChoiceFromLibrary(legacy, modelInfo{ID: legacy.ID, ContextWindow: 1050000})
	if first.ContextPreset != contextPresetCustom || first.ContextTokens != 200000 || first.Model.ContextWindow != 1050000 {
		t.Fatalf("legacy choice lost saved budget or catalog maximum: %#v", first)
	}
	resolved, err := contextPolicyForChoice(first)
	if err != nil || resolved.ContextWindow != 200000 {
		t.Fatal(resolved, err)
	}
	legacy.ContextPreset, legacy.ContextWindow = contextPresetMaximum, 0
	for _, maximum := range []int{200000, 1050000, 64000} {
		choice := contextChoiceFromLibrary(legacy, modelInfo{ContextWindow: maximum})
		resolved, err := contextPolicyForChoice(choice)
		if err != nil || resolved.ContextWindow != maximum {
			t.Fatal(resolved, err)
		}
	}
	if _, err := contextPolicyForChoice(contextChoiceFromLibrary(legacy, modelInfo{})); err == nil {
		t.Fatal("Maximum silently invented capacity while offline")
	}
}

func TestContextOutputReserveNeverExceedsPublishedMaximum(t *testing.T) {
	for _, requested := range []int{0, 1024, 128000} {
		item := modelLibraryItem{ID: "vendor/small-output", ContextPreset: contextPresetRecommended, MaxOutputTokens: requested}
		choice := contextChoiceFromLibrary(item, modelInfo{ContextWindow: 1050000, MaxOutputTokens: 4096})
		resolved, err := contextPolicyForChoice(choice)
		want := 4096
		if requested > 0 {
			want = min(requested, want)
		}
		if err != nil || resolved.MaxOutputTokens != want || choice.Model.MaxOutputTokens != requested || choice.MaximumOutputTokens != 4096 {
			t.Fatalf("requested %d: choice %#v resolved %#v error %v", requested, choice, resolved, err)
		}
	}
}

func TestContextPresetsPersistThroughAtomicLibraryRestart(t *testing.T) {
	dir := t.TempDir()
	store := newModelLibraryStore(dir)
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/a", Models: []modelLibraryItem{
		{ID: "vendor/a", ContextPreset: contextPresetRecommended},
		{ID: "vendor/b", ContextPreset: contextPresetLow, MaxOutputTokens: 128000},
		{ID: "vendor/c", ContextPreset: contextPresetMaximum},
		{ID: "vendor/d", ContextPreset: contextPresetCustom, ContextWindow: 96000},
		{ID: "vendor/legacy", ContextWindow: 123456},
	}}
	if _, err := store.save(library, 0, false); err != nil {
		t.Fatal(err)
	}
	got := newModelLibraryStore(dir).snapshot()
	if got.RecoveryRequired || !reflect.DeepEqual(got.Library, library) {
		t.Fatalf("restart: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) != nil {
		t.Fatal("invalid persisted JSON")
	}
	for _, row := range decoded["models"].([]any) {
		item := row.(map[string]any)
		if item["contextPreset"] == "maximum" && item["contextWindow"] != nil {
			t.Fatal("Maximum persisted a stale numeric maximum")
		}
	}
}

func TestContextPresetValidationRejectsAmbiguousPolicies(t *testing.T) {
	for _, item := range []modelLibraryItem{
		{ID: "vendor/a", ContextPreset: "invalid"},
		{ID: "vendor/a", ContextPreset: contextPresetRecommended, ContextWindow: 200000},
		{ID: "vendor/a", ContextPreset: contextPresetLow, ContextWindow: 128000},
		{ID: "vendor/a", ContextPreset: contextPresetMaximum, ContextWindow: 1050000},
		{ID: "vendor/a", ContextPreset: contextPresetCustom},
	} {
		library := modelLibrary{SchemaVersion: 1, DefaultModel: item.ID, Models: []modelLibraryItem{item}}
		if validateModelLibrary(library) == nil {
			t.Fatalf("accepted %#v", item)
		}
	}
}

func TestContextCatalogAndOMPSwitchModelsWithoutGlobalOverride(t *testing.T) {
	choices := []nativeModelChoice{
		{Model: modelInfo{ID: "vendor/large", ContextWindow: 1050000, MaxOutputTokens: 128000}, ContextPreset: contextPresetLow},
		{Model: modelInfo{ID: "vendor/small", ContextWindow: 64000, MaxOutputTokens: 4096}, ContextPreset: contextPresetRecommended},
	}
	catalog, err := buildCodexCatalog(choices, "vendor/large", false)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Models []struct {
			Context int `json:"context_window"`
			Compact int `json:"auto_compact_token_limit"`
		} `json:"models"`
	}
	if json.Unmarshal(catalog, &parsed) != nil {
		t.Fatal("invalid catalog")
	}
	omp, err := ompSelectionFromChoices(choices, "vendor/large")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{128000, 64000} {
		if parsed.Models[i].Context != want || omp.Models[i].Context != want || parsed.Models[i].Compact >= want {
			t.Fatal("agent budgets diverged", parsed, omp)
		}
	}
	if omp.Models[0].Output != 32000 {
		t.Fatal("OMP reserved the whole low window for output")
	}
	choices[0].ContextPreset, choices[0].Model.ContextWindow = contextPresetMaximum, 0
	if _, err := buildCodexCatalog(choices, "vendor/large", false); err == nil {
		t.Fatal("Codex accepted unknown maximum")
	}
	if _, err := ompSelectionFromChoices(choices, "vendor/large"); err == nil {
		t.Fatal("OMP accepted unknown maximum")
	}
}
