package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func inferenceImageBody(t *testing.T, path string, raw []byte, tool bool) []byte {
	t.Helper()
	if path == "/v1/responses" {
		return responseUploadBody(t, [][]byte{raw, raw}, tool, 0)
	}
	part := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), "detail": "high"}}
	if path == "/v1/messages" {
		part = map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(raw)}, "cache_control": map[string]any{"type": "ephemeral"}}
	}
	content := []any{part, part, map[string]any{"type": "text", "text": "Keep this text"}}
	if tool && path == "/v1/messages" {
		content = []any{map[string]any{"type": "tool_result", "tool_use_id": "call_preserved", "content": content}}
	}
	body, err := json.Marshal(map[string]any{"model": "test/model", "messages": []any{map[string]any{"role": "user", "content": content}}, "metadata": map[string]any{"exact": json.Number("9007199254740993")}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type inferenceTestLease struct {
	data          []byte
	calls, closed int
	fail          bool
	uploadErr     error
}

func (l *inferenceTestLease) Upload(_ context.Context, raw []byte, mime string) (string, error) {
	l.calls++
	if l.uploadErr != nil {
		return "", l.uploadErr
	}
	if l.fail {
		return "", errors.New("private uploader detail")
	}
	l.data = append([]byte(nil), raw...)
	return "https://example.com/private-image-capability.png", nil
}
func (l *inferenceTestLease) ExpiresAt() time.Time        { return time.Now().Add(time.Hour) }
func (l *inferenceTestLease) Close(context.Context) error { l.closed++; return nil }

func TestImageURLBackendsAcrossInferenceAPIs(t *testing.T) {
	original := responseUploadPNG(t, 1366, 1000, 4)
	for _, mode := range []string{"cloudflare", "litterbox", "tailscale"} {
		for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
			t.Run(mode+path, func(t *testing.T) {
				a := captureTestApp(t)
				a.config.ImageTransport = imageTransportSettings{Mode: mode, Profile: "high", LitterboxTTL: "12h"}
				lease := &inferenceTestLease{}
				a.imageURLLeaseFactory = func(_ context.Context, gotMode, ttl string) (imageURLLease, error) {
					if gotMode != mode || ttl != "12h" {
						t.Fatal("backend preference changed")
					}
					return lease, nil
				}
				a.attachmentClientFactory = func(string, string) *imageAttachmentClient { t.Fatal("unexpected Kilo storage fallback"); return nil }
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if len(body) > imageUploadRequestBudget || r.ContentLength != int64(len(body)) {
						t.Error("upstream body too large or incorrectly framed")
					}
					if strings.Contains(string(body), "base64,") || strings.Contains(string(body), `"type":"base64"`) {
						t.Error("inline data remains")
					}
					doc, err := decodeObject(body)
					if err != nil || object(doc["metadata"])["exact"] != json.Number("9007199254740993") {
						t.Error("non-image data changed")
					}
					if strings.Count(string(body), "https://example.com/private-image-capability.png") != 2 {
						t.Error("duplicate image references not replaced")
					}
					if path == "/v1/messages" && (!strings.Contains(string(body), `"tool_use_id":"call_preserved"`) || !strings.Contains(string(body), `"cache_control":{"type":"ephemeral"}`)) {
						t.Error("tool result or image metadata lost")
					}
					if path == "/v1/chat/completions" && !strings.Contains(string(body), `"detail":"high"`) {
						t.Error("image detail lost")
					}
					if lease.closed != 0 || !bytes.Equal(lease.data, original) {
						t.Error("image modified or deleted before inference")
					}
					if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("X-KiloCode-OrganizationId") != "team-id" {
						t.Error("gateway credentials changed")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"text\":\"done\"}\n\ndata: [DONE]\n\n")
				}))
				defer upstream.Close()
				setUpstream(a, upstream.URL)
				request := httptest.NewRequest("POST", "http://127.0.0.1:8877"+path, bytes.NewReader(inferenceImageBody(t, path, original, true)))
				request.Header.Set("Authorization", "Bearer local-secret")
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				a.inferenceHandler("upstream-secret", "team-id", "local-secret", "127.0.0.1:8877").ServeHTTP(response, request)
				if response.Code != 200 || !strings.Contains(response.Body.String(), "[DONE]") {
					t.Fatalf("status=%d %s", response.Code, response.Body.String())
				}
				if lease.calls != 1 || lease.closed != 1 || a.imageUploadsActive != 0 {
					t.Fatal("deduplication/cleanup failed")
				}
				trace, _ := json.Marshal(a.traces)
				if bytes.Contains(trace, []byte("private-image-capability")) {
					t.Fatal("public image capability leaked into trace")
				}
			})
		}
	}
}

func TestImageCompressionAcrossInferenceAPIs(t *testing.T) {
	original := responseUploadPNG(t, 1366, 1000, 9)
	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		r := httptest.NewRequest("POST", path, bytes.NewReader(inferenceImageBody(t, path, original, true)))
		prepared, err := prepareCompressedResponseImages(r, "high")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(prepared.Body)
		if len(body) >= imageUploadRequestBudget {
			t.Fatal("compression did not fit")
		}
		doc, _ := decodeObject(body)
		parts := inferenceImageParts(doc, path)
		if len(parts) != 2 {
			t.Fatal("image slots lost")
		}
		for _, part := range parts {
			if _, mime, err := decodeResponseImage(part.inline()); err != nil || mime != "image/png" {
				t.Fatal("compression damaged image source", err)
			}
		}
	}
}

func TestInferenceImageTraversalExcludesArbitraryData(t *testing.T) {
	inline := "data:image/png;base64,YQ=="
	doc := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": inline},
		map[string]any{"type": "tool_use", "input": map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "YQ=="}}},
		map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/image.png"}},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "file:///private.png"}},
	}}}}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		if len(inferenceImageParts(doc, path)) != 0 {
			t.Fatal("non-inline image position selected", path)
		}
	}
}

func TestExternalImageUploadFailureNeverFallsBack(t *testing.T) {
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "cloudflare", Profile: "high"}
	lease := &inferenceTestLease{fail: true}
	a.imageURLLeaseFactory = func(context.Context, string, string) (imageURLLease, error) { return lease, nil }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("failed upload reached inference") }))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	response := activityRequest(a, string(responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 2)}, false, 0)))
	if response.Code != 502 || strings.Contains(response.Body.String(), "private uploader detail") || lease.closed != 1 || a.imageUploadsActive != 0 {
		t.Fatal("failed upload leaked resources or private error", response.Code)
	}
}

func TestImageBackendFailureKeepsSanitizedStatus(t *testing.T) {
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "litterbox", Profile: "high"}
	lease := &inferenceTestLease{uploadErr: imageUploadProblem(502, "Litterbox rejected the upload (HTTP 412). No inference was sent.")}
	a.imageURLLeaseFactory = func(context.Context, string, string) (imageURLLease, error) { return lease, nil }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("rejected upload reached inference") }))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	response := activityRequest(a, string(responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 3)}, false, 0)))
	if response.Code != 502 || !strings.Contains(response.Body.String(), "HTTP 412") || lease.closed != 1 || a.imageUploadsActive != 0 {
		t.Fatal("actionable backend status or cleanup lost", response.Code)
	}
}

func TestExternalImageUploadPlanSupportsMoreThanFiveAndRejectsBackground(t *testing.T) {
	var images [][]byte
	for i := range 6 {
		images = append(images, responseUploadPNG(t, 1200, 950, i))
	}
	body := responseUploadBody(t, images, false, 0)
	plan, err := planInferenceImageUploads(body, "/v1/responses", 64)
	if err != nil || len(plan.images) != 6 {
		t.Fatal("external backend inherited Kilo's five-image quota", err)
	}
	doc, _ := decodeObject(body)
	doc["background"] = true
	body, _ = json.Marshal(doc)
	if _, err := planInferenceImageUploads(body, "/v1/responses", 64); err == nil {
		t.Fatal("async request would outlive temporary image lease")
	}
}
