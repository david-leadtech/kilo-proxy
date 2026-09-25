package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func responseUploadPNG(t *testing.T, width, height, seed int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x + seed), G: byte(y + seed), B: byte(seed), A: 255})
		}
	}
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func responseUploadBody(t *testing.T, images [][]byte, tool bool, padding int) []byte {
	t.Helper()
	content := []any{map[string]any{"type": "input_text", "text": "Preserve this text " + strings.Repeat("p", padding)}}
	for _, raw := range images {
		content = append(content, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw), "detail": "high"})
	}
	input := []any{map[string]any{"role": "user", "content": content}}
	if tool {
		input = []any{
			map[string]any{"role": "user", "content": "Review the tool's original images"},
			map[string]any{"type": "function_call", "name": "read_images", "call_id": "call_original", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "call_original", "output": content},
		}
	}
	raw, err := json.Marshal(map[string]any{"model": "openai/synthetic", "input": input, "metadata": map[string]any{"exact": json.Number("9007199254740993")}, "reasoning": map[string]string{"effort": "high"}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestResponseImageUploadPlanKeepsOriginalsAndDeduplicates(t *testing.T) {
	image := responseUploadPNG(t, 1366, 1000, 1)
	for _, tool := range []bool{false, true} {
		body := responseUploadBody(t, [][]byte{image, image, image, image}, tool, 0)
		plan, err := planResponseImageUploads(body)
		if err != nil || plan == nil {
			t.Fatalf("tool %t: %v", tool, err)
		}
		if len(plan.images) != 1 || len(plan.images[0].parts) != 4 || !bytes.Equal(plan.images[0].data, image) || plan.images[0].mime != "image/png" {
			t.Fatal("image was changed or uploaded repeatedly")
		}
		original, _ := decodeObject(body)
		for _, key := range []string{"metadata", "model", "reasoning"} {
			if !reflect.DeepEqual(original[key], plan.doc[key]) {
				t.Fatalf("%s changed", key)
			}
		}
		if tool && object(plan.doc["input"].([]any)[2])["call_id"] != "call_original" {
			t.Fatal("tool call identity changed")
		}
	}
}

func TestResponseImageUploadPlanUsesOnlyNeededImagesAndFailsBeforeUpload(t *testing.T) {
	large := responseUploadPNG(t, 1366, 1000, 1)
	small := responseUploadPNG(t, 16, 16, 2)
	plan, err := planResponseImageUploads(responseUploadBody(t, [][]byte{large, small}, false, 0))
	if err != nil || len(plan.images) != 1 || !bytes.Equal(plan.images[0].data, large) {
		t.Fatal("did not select only the necessary image", err)
	}
	var images [][]byte
	for i := range 6 {
		images = append(images, responseUploadPNG(t, 1200, 950, i))
	}
	// Removing five leaves one >4.4MB inline. Do not create extra messages to
	// bypass Kilo's per-message quota, or upload anything before this check.
	if plan, err := planResponseImageUploads(responseUploadBody(t, images, false, 0)); err == nil || plan != nil {
		t.Fatal("over-quota request was accepted")
	}
	if plan, err := planResponseImageUploads(responseUploadBody(t, [][]byte{large}, false, imageUploadRequestBudget)); err == nil || plan != nil {
		t.Fatal("oversized non-image content was accepted")
	}
	bad := []byte(`{"input":[{"type":"function_call","arguments":"data:image/png;base64,` + strings.Repeat("A", imageUploadRequestBudget) + `"}]}`)
	if plan, err := planResponseImageUploads(bad); err == nil || plan != nil {
		t.Fatal("arbitrary tool arguments were treated as an image")
	}
}

func TestResponseImageUploadRejectsInvalidImages(t *testing.T) {
	pngBytes := responseUploadPNG(t, 2, 2, 1)
	for _, value := range []string{
		"data:image/png;base64,!!!!", "data:text/plain;base64,YQ==", "data:image/svg+xml;base64,YQ==",
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(pngBytes), "data:image/png,abc",
		"data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(imageAttachmentMaxBytes+4)),
	} {
		if _, _, err := decodeResponseImage(value); err == nil {
			t.Fatal("invalid or oversized image accepted")
		}
	}
}

func TestImageURLTransportPreservesAnimatedWebP(t *testing.T) {
	// Two synthetic 2x2 red/blue frames. This is a complete animation rather
	// than a header-only fixture, and requires no external image tools at test time.
	const encoded = "UklGRsAAAABXRUJQVlA4WAoAAAACAAAAAQAAAQAAQU5JTQYAAAD/////AABBTk1GSAAAAAAAAAAAAAEAAAEAAGQAAAJWUDggMAAAANABAJ0BKgIAAgACADQloAJ0ugH4AAOwAP7wxAv/ILlhdcjX/yA/5Af8gP/48gAAAEFOTUZEAAAAAAAAAAAAAQAAAQAAZAAAAFZQOCAsAAAAlAEAnQEqAgACAAAANCWgAnS6AAOYAP75k2//kB//kB//kB//ID/iF3sgMAA="
	original, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	data, mime, err := decodeResponseImage("data:image/webp;base64," + encoded)
	if err != nil || mime != "image/webp" || !bytes.Equal(data, original) || !responseImageAnimated(data, "webp") {
		t.Fatal("animated WebP was rejected or changed", err)
	}
	if err := validateImageTransportData(original, "image/png"); err == nil {
		t.Fatal("animation accepted with the wrong declared format")
	}
	truncated := original[:len(original)-1]
	if err := validateImageTransportData(truncated, "image/webp"); err == nil {
		t.Fatal("truncated animation accepted")
	}
}

type responseUploadStore struct {
	mu          sync.Mutex
	objects     map[string][]byte
	uploads     [][]byte
	releases    int
	calls       int
	failPut     bool
	keepObjects bool
}

func newResponseUploadStore() *responseUploadStore {
	return &responseUploadStore{objects: make(map[string][]byte)}
}

func (s *responseUploadStore) factory(key, org string) *imageAttachmentClient {
	c := newImageAttachmentClient(key, org)
	c.transport = s
	return c
}

func (s *responseUploadStore) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	status := 200
	var value any = map[string]any{}
	var body []byte
	if r.URL.Host == "api.kilo.ai" {
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			return nil, fmt.Errorf("unexpected RPC auth")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			return nil, err
		}
		if strings.Contains(r.URL.Path, "organizations.") && input["organizationId"] != "team-id" {
			return nil, fmt.Errorf("missing organization")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "getAttachmentUploadUrl"), strings.HasSuffix(r.URL.Path, "getAttachmentDownloadUrl"):
			filename := stringValue(input["filename"])
			if filename == "" {
				filename = stringValue(input["attachmentId"]) + ".png"
			}
			key := "synthetic-user/cloud-agent/" + stringValue(input["messageUuid"]) + "/" + filename
			value = map[string]any{"signedUrl": "https://synthetic.r2.cloudflarestorage.com/bucket/" + key + "?X-Amz-Signature=private-signature&X-Amz-Credential=private-credential", "key": key, "expiresAt": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)}
		case strings.HasSuffix(r.URL.Path, "releasePendingUploads"):
			s.releases++
			if !s.keepObjects {
				for _, key := range input["objectKeys"].([]any) {
					delete(s.objects, "/bucket/"+key.(string))
				}
			}
			value = map[string]bool{"success": true}
		default:
			return nil, fmt.Errorf("unexpected RPC")
		}
		body, _ = json.Marshal(map[string]any{"result": map[string]any{"data": value}})
	} else {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-KiloCode-OrganizationId") != "" {
			return nil, fmt.Errorf("credentials sent to storage")
		}
		switch r.Method {
		case "PUT":
			body, _ = io.ReadAll(r.Body)
			if s.failPut {
				status = 500
			} else {
				s.objects[r.URL.Path] = bytes.Clone(body)
				s.uploads = append(s.uploads, bytes.Clone(body))
				status = 200
			}
			body = nil
		case "GET":
			var ok bool
			body, ok = s.objects[r.URL.Path]
			if !ok {
				status = 404
			} else if len(body) > 0 && r.Header.Get("Range") != "" {
				body = body[:1]
				status = 206
			}
		default:
			return nil, fmt.Errorf("unexpected storage method")
		}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r, ContentLength: int64(len(body))}, nil
}

func TestResponseImageUploadProxyEndToEnd(t *testing.T) {
	var originals [][]byte
	for i := range 4 {
		originals = append(originals, responseUploadPNG(t, 1366, 1000, i))
	}
	body := responseUploadBody(t, originals, true, 0)
	if len(body) < 21_000_000 {
		t.Fatal("fixture does not reproduce the reported oversized image history")
	}
	a := captureTestApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
	store := newResponseUploadStore()
	a.attachmentClientFactory = store.factory
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > imageUploadRequestBudget || r.ContentLength != int64(len(raw)) {
			t.Error("gateway still receives oversized or incorrectly framed request")
		}
		doc, err := decodeObject(raw)
		if err != nil || object(doc["metadata"])["exact"] != json.Number("9007199254740993") {
			t.Error("non-image fields changed")
		}
		input := doc["input"].([]any)
		output := object(input[2])
		if output["call_id"] != "call_original" {
			t.Error("lost tool call")
		}
		parts := output["output"].([]any)
		for i, value := range parts[1:] {
			part := object(value)
			if part["detail"] != "high" {
				t.Error("image detail was changed")
			}
			url := stringValue(part["image_url"])
			if strings.HasPrefix(url, "data:") {
				// A smaller original may remain inline if the entire body fits.
				decoded, _, err := decodeResponseImage(url)
				if err != nil || !bytes.Equal(decoded, originals[i]) {
					t.Error("inline original changed")
				}
				continue
			}
			get, _ := http.NewRequest("GET", url, nil)
			resp, err := store.RoundTrip(get)
			if err != nil || resp.StatusCode != 200 {
				t.Error("provider cannot fetch original")
				continue
			}
			fetched, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !bytes.Equal(fetched, originals[i]) {
				t.Error("provider did not receive exact original image bytes")
			}
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("X-KiloCode-OrganizationId") != "team-id" {
			t.Error("inference billing/auth changed")
		}
		w.Header().Set("Content-Type", "application/json")
		// Reflection by an upstream failure/debug response must be redacted
		// in capture as well as in the outbound request trace.
		json.NewEncoder(w).Encode(map[string]any{"output": []any{}, "debug": parts[1]})
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	response := activityRequest(a, string(body))
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(store.uploads) != 4 || len(store.objects) != 0 || store.releases == 0 || a.imageUploadsActive != 0 || a.imageUploadWarning != "" {
		t.Fatalf("upload/cleanup state: uploads=%d remaining=%d releases=%d active=%d warning=%s", len(store.uploads), len(store.objects), store.releases, a.imageUploadsActive, a.imageUploadWarning)
	}
	trace := a.traces[a.events[0].ID]
	encoded, _ := json.Marshal(trace)
	for _, secret := range []string{"private-signature", "private-credential", "synthetic-user", "upstream-secret", "local-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("trace leaked temporary credentials: %s", secret)
		}
	}
	if !strings.Contains(trace.UpstreamRequest.Body, "[REDACTED]") || !trace.Request.Truncated {
		t.Fatal("expected safe bounded capture")
	}
}

func TestResponseImageUploadsOffAndSmallRequestsDoNotContactStorage(t *testing.T) {
	body := responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 1)}, false, 0)
	for _, enabled := range []bool{false, true} {
		for _, small := range []bool{false, true} {
			if enabled && !small {
				continue
			}
			a := testApp(t)
			a.config.ImageTransport = imageTransportSettings{Mode: "off", Profile: "high"}
			if enabled {
				a.config.ImageTransport.Mode = "upload"
			}
			a.attachmentClientFactory = func(_, _ string) *imageAttachmentClient { t.Fatal("unnecessary storage contact"); return nil }
			raw := body
			if small {
				raw = []byte(` {"model":"test/model","input":"small unchanged"} `)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ := io.ReadAll(r.Body)
				if !bytes.Equal(got, raw) {
					t.Error("pass-through bytes changed")
				}
				io.WriteString(w, `{}`)
			}))
			setUpstream(a, upstream.URL)
			if response := activityRequest(a, string(raw)); response.Code != 200 {
				t.Fatal(response.Code)
			}
			upstream.Close()
			if len(a.events) != 0 || len(a.traces) != 0 {
				t.Fatal("opt-in image uploads enabled request capture")
			}
		}
	}
}

func TestResponseImageCompressionModeNeverContactsStorage(t *testing.T) {
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "compress", Profile: "high"}
	a.attachmentClientFactory = func(_, _ string) *imageAttachmentClient {
		t.Error("compression unexpectedly tried to upload images")
		return newResponseUploadStore().factory("upstream-secret", "team-id")
	}
	original := responseUploadPNG(t, 1366, 1000, 2)
	body := responseUploadBody(t, [][]byte{original}, false, 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > imageUploadRequestBudget || r.ContentLength != int64(len(raw)) {
			t.Error("compression mode did not produce a correctly framed small request")
		}
		doc, _ := decodeObject(raw)
		parts := responseImageParts(doc)
		if len(parts) != 1 || !strings.HasPrefix(stringValue(parts[0]["image_url"]), "data:image/png;base64,") {
			t.Error("lossless compression unexpectedly uploaded or changed the image format")
		}
		io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	if response := activityRequest(a, string(body)); response.Code != 200 {
		t.Fatalf("compression mode returned %d", response.Code)
	}
	if a.imageUploadsActive != 0 || a.imageUploadWarning != "" || len(a.events) != 0 || len(a.traces) != 0 {
		t.Fatal("local compression leaked an upload slot, warning or request capture")
	}
}

func TestResponseImageUploadFailuresCleanUpAndDoNotSendInference(t *testing.T) {
	body := responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 1)}, false, 0)
	for _, failCleanup := range []bool{false, true} {
		a := testApp(t)
		a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
		store := newResponseUploadStore()
		store.failPut = !failCleanup
		store.keepObjects = failCleanup
		a.attachmentClientFactory = store.factory
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !failCleanup {
				t.Error("inference sent after upload failed")
			}
			io.Copy(io.Discard, r.Body)
			io.WriteString(w, `{}`)
		}))
		setUpstream(a, upstream.URL)
		response := activityRequest(a, string(body))
		upstream.Close()
		if failCleanup {
			if response.Code != 200 || a.imageUploadWarning == "" || store.releases != 3 {
				t.Fatal("unconfirmed cleanup was hidden or altered inference result")
			}
		} else if response.Code != 502 || store.releases == 0 || len(store.objects) != 0 {
			t.Fatal("failed upload did not roll back")
		}
		if a.imageUploadsActive != 0 {
			t.Fatal("concurrency slot leaked")
		}
	}
}

func TestResponseImageUploadCleanupSurvivesCancellationAndSettingsChange(t *testing.T) {
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
	store := newResponseUploadStore()
	a.attachmentClientFactory = store.factory
	ctx, cancel := context.WithCancel(context.Background())
	body := responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 1)}, false, 0)
	r := httptest.NewRequest("POST", "http://localhost/v1/responses", bytes.NewReader(body)).WithContext(ctx)
	prepared, cleanup, err := a.prepareResponseImageUploads(r, "upstream-secret", "team-id")
	if err != nil || cleanup == nil {
		t.Fatal(err)
	}
	if len(store.objects) == 0 {
		t.Fatal("missing temporary upload")
	}
	a.mu.Lock()
	a.config.ImageTransport.Mode = "off"
	a.mu.Unlock()
	cancel()
	if prepared.Context().Err() == nil {
		t.Fatal("cancellation not propagated to inference")
	}
	cleanup()
	cleanup() // Idempotent finalization cannot underflow the semaphore.
	if len(store.objects) != 0 || a.imageUploadsActive != 0 || a.imageUploadWarning != "" {
		t.Fatal("cancelled request did not clean up independently")
	}
}

func TestActivityLateImageSecretsRedactAcrossTruncation(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost/v1/responses", nil)
	capture := newTraceCapture(r, []string{"key"})
	secret := "https://synthetic.r2.cloudflarestorage.com/bucket/user/image.png?X-Amz-Signature=" + strings.Repeat("s", 500) + "&credential=secret"
	capture.addSecrets(secret)
	escaped, _ := json.Marshal(secret)
	payload := strings.Repeat("p", traceBodyLimit-10) + string(escaped)
	capture.upRequest.write([]byte(payload))
	trace := capture.finish("1", nil)
	if strings.Contains(trace.UpstreamRequest.Body, "https:") || strings.Contains(trace.UpstreamRequest.Body, "Signature") || !strings.Contains(trace.UpstreamRequest.Body, "[REDACTED") {
		t.Fatal("signed URL leaked at the trace truncation boundary")
	}
}

func TestResponseImageUploadStreamingLifetime(t *testing.T) {
	body := responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 1)}, true, 0)
	for _, cancelStream := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelStream), func(t *testing.T) {
			a := captureTestApp(t)
			a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
			store := newResponseUploadStore()
			a.attachmentClientFactory = store.factory
			finish := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"private-sign\"}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-finish:
					io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ature\"}\n\ndata: [DONE]\n\n")
				case <-r.Context().Done():
				}
			}))
			defer upstream.Close()
			setUpstream(a, upstream.URL)
			handled := make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handled)
				a.inferenceHandler("upstream-secret", "team-id", "local-secret", r.Host).ServeHTTP(w, r)
			}))
			defer proxy.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", proxy.URL+"/v1/responses", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer local-secret")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				close(finish)
				t.Fatal(err)
			}
			store.mu.Lock()
			alive, releases := len(store.objects), store.releases
			store.mu.Unlock()
			if alive != 1 || releases != 0 {
				close(finish)
				t.Fatal("originals deleted before stream finished")
			}
			if cancelStream {
				cancel()
			} else {
				close(finish)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			select {
			case <-handled:
			case <-time.After(5 * time.Second):
				t.Fatal("stream cleanup did not finish")
			}
			if len(store.objects) != 0 || a.imageUploadsActive != 0 || a.imageUploadWarning != "" {
				t.Fatal("stream cleanup lost objects or its concurrency slot")
			}
			trace := a.traces[a.events[0].ID]
			for _, part := range []tracePart{trace.Response, trace.UpstreamResponse} {
				if !strings.Contains(part.Body, "omitted") || strings.Contains(part.Body, "private-sign") || part.Bytes == 0 {
					t.Fatal("stream capture retained potentially reconstructable URL fragments")
				}
			}
			if !cancelStream && a.events[0].Status != 200 {
				t.Fatal("normal cleanup marked a successful stream as cancelled")
			}
		})
	}
}

func TestResponseImageUploadQuitDrainsAndRejectsNewLeases(t *testing.T) {
	a := testApp(t)
	a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
	store := newResponseUploadStore()
	a.attachmentClientFactory = store.factory
	body := responseUploadBody(t, [][]byte{responseUploadPNG(t, 1366, 1000, 1)}, false, 0)
	prepare := func() (func(), error) {
		r := httptest.NewRequest("POST", "http://localhost/v1/responses", bytes.NewReader(body))
		_, cleanup, err := a.prepareResponseImageUploads(r, "upstream-secret", "team-id")
		return cleanup, err
	}
	first, err := prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	if cleanup, err := prepare(); err == nil || cleanup != nil || a.imageUploadsActive != 2 {
		t.Fatal("third concurrent upload was not bounded")
	}
	a.requestQuit()
	drained := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		a.drainImageUploads(ctx)
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("quit did not wait for pending images")
	default:
	}
	first()
	second()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("quit did not finish after deletion")
	}
	if cleanup, err := prepare(); err == nil || cleanup != nil {
		t.Fatal("new image upload registered after quit")
	}
	if len(store.objects) != 0 {
		t.Fatal("quit retained temporary images")
	}
}
