package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func omnigentLegacySelectionFixture() modelLibrary {
	return modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/exact", StartingPack: modelLibraryStartRecommended, Roles: map[string]string{"vendor/exact": "Coding"}, Models: []modelLibraryItem{
		{ID: "vendor/exact", DisplayName: "Saved name", ReasoningCustom: true, ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "low"},
		{ID: "vendor/plain", ReasoningCustom: true, ReasoningLevels: []string{}, ContextWindow: 32768, MaxOutputTokens: 2048},
	}}
}

func TestOmnigentSelectionComparisonPreservesActualPolicy(t *testing.T) {
	saved := omnigentLegacySelectionFixture()
	transport := cloneModelLibrary(saved)
	transport.StartingPack, transport.Roles = "", nil
	transport.Models[0].ContextPreset = contextPresetRecommended
	transport.Models[0].ReasoningLevels = []string{"high", "none", "low"}
	transport.Models[1].ContextPreset = contextPresetCustom
	transport.Models[1].ReasoningLevels = []string{}
	before, _ := json.Marshal(saved)
	if !omnigentSameModelSelection(saved, transport) || !omnigentSameModelSelection(transport, saved) {
		t.Fatal("unchanged legacy selection rejected after native serialization")
	}
	for name, change := range map[string]func(*modelLibrary){
		"schema":         func(l *modelLibrary) { l.SchemaVersion = 2 },
		"id":             func(l *modelLibrary) { l.Models[1].ID = "vendor/different" },
		"order":          func(l *modelLibrary) { l.Models[0], l.Models[1] = l.Models[1], l.Models[0] },
		"default":        func(l *modelLibrary) { l.DefaultModel = l.Models[1].ID },
		"name":           func(l *modelLibrary) { l.Models[0].DisplayName = "Unsaved name" },
		"effort":         func(l *modelLibrary) { l.Models[0].ReasoningEffort = "high" },
		"levels":         func(l *modelLibrary) { l.Models[0].ReasoningLevels = []string{"low"} },
		"reasoning mode": func(l *modelLibrary) { l.Models[1].ReasoningCustom = false },
		"plain to none":  func(l *modelLibrary) { l.Models[1].ReasoningLevels = []string{"none"} },
		"context preset": func(l *modelLibrary) { l.Models[0].ContextPreset = contextPresetLow },
		"context budget": func(l *modelLibrary) { l.Models[1].ContextWindow++ },
		"output":         func(l *modelLibrary) { l.Models[1].MaxOutputTokens++ },
		"removed model":  func(l *modelLibrary) { l.Models = l.Models[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneModelLibrary(transport)
			change(&changed)
			if omnigentSameModelSelection(saved, changed) {
				t.Fatal("unsaved model policy change was accepted")
			}
		})
	}
	after, _ := json.Marshal(saved)
	if !bytes.Equal(before, after) {
		t.Fatal("selection comparison mutated the saved library")
	}
}

func TestOmnigentPrepareKeepsExactSavedLibraryAfterNativeSerialization(t *testing.T) {
	a := omnigentTestApp(t)
	library := omnigentLegacySelectionFixture()
	state, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.dir, "models.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	transport := cloneModelLibrary(library)
	transport.StartingPack, transport.Roles = "", nil
	transport.Models[0].ContextPreset = contextPresetRecommended
	transport.Models[1].ContextPreset = contextPresetCustom
	raw, _ := json.Marshal(map[string]any{"library": transport})
	if w := adminRequest(a, "clients/omnigent", string(raw)); w.Code != 200 {
		t.Fatalf("unchanged native selection failed preparation: %d %s", w.Code, w.Body.String())
	}
	saved, err := a.readOmnigentPrepared()
	if err != nil || !reflect.DeepEqual(saved.Library, state.Library) {
		t.Fatalf("preparation replaced the saved library with transport defaults: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || a.modelLibrary.snapshot().Revision != state.Revision {
		t.Fatal("preparation silently saved or changed the shared library")
	}
	transport.Models[0].ReasoningEffort = "high"
	raw, _ = json.Marshal(map[string]any{"library": transport})
	if w := adminRequest(a, "clients/omnigent", string(raw)); w.Code != 409 {
		t.Fatalf("unsaved reasoning change bypassed snapshot guard: %d", w.Code)
	}
	state.Library.Roles["vendor/exact"] = "Updated purpose"
	if _, err := a.modelLibrary.save(state.Library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	if a.omnigentReady(saved, a.launchRuntime()) {
		t.Fatal("full saved-library change detection was weakened")
	}
}
