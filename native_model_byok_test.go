//go:build desktop

package main

import (
	"encoding/json"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

func nativeByokLabelCount(nodes []input.SemanticNode, label string) int {
	count := 0
	for _, node := range nodes {
		if node.Desc.Label == label {
			count++
		}
	}
	return count
}

func nativeByokWidgetNodes(t *testing.T, u *nativeUI, widget layout.Widget) []input.SemanticNode {
	t.Helper()
	var ops op.Ops
	var router input.Router
	gtx := layout.Context{Ops: &ops, Source: router.Source(), Constraints: layout.Exact(image.Pt(1180, 5000)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now()}
	widget(gtx)
	router.Frame(&ops)
	return router.AppendSemantics(nil)
}

func TestNativeModelByokRequiresCurrentExplicitKiloAvailability(t *testing.T) {
	yes, no := true, false
	u := nativeTestUI(t)
	cases := []struct {
		name              string
		selected, current modelInfo
		ready             bool
		want              bool
	}{
		{"available", modelInfo{ID: "vendor/model"}, modelInfo{ID: "vendor/model", Connection: "kilo", HasUserByokAvailable: &yes}, true, true},
		{"false", modelInfo{ID: "vendor/model", HasUserByokAvailable: &yes}, modelInfo{ID: "vendor/model", Connection: "kilo", HasUserByokAvailable: &no}, true, false},
		{"missing", modelInfo{ID: "vendor/model", HasUserByokAvailable: &yes}, modelInfo{ID: "vendor/model", Connection: "kilo"}, true, false},
		{"no current catalog", modelInfo{ID: "vendor/model", Connection: "kilo", HasUserByokAvailable: &yes}, modelInfo{}, true, false},
		{"native ChatGPT", modelInfo{ID: "chatgpt/model", Connection: "chatgpt"}, modelInfo{ID: "chatgpt/model", Connection: "chatgpt", HasUserByokAvailable: &yes}, true, false},
		{"reserved subscription namespace", modelInfo{ID: "chatgpt/model"}, modelInfo{ID: "chatgpt/model", Connection: "kilo", HasUserByokAvailable: &yes}, true, false},
		{"selected other connection", modelInfo{ID: "vendor/model", Connection: "chatgpt"}, modelInfo{ID: "vendor/model", Connection: "kilo", HasUserByokAvailable: &yes}, true, false},
		{"catalog other connection", modelInfo{ID: "vendor/model"}, modelInfo{ID: "vendor/model", Connection: "chatgpt", HasUserByokAvailable: &yes}, true, false},
		{"unattributed catalog", modelInfo{ID: "vendor/model"}, modelInfo{ID: "vendor/model", HasUserByokAvailable: &yes}, true, false},
		{"disconnected Kilo", modelInfo{ID: "vendor/model"}, modelInfo{ID: "vendor/model", Connection: "kilo", HasUserByokAvailable: &yes}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u.state["kiloReady"] = tc.ready
			u.models = []modelInfo{tc.current}
			if got := nativeModelHasUserByok(tc.selected, u.modelByokIDs()); got != tc.want {
				t.Fatalf("BYOK = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNativeModelByokCatalogLibraryAndClientPicker(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name  string
		flag  *bool
		count int
	}{{"true", &yes, 1}, {"false", &no, 0}, {"missing", nil, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			u := nativeTestUI(t)
			u.state["kiloReady"] = true
			u.models = []modelInfo{
				{ID: "vendor/eligible", Name: "Eligible model", Connection: "kilo", HasUserByokAvailable: tc.flag, Tools: &yes, OutputModalities: []string{"text"}},
				{ID: "vendor/ordinary", Name: "Ordinary model", Connection: "kilo", HasUserByokAvailable: &no},
				{ID: "vendor/unknown", Name: "Unknown model", Connection: "kilo"},
				{ID: "chatgpt/native", Name: "Native ChatGPT", Connection: "chatgpt", HasUserByokAvailable: &yes},
			}
			u.page = "models"
			u.expanded["library.catalog"] = true
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 5000), now: time.Now()}
			h.frame()
			if got := nativeByokLabelCount(h.nodes(), "BYOK"); got != tc.count {
				t.Fatalf("catalog BYOK badges = %d, want %d", got, tc.count)
			}
			for _, name := range []string{"Eligible model", "Ordinary model", "Unknown model", "Native ChatGPT"} {
				h.target(name, semantic.CheckBox)
				count := 0
				for _, node := range h.nodes() {
					if node.Desc.Class == semantic.CheckBox && node.Desc.Label == name {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("model %q has %d selection controls, want exactly one", name, count)
				}
			}
			if tc.count == 0 {
				found := false
				for _, node := range h.nodes() {
					found = found || strings.Contains(node.Desc.Label, "current catalog does not report")
				}
				if !found {
					t.Fatal("missing honest BYOK empty-state copy")
				}
			}
			h.click("Eligible model", semantic.CheckBox)
			if len(u.library.selection.Models) != 1 {
				t.Fatal("BYOK section changed selection semantics")
			}
			h.click("Done", semantic.Button)
			if got := nativeByokLabelCount(h.nodes(), "BYOK"); got != tc.count {
				t.Fatalf("library BYOK badges = %d, want %d", got, tc.count)
			}
			picker := u.clientPicker("generic", u.clientState().selection("generic"))
			if got := nativeByokLabelCount(nativeByokWidgetNodes(t, u, picker), "BYOK"); got != tc.count {
				t.Fatalf("client picker BYOK badges = %d, want %d", got, tc.count)
			}
			if tc.flag != nil && *tc.flag {
				// Saved choice still carries true; a later account/catalog observation
				// is authoritative without deleting the user's model or defaults.
				u.models[0].HasUserByokAvailable = &no
				h.frame()
				if got := nativeByokLabelCount(h.nodes(), "BYOK"); got != 0 {
					t.Fatal("old selected availability survived current false")
				}
				u.models = nil
				h.frame()
				if got := nativeByokLabelCount(h.nodes(), "BYOK"); got != 0 || len(u.library.selection.Models) != 1 {
					t.Fatal("unavailable current catalog changed saved selection or kept BYOK")
				}
			}
		})
	}
}

func TestNativeModelByokPersistedCatalogNeedsFreshAccountObservation(t *testing.T) {
	u := nativeTestUI(t)
	yes, no := true, false
	model := modelInfo{ID: "vendor/persisted", Name: "Persisted model", Connection: "kilo", HasUserByokAvailable: &yes}
	data, err := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: u.owner.uiCatalogScope(), Models: []modelInfo{model}, FetchedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(u.owner.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicCatalogFile(filepath.Join(u.owner.dir, "model-catalog.json"), data); err != nil {
		t.Fatal(err)
	}
	again := newNativeUI(u.owner, func() {})
	t.Cleanup(again.shutdownModelLibrary)
	nativeTestWait(t, again, func() bool { return again.authenticated })
	if !again.catalogCached || len(again.models) != 1 || again.models[0].HasUserByokAvailable == nil || !*again.models[0].HasUserByokAvailable {
		t.Fatal("restart did not load the real persisted true observation")
	}
	if len(again.modelByokIDs()) != 0 {
		t.Fatal("disk cache was treated as current-account BYOK availability")
	}
	nativeSeedSharedForTest(t, again, model)
	for _, flag := range []*bool{&no, nil} {
		model.HasUserByokAvailable = flag
		raw, err := json.Marshal(map[string]any{"models": []modelInfo{model}, "revision": nativeNumber(again.state, "catalogRevision"), "complete": true})
		if err != nil || !again.applyModels(raw, nativeNumber(again.state, "catalogRevision"), "models") {
			t.Fatalf("fresh observation was not applied: %v", err)
		}
		if again.catalogCached || len(again.modelByokIDs()) != 0 {
			t.Fatal("false or missing current observation retained cached BYOK")
		}
		if len(again.library.selection.Models) != 1 {
			t.Fatal("availability refresh removed the saved model")
		}
	}
	model.HasUserByokAvailable = &yes
	// Another provider can fail while this Kilo row is fresh. Global
	// completeness does not invalidate that current-account observation.
	raw, _ := json.Marshal(map[string]any{"models": []modelInfo{model}, "revision": nativeNumber(again.state, "catalogRevision"), "complete": false})
	if !again.applyModels(raw, nativeNumber(again.state, "catalogRevision"), "models") || !again.modelByokIDs()[model.ID] {
		t.Fatal("fresh Kilo true was hidden by another provider's partial failure")
	}
}
