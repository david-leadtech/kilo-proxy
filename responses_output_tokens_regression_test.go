package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func responsesRegressionLibrary(t *testing.T, a *app, items []modelLibraryItem) {
	t.Helper()
	library := emptyModelLibrary()
	library.Models = items
	if len(items) > 0 {
		library.DefaultModel = items[0].ID
	}
	if _, err := a.modelLibrary.save(library, 0, false); err != nil {
		t.Fatal(err)
	}
}

func responsesRegressionCatalog(t *testing.T, a *app, scope string, models []modelInfo) {
	t.Helper()
	data, err := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: scope, Models: models, FetchedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.dir, "model-catalog.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesOutputTokensPublishedCeiling(t *testing.T) {
	const id = "anthropic/fixture"
	item := modelLibraryItem{ID: id, ContextPreset: contextPresetRecommended, MaxOutputTokens: 128000}
	metadata := modelInfo{ID: id, ContextWindow: 1050000, MaxOutputTokens: 4096}
	resolved, err := contextPolicyForChoice(contextChoiceFromLibrary(item, metadata))
	if err != nil || resolved.MaxOutputTokens != 4096 {
		t.Fatalf("existing published-ceiling contract: %#v %v", resolved, err)
	}
	var forwarded int
	var forwardedPresent bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		_, forwardedPresent = body["max_output_tokens"]
		if value, ok := body["max_output_tokens"].(float64); ok {
			forwarded = int(value)
			if value > 4096 {
				http.Error(w, "provider published output ceiling is 4096", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"response_fixture"}`)
	}))
	defer upstream.Close()
	a := testApp(t)
	setUpstream(a, upstream.URL)
	responsesRegressionLibrary(t, a, []modelLibraryItem{item})
	a.mu.Lock()
	scope := a.catalogScopeLocked()
	a.mu.Unlock()
	responsesRegressionCatalog(t, a, scope, []modelInfo{metadata})
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", strings.NewReader(`{"model":"anthropic/fixture","input":"hello"}`))
	r.Header.Set("Authorization", "Bearer local-secret")
	w := httptest.NewRecorder()
	a.inferenceHandler("upstream-secret", "company-org", "local-secret", r.Host).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !forwardedPresent || forwarded != 4096 {
		t.Fatalf("valid selection rejected after output injection: status=%d field_present=%t forwarded_max=%d; existing effective ceiling=%d; %s", w.Code, forwardedPresent, forwarded, resolved.MaxOutputTokens, w.Body)
	}
}

func TestResponsesOutputTokensCatalogScopeAndPriority(t *testing.T) {
	for _, test := range []struct {
		name                             string
		libraryLimit, catalogLimit, want int
		wrongScope                       bool
	}{
		{"published ceiling caps saved override", 128000, 4096, 4096, false},
		{"smaller saved override wins", 1024, 4096, 1024, false},
		{"wrong organization cannot cap saved override", 128000, 4096, 128000, true},
		{"zero published ceiling does not cap", 128000, 0, 128000, false},
		{"negative published ceiling does not cap", 128000, -1, 128000, false},
		{"catalog fallback for unset override", 0, 4096, 4096, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := testApp(t)
			responsesRegressionLibrary(t, a, []modelLibraryItem{{ID: "vendor/model", ContextPreset: contextPresetRecommended, MaxOutputTokens: test.libraryLimit}})
			a.mu.Lock()
			scope := a.catalogScopeLocked()
			a.mu.Unlock()
			if test.wrongScope {
				scope += "-other"
			}
			responsesRegressionCatalog(t, a, scope, []modelInfo{{ID: "vendor/model", MaxOutputTokens: test.catalogLimit}})
			if got := a.responsesMaxOutputTokensLimit("~vendor/model"); got != test.want {
				t.Fatalf("limit=%d want=%d", got, test.want)
			}
		})
	}
}

func TestResponsesOutputTokensProtocolsAndStreaming(t *testing.T) {
	const sse = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"fixture\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"response_fixture\",\"status\":\"completed\",\"output\":[]}}\n\n"
	for _, test := range []struct {
		name, path, payload        string
		wantLimit                  int
		exact, compressed, chunked bool
	}{
		{"responses stream remains exact", "/v1/responses", `{"model":"anthropic/fixture","input":"hello","stream":true}`, 4096, false, false, false},
		{"chunked request length repaired", "/v1/responses", `{"model":"anthropic/fixture","input":"hello","stream":true}`, 4096, false, false, true},
		{"client explicit limit retained", "/v1/responses", ` { "model":"anthropic/fixture", "max_output_tokens":100000, "input":"hello" } `, 100000, true, false, false},
		{"client null retained", "/v1/responses", ` { "model":"anthropic/fixture", "max_output_tokens":null, "input":"hello" } `, 0, true, false, false},
		{"unknown model exact bytes", "/v1/responses", ` { "model":"unknown/fixture", "input":"hello" } `, 0, true, false, false},
		{"Claude Messages unaffected", "/v1/messages", ` { "model":"anthropic/fixture", "max_tokens":1000, "messages":[] } `, 0, true, false, false},
		{"OpenCode Chat Completions unaffected", "/v1/chat/completions", ` { "model":"anthropic/fixture", "messages":[] } `, 0, true, false, false},
		{"compressed body bypass unchanged", "/v1/responses", `{"model":"anthropic/fixture","input":"hello"}`, 0, true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := []byte(test.payload)
			if test.compressed {
				var buffer bytes.Buffer
				writer := gzip.NewWriter(&buffer)
				_, _ = writer.Write(payload)
				_ = writer.Close()
				payload = buffer.Bytes()
			}
			var forwarded []byte
			var length int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded, _ = io.ReadAll(r.Body)
				length = r.ContentLength
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, sse)
			}))
			defer upstream.Close()
			a := testApp(t)
			setUpstream(a, upstream.URL)
			responsesRegressionLibrary(t, a, []modelLibraryItem{{ID: "anthropic/fixture", ContextPreset: contextPresetRecommended, MaxOutputTokens: 4096}})
			r := httptest.NewRequest("POST", "http://127.0.0.1:8877"+test.path, bytes.NewReader(payload))
			r.Header.Set("Authorization", "Bearer local-secret")
			if test.compressed {
				r.Header.Set("Content-Encoding", "gzip")
			}
			if test.chunked {
				r.ContentLength = -1
				r.TransferEncoding = []string{"chunked"}
			}
			w := httptest.NewRecorder()
			a.inferenceHandler("upstream-secret", "company-org", "local-secret", r.Host).ServeHTTP(w, r)
			if test.compressed {
				// Existing routing rejects compressed JSON before this adapter.
				if w.Code != http.StatusBadRequest || len(forwarded) != 0 {
					t.Fatalf("compressed request routing changed: %d %s", w.Code, w.Body)
				}
				return
			}
			if w.Code != http.StatusOK || w.Body.String() != sse {
				t.Fatalf("SSE/status changed: %d %q", w.Code, w.Body.String())
			}
			if test.exact && !bytes.Equal(forwarded, payload) {
				t.Fatalf("unrelated body changed: %s", forwarded)
			}
			if test.wantLimit != 0 {
				var body map[string]any
				if json.Unmarshal(forwarded, &body) != nil || body["max_output_tokens"] != float64(test.wantLimit) {
					t.Fatalf("wrong output limit: %s", forwarded)
				}
			}
			if test.chunked && length >= 0 && length != int64(len(forwarded)) {
				t.Fatalf("forwarded length=%d bytes=%d", length, len(forwarded))
			}
		})
	}
}

func TestResponsesOutputTokensChatGPTSubscriptionBypass(t *testing.T) {
	a := subscriptionTestApp(t)
	responsesRegressionLibrary(t, a, []modelLibraryItem{{ID: "chatgpt/gpt-test", MaxOutputTokens: 128000}})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, exists := body["max_output_tokens"]; exists {
			t.Error("subscription received gateway output injection")
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-chatgpt-access" {
			t.Error("subscription routing changed")
		}
		subscriptionTextSSE(w)
	}))
	defer upstream.Close()
	a.chatGPTResponsesURL = upstream.URL
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", strings.NewReader(`{"model":"chatgpt/gpt-test","input":"hello"}`))
	r.Header.Set("Authorization", "Bearer "+a.config.LocalKey)
	w := httptest.NewRecorder()
	a.inferenceHandler("kilo-must-not-leave", "company-org", a.config.LocalKey, r.Host).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Subscription works") {
		t.Fatal(fmt.Sprintf("subscription response: %d %s", w.Code, w.Body))
	}
}

func TestResponsesOutputTokensCompressedInputTransformBypass(t *testing.T) {
	a := testApp(t)
	responsesRegressionLibrary(t, a, []modelLibraryItem{{ID: "vendor/model", MaxOutputTokens: 4096}})
	original := []byte("compressed-wire-fixture")
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", bytes.NewReader(original))
	r.Header.Set("Content-Encoding", "gzip")
	if err := a.prepareResponsesMaxOutputTokens(r, "vendor/model"); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("compressed bytes changed: %q %v", got, err)
	}
}
