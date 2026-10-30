package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestHeadlessCatalogByokPartialPreservesOnlyFreshAvailability(t *testing.T) {
	for _, failedProvider := range []string{"kilo", "chatgpt"} {
		t.Run(failedProvider, func(t *testing.T) {
			a := subscriptionTestApp(t)
			a.apiKey, a.config.OrgID = "synthetic-key", "synthetic-team"
			var partial atomic.Bool
			kilo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-key" || r.Header.Get("X-KiloCode-OrganizationId") != "synthetic-team" {
					t.Error("Kilo catalog context changed")
				}
				if partial.Load() && failedProvider == "kilo" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				jsonResponse(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
					"id": "vendor/one", "name": "Kilo model", "context_length": 100000,
					"supported_parameters": []string{"tools"}, "mayTrainOnYourPrompts": false,
					"pricing":              map[string]any{"prompt": "0.000002", "completion": "0.000005"},
					"hasUserByokAvailable": true,
				}}})
			}))
			defer kilo.Close()
			subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if partial.Load() && failedProvider == "chatgpt" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				jsonResponse(w, http.StatusOK, map[string]any{"models": []any{map[string]any{"slug": "gpt-test", "display_name": "Subscription model"}}})
			}))
			defer subscription.Close()
			setUpstream(a, kilo.URL)
			a.chatgpt.backendURL = subscription.URL

			var full headlessCatalogState
			w := adminRequest(a, "headless/catalog", "{}")
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &full) != nil || !full.Complete || len(full.Models) != 2 {
				t.Fatalf("full catalog failed: %d %s", w.Code, w.Body.String())
			}
			expected := make(map[string]modelInfo, len(full.Models))
			for _, model := range full.Models {
				expected[model.ID] = model
			}
			kiloModel := expected["vendor/one"]
			if kiloModel.HasUserByokAvailable == nil || !*kiloModel.HasUserByokAvailable {
				t.Fatal("initial Kilo BYOK availability missing")
			}
			if failedProvider == "kilo" {
				kiloModel.HasUserByokAvailable = nil
				expected[kiloModel.ID] = kiloModel
			}

			partial.Store(true)
			w = adminRequest(a, "headless/catalog", "{}")
			var got headlessCatalogState
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Complete || !got.FetchedAt.IsZero() || len(got.Models) != 2 {
				t.Fatalf("partial catalog lost models or became complete: %d %s", w.Code, w.Body.String())
			}
			for _, model := range got.Models {
				want, exists := expected[model.ID]
				if !exists || !reflect.DeepEqual(model, want) {
					t.Fatalf("partial catalog changed availability/capabilities: got=%+v want=%+v", model, want)
				}
			}
			// Preserve fresh-result ordering, then append the unavailable provider's cached row.
			wantIDs := []string{"vendor/one", "chatgpt/gpt-test"}
			if failedProvider == "kilo" {
				wantIDs[0], wantIDs[1] = wantIDs[1], wantIDs[0]
			}
			if got.Models[0].ID != wantIDs[0] || got.Models[1].ID != wantIDs[1] {
				t.Fatalf("partial catalog ordering changed: %+v", got.Models)
			}
			cached := readNativeCachedCatalog(a.dir, a.catalogScopeLocked())
			if !cached.FetchedAt.IsZero() || !reflect.DeepEqual(cached.Models, got.Models) {
				t.Fatal("persisted partial catalog differs from returned availability")
			}
		})
	}
}

func TestHeadlessCatalogByokCachedGetDoesNotClaimCurrentAvailability(t *testing.T) {
	a := testApp(t)
	a.apiKey, a.config.OrgID = "synthetic-key", "synthetic-team"
	var calls atomic.Int32
	kilo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]any{"data": []any{map[string]any{
			"id": "vendor/one", "name": "Kilo model", "context_length": 100000,
			"supported_parameters": []string{"tools"}, "hasUserByokAvailable": true,
		}}})
	}))
	defer kilo.Close()
	setUpstream(a, kilo.URL)
	var fresh headlessCatalogState
	w := adminRequest(a, "headless/catalog", "{}")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &fresh) != nil || !fresh.Complete || len(fresh.Models) != 1 || fresh.Models[0].HasUserByokAvailable == nil || !*fresh.Models[0].HasUserByokAvailable {
		t.Fatalf("fresh catalog unavailable: %d %s", w.Code, w.Body.String())
	}

	w = adminRequest(a, "headless/catalog", "")
	var saved headlessCatalogState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.Complete != fresh.Complete || !saved.FetchedAt.Equal(fresh.FetchedAt) || saved.Revision != fresh.Revision || len(saved.Models) != 1 {
		t.Fatalf("cached catalog metadata changed: %d %s", w.Code, w.Body.String())
	}
	want := fresh.Models[0]
	want.HasUserByokAvailable = nil
	if !reflect.DeepEqual(saved.Models[0], want) {
		t.Fatalf("cached GET retained availability or changed capabilities: got=%+v want=%+v", saved.Models[0], want)
	}
	persisted := readNativeCachedCatalog(a.dir, a.catalogScopeLocked())
	if len(persisted.Models) != 1 || persisted.Models[0].HasUserByokAvailable == nil || !*persisted.Models[0].HasUserByokAvailable {
		t.Fatal("cached GET modified persisted catalog")
	}
	if calls.Load() != 1 {
		t.Fatal("cached GET unexpectedly fetched a provider")
	}

	w = adminRequest(a, "headless/catalog", "{}")
	var refreshed headlessCatalogState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &refreshed) != nil || len(refreshed.Models) != 1 || !reflect.DeepEqual(refreshed.Models[0], fresh.Models[0]) || calls.Load() != 2 {
		t.Fatalf("fresh refresh lost current BYOK availability: %d %s", w.Code, w.Body.String())
	}
}
