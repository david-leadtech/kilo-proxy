//go:build desktop

package main

import (
	"encoding/json"
	"image"
	"reflect"
	"strings"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func nativeContextModels() []modelInfo {
	return []modelInfo{
		{ID: "openai/large", Name: "Large context", ContextWindow: 1050000, MaxOutputTokens: 128000},
		{ID: "vendor/small", Name: "Small context", ContextWindow: 200000, MaxOutputTokens: 16384},
	}
}

func TestNativeContextPresetsAutosaveAndRestart(t *testing.T) {
	u := nativeTestUI(t)
	u.models = nativeContextModels()
	u.page = "models"
	nativeSeedSharedForTest(t, u, u.models...)
	nativeTestFrame(t, u)
	for _, choice := range u.library.selection.Models {
		if choice.ContextPreset != contextPresetRecommended {
			t.Fatalf("new model did not start at Recommended: %#v", choice)
		}
	}
	u.clickable("models.context.low").Click()
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	for _, saved := range u.owner.modelLibrary.snapshot().Library.Models {
		if saved.ContextPreset != contextPresetLow || saved.ContextWindow != 0 {
			t.Fatalf("bulk preset did not save policy independently of capacity: %#v", saved)
		}
	}
	// A per-model Custom override is an ordinary auto-saved editor. It retains
	// the requested budget even when that budget is capped by model capacity.
	u.clickable("client:shared:edit:openai/large").Click()
	nativeTestFrame(t, u)
	u.clickable(nativeClientField(sharedModelKey, "openai/large", "context-preset:custom")).Click()
	nativeTestFrame(t, u)
	u.setValue(nativeClientField(sharedModelKey, "openai/large", "context"), "400000")
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	saved := u.owner.modelLibrary.snapshot().Library
	large := nativeSavedLibraryItem(t, saved, "openai/large")
	if large.ContextPreset != contextPresetCustom || large.ContextWindow != 400000 {
		t.Fatalf("custom edit was not autosaved: %#v", large)
	}
	owner, err := newApp(u.owner.dir, &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	reopened := newNativeUI(owner, func() {})
	t.Cleanup(func() { owner.requestQuit(); reopened.shutdownModelLibrary() })
	if got := reopened.modelLibraryValue(); !reflect.DeepEqual(got, saved) {
		t.Fatalf("restart changed context policy: got %#v want %#v", got, saved)
	}
	reopened.models = nativeContextModels()
	reopened.syncClientSelection(sharedModelKey, reopened.library.selection)
	limits, err := contextPolicyForChoice(*reopened.library.selection.choice("openai/large"))
	if err != nil || limits.ContextWindow != 400000 || limits.MaximumContextWindow != 1050000 {
		t.Fatalf("restart confused custom budget with maximum: %#v %v", limits, err)
	}
}

func TestNativeContextCatalogRefreshKeepsPolicyAndResolvesEachClient(t *testing.T) {
	u := nativeTestUI(t)
	u.models = nativeContextModels()
	nativeSeedSharedForTest(t, u, u.models...)
	u.models[0].ContextWindow = 250000
	u.models[0].MaxOutputTokens = 16000
	u.syncClientSelection(sharedModelKey, u.library.selection)
	choice := u.library.selection.choice("openai/large")
	if choice.ContextPreset != contextPresetRecommended || choice.ContextTokens != 0 || choice.Model.ContextWindow != 250000 {
		t.Fatalf("catalog refresh replaced policy or retained old capacity: %#v", choice)
	}
	for _, client := range []string{"codex", "codex-cli", "opencode", "omp", "zed", "claude"} {
		t.Run(client, func(t *testing.T) {
			selection := u.sharedClientSelection(client)
			payload, err := nativeClientPayload(client, selection)
			if err != nil {
				t.Fatal(err)
			}
			var got int
			switch typed := payload.(type) {
			case editorSelection:
				got = typed.Models[0].Context
				if typed.Models[0].Output != 16000 {
					t.Fatal("gateway output cap not applied")
				}
			case ompSelection:
				got = typed.Models[0].Context
			case claudeSelection:
				got = typed.Models[0].Context
			case map[string]any:
				var catalog struct {
					Models []struct {
						Context int `json:"context_window"`
					} `json:"models"`
				}
				if err := json.Unmarshal(typed["catalog"].(json.RawMessage), &catalog); err != nil {
					t.Fatal(err)
				}
				got = catalog.Models[0].Context
			default:
				t.Fatalf("unexpected payload %T", payload)
			}
			if got != 250000 {
				t.Fatalf("agent received %d tokens instead of capped Recommended", got)
			}
		})
	}
}

func TestNativeContextMaximumUnknownNeverPartiallyApplies(t *testing.T) {
	u := nativeTestUI(t)
	u.models = append(nativeContextModels(), modelInfo{ID: "private/unknown", Name: "Private model"})
	nativeSeedSharedForTest(t, u, u.models...)
	u.page = "models"
	before := u.modelLibraryValue()
	if u.applySharedContext(contextPresetMaximum, 0) {
		t.Fatal("Maximum accepted invented catalog capacity")
	}
	if !reflect.DeepEqual(before, u.modelLibraryValue()) {
		t.Fatal("failed bulk action changed part of the library")
	}
	nativeTestFrame(t, u)
	u.clickable("models.context.maximum").Click()
	nativeTestFrame(t, u)
	if !reflect.DeepEqual(before, u.modelLibraryValue()) {
		t.Fatal("disabled Maximum button changed the library")
	}
	unknown := u.library.selection.choice("private/unknown")
	if unknown.Model.ContextWindow != 0 || !strings.Contains(u.contextChoiceSummary(*unknown), "unknown") {
		t.Fatal("unknown model advertised fabricated maximum")
	}
}

func TestNativeContextPresetControlsFitAndPointerApply(t *testing.T) {
	for _, size := range []image.Point{{1180, 900}, {780, 900}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.models = nativeContextModels()[:1]
				u.page, u.language = "models", lang
				nativeSeedSharedForTest(t, u, u.models...)
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				for _, label := range []string{"● " + u.contextPresetLabel(contextPresetRecommended), u.contextPresetLabel(contextPresetLow), u.contextPresetLabel(contextPresetMaximum), u.contextPresetLabel(contextPresetCustom)} {
					bounds := h.target(label, semantic.Button).Desc.Bounds
					if !bounds.In(image.Rectangle{Max: size}) {
						t.Fatalf("preset falls outside viewport: %s %v", label, bounds)
					}
				}
				h.click(u.contextPresetLabel(contextPresetLow), semantic.Button)
				u.flushModelLibrary()
				if nativeSavedLibraryItem(t, u.owner.modelLibrary.snapshot().Library, "openai/large").ContextPreset != contextPresetLow {
					t.Fatal("pointer action did not persist Low")
				}
				nativeGridCapture(t, h, "native-context-presets-"+fmtSize(size)+"-"+lang)
			})
		}
	}
}

func TestNativeContextBulkCustomDraftAndMaximumPersistPolicies(t *testing.T) {
	u := nativeTestUI(t)
	u.models = nativeContextModels()
	u.page = "models"
	nativeSeedSharedForTest(t, u, u.models...)
	nativeTestFrame(t, u)
	u.clickable("models.context.custom").Click()
	nativeTestFrame(t, u)
	before := u.modelLibraryValue()
	u.setValue("models.context.tokens", "unfinished")
	u.clickable("models.context.apply").Click()
	nativeTestFrame(t, u)
	if !reflect.DeepEqual(before, u.modelLibraryValue()) || !strings.Contains(u.notice, "whole numbers") {
		t.Fatal("incomplete bulk draft modified the saved model choices")
	}
	u.setValue("models.context.tokens", "400000")
	u.clickable("models.context.apply").Click()
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	for _, item := range u.owner.modelLibrary.snapshot().Library.Models {
		if item.ContextPreset != contextPresetCustom || item.ContextWindow != 400000 {
			t.Fatalf("bulk custom did not preserve requested tokens: %#v", item)
		}
	}
	limits, err := contextPolicyForChoice(*u.library.selection.choice("vendor/small"))
	if err != nil || limits.ContextWindow != 200000 {
		t.Fatalf("custom request did not respect the smaller model: %#v %v", limits, err)
	}
	u.clickable("models.context.maximum").Click()
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	for _, item := range u.owner.modelLibrary.snapshot().Library.Models {
		if item.ContextPreset != contextPresetMaximum || item.ContextWindow != 0 {
			t.Fatalf("Maximum froze catalog capacity in the saved selection: %#v", item)
		}
	}
	u.clickable("models.context.recommended").Click()
	nativeTestFrame(t, u)
	u.flushModelLibrary()
	large, err := contextPolicyForChoice(*u.library.selection.choice("openai/large"))
	if err != nil || large.ContextWindow != 272000 {
		t.Fatalf("Recommended did not reduce the previous Maximum budget: %#v %v", large, err)
	}
}
