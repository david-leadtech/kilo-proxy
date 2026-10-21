package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func saveTestLibrary(t *testing.T, a *app, models []modelLibraryItem) {
	t.Helper()
	library := emptyModelLibrary()
	library.Models = models
	library.DefaultModel = models[0].ID
	if _, err := a.modelLibrary.save(library, 0, false); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesMaxOutputTokensInjection(t *testing.T) {
	const dummy = `{"id":"resp_fixture"}`
	libraryOverride := modelLibraryItem{ID: "vendor/model", ContextWindow: 272000, MaxOutputTokens: 128000}
	for _, test := range []struct {
		name      string
		model     string
		payload   string
		library   []modelLibraryItem
		catalog   []modelInfo
		wantToken any
		wantBytes string
	}{
		{
			name:      "library override injected",
			model:     "vendor/model",
			payload:   `{"model":"vendor/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			library:   []modelLibraryItem{libraryOverride},
			wantToken: float64(128000),
		},
		{
			name:      "library override injected for tilde model",
			model:     "~vendor/model",
			payload:   `{"model":"~vendor/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			library:   []modelLibraryItem{libraryOverride},
			wantToken: float64(128000),
		},
		{
			name:      "catalog fallback injected",
			model:     "vendor/model",
			payload:   `{"model":"vendor/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			library:   []modelLibraryItem{{ID: "other/model", ContextWindow: 272000, MaxOutputTokens: 999}},
			catalog:   []modelInfo{{ID: "vendor/model", MaxOutputTokens: 4096}},
			wantToken: float64(4096),
		},
		{
			name:      "client value wins",
			model:     "vendor/model",
			payload:   `{"model":"vendor/model","max_output_tokens":1000,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			library:   []modelLibraryItem{libraryOverride},
			wantBytes: `{"model":"vendor/model","max_output_tokens":1000,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		},
		{
			name:      "unknown model keeps exact bytes",
			model:     "unknown/model",
			payload:   `{"model":"unknown/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
			library:   []modelLibraryItem{libraryOverride},
			wantBytes: `{"model":"unknown/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var forwarded []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded, _ = io.ReadAll(r.Body)
				if r.URL.Path != "/api/gateway/responses" {
					t.Errorf("path = %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, dummy)
			}))
			defer upstream.Close()
			a := testApp(t)
			setUpstream(a, upstream.URL)
			if len(test.library) > 0 {
				saveTestLibrary(t, a, test.library)
			}
			if test.catalog != nil {
				a.mu.Lock()
				org := a.catalogScopeLocked()
				a.mu.Unlock()
				cache, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: org, Models: test.catalog, FetchedAt: time.Now().UTC()})
				if err := os.WriteFile(filepath.Join(a.dir, "model-catalog.json"), cache, 0600); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", bytes.NewReader([]byte(test.payload)))
			r.Header.Set("Authorization", "Bearer local-secret")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			a.inferenceHandler("upstream-secret", "company-org", "local-secret", "127.0.0.1:8877").ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("unexpected response %d %s", w.Code, w.Body)
			}
			if len(forwarded) == 0 {
				t.Fatal("upstream never received the request")
			}
			if test.wantBytes != "" {
				if string(forwarded) != test.wantBytes {
					t.Fatalf("forwarded body changed: %s", forwarded)
				}
			} else {
				var doc map[string]any
				if err := json.Unmarshal(forwarded, &doc); err != nil {
					t.Fatalf("forwarded body is not JSON: %s", forwarded)
				}
				if doc["max_output_tokens"] != test.wantToken {
					t.Fatalf("max_output_tokens = %v, want %v; body %s", doc["max_output_tokens"], test.wantToken, forwarded)
				}
			}
		})
	}
}

func TestResponsesMaxOutputTokensHeaders(t *testing.T) {
	payload := `{"model":"vendor/model","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	var forwarded []byte
	var forwardLength int64
	var forwardEncoding string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded, _ = io.ReadAll(r.Body)
		forwardLength = r.ContentLength
		forwardEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"resp_fixture"}`)
	}))
	defer upstream.Close()
	a := testApp(t)
	setUpstream(a, upstream.URL)
	saveTestLibrary(t, a, []modelLibraryItem{{ID: "vendor/model", ContextWindow: 272000, MaxOutputTokens: 128000}})
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", bytes.NewReader([]byte(payload)))
	r.Header.Set("Authorization", "Bearer local-secret")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.inferenceHandler("upstream-secret", "company-org", "local-secret", "127.0.0.1:8877").ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body)
	}
	if len(forwarded) == 0 {
		t.Fatal("upstream never received the request")
	}
	var doc map[string]any
	if err := json.Unmarshal(forwarded, &doc); err != nil {
		t.Fatalf("forwarded body is not JSON: %s", forwarded)
	}
	if doc["max_output_tokens"] != float64(128000) {
		t.Fatalf("max_output_tokens = %v, want 128000", doc["max_output_tokens"])
	}
	if forwardLength != int64(len(forwarded)) {
		t.Fatalf("ContentLength = %d, want %d", forwardLength, len(forwarded))
	}
	if forwardEncoding != "" {
		t.Fatalf("stale Content-Encoding %q", forwardEncoding)
	}
}

func TestResponsesMaxOutputTokensInvalidJSONPassthrough(t *testing.T) {
	a := testApp(t)
	saveTestLibrary(t, a, []modelLibraryItem{{ID: "vendor/model", ContextWindow: 272000, MaxOutputTokens: 128000}})
	r := httptest.NewRequest("POST", "http://127.0.0.1:8877/v1/responses", strings.NewReader(`{"model":"vendor/model","input":`))
	if err := a.prepareResponsesMaxOutputTokens(r, "vendor/model"); err != nil {
		t.Fatalf("invalid JSON must pass through unchanged: %v", err)
	}
	got, _ := io.ReadAll(r.Body)
	if string(got) != `{"model":"vendor/model","input":` {
		t.Fatalf("body changed: %s", got)
	}
}
