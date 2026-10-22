package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteImageFallbackReplacesOnlyDocumentedImageSlots(t *testing.T) {
	dataImage := "data:image/png;base64,YQ=="
	tests := []struct {
		path string
		doc  map[string]any
	}{
		{
			path: "/v1/responses",
			doc: map[string]any{"input": []any{
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": "Keep this"},
					map[string]any{"type": "input_image", "image_url": "https://stale.example/image.png"},
					map[string]any{"type": "input_image", "image_url": dataImage},
				}},
			}},
		},
		{
			path: "/v1/chat/completions",
			doc: map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Keep this"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://stale.example/image.png"}},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataImage}},
			}}}},
		},
		{
			path: "/v1/messages",
			doc: map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "content": []any{
					map[string]any{"type": "text", "text": "Keep this"},
					map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://stale.example/image.png"}},
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "YQ=="}},
				}},
			}}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			before, err := json.Marshal(tc.doc)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+tc.path, bytes.NewReader(before))
			prepared, err := prepareRemoteImageFallback(r)
			if err != nil {
				t.Fatal(err)
			}
			plan, _ := prepared.Context().Value(remoteImageFallbackContextKey{}).(*remoteImageFallbackPlan)
			if plan == nil || plan.skipped != 1 {
				t.Fatalf("plan = %+v", plan)
			}
			if !bytes.Equal(readBody(t, prepared.Body), before) {
				t.Fatal("first request body changed")
			}
			preservedInline := dataImage
			if tc.path == "/v1/messages" {
				preservedInline = "YQ=="
			}
			if !strings.Contains(string(plan.body), remoteImageFallbackMarker) || !strings.Contains(string(plan.body), "Keep this") || !strings.Contains(string(plan.body), preservedInline) || strings.Contains(string(plan.body), "stale.example") {
				t.Fatalf("fallback body lost context or retained stale image: %s", plan.body)
			}
		})
	}
}

func TestRemoteImageFallbackRetriesOnceAfterDownloadFailure(t *testing.T) {
	payload := `{"model":"test/model","input":[{"role":"user","content":[{"type":"input_text","text":"Keep this"},{"type":"input_image","image_url":"https://stale.example/image.png"}]}]}`
	var bodies []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Error while downloading file."}}`)
			return
		}
		io.WriteString(w, `{"output":[]}`)
	}))
	defer upstream.Close()

	a := testApp(t)
	setUpstream(a, upstream.URL)
	response := activityRequest(a, payload)
	if response.Code != http.StatusOK || response.Body.String() != `{"output":[]}` {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if len(bodies) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], "stale.example") || strings.Contains(bodies[1], "stale.example") || !strings.Contains(bodies[1], remoteImageFallbackMarker) || !strings.Contains(bodies[1], "Keep this") {
		t.Fatalf("retry bodies did not preserve text and omit image: %#v", bodies)
	}
}

func TestRemoteImageFallbackDoesNotRetryUnrelatedErrors(t *testing.T) {
	payload := `{"model":"test/model","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://valid.example/image.png"}]}]}`
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"invalid request"}}`)
	}))
	defer upstream.Close()

	a := testApp(t)
	setUpstream(a, upstream.URL)
	response := activityRequest(a, payload)
	if response.Code != http.StatusBadRequest || calls != 1 {
		t.Fatalf("response = %d, calls = %d", response.Code, calls)
	}
}

func readBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
