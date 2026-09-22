package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type imageLiveTransport func(*http.Request) (*http.Response, error)

func (f imageLiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Explicit opt-in only: this makes one paid inference with synthetic images.
// It reads the chosen Kilo profile/keyring but never changes it or starts/stops
// the user's app. All images, signed URLs and test bookkeeping stay temporary.
func TestImageUploadsLiveGateway(t *testing.T) {
	if os.Getenv("KILO_IMAGE_UPLOAD_LIVE_TEST") != "1" {
		t.Skip("set KILO_IMAGE_UPLOAD_LIVE_TEST=1 and KILO_IMAGE_UPLOAD_CONFIG_DIR for the synthetic live probe")
	}
	dir := os.Getenv("KILO_IMAGE_UPLOAD_CONFIG_DIR")
	if dir == "" {
		t.Fatal("an explicit existing profile directory is required")
	}
	cfg, err := readSettings(dir)
	if err != nil || !cfg.Remember || cfg.OrgID == "" {
		t.Fatal("the chosen profile needs saved Kilo credentials and a selected organization")
	}
	key, err := (systemVault{}).Get(cfg.VaultID)
	if err != nil || key == "" {
		t.Fatal("could not read the chosen profile credential")
	}
	a, err := newApp(t.TempDir(), &fakeVault{values: make(map[string]string)})
	if err != nil {
		t.Fatal("could not create isolated probe")
	}
	a.config.ImageTransport = imageTransportSettings{Mode: "upload", Profile: "high"}
	var deleted atomic.Int32
	a.attachmentClientFactory = func(key, org string) *imageAttachmentClient {
		client := newImageAttachmentClient(key, org)
		base := client.transport
		client.transport = imageLiveTransport(func(r *http.Request) (*http.Response, error) {
			response, err := base.RoundTrip(r)
			if err == nil && r.Method == "GET" && r.Header.Get("Range") == "bytes=0-0" && response.StatusCode == 404 {
				deleted.Add(1)
			}
			return response, err
		})
		return client
	}
	colors := []color.NRGBA{{220, 0, 0, 255}, {0, 180, 0, 255}, {0, 0, 220, 255}, {240, 220, 0, 255}}
	var originals [][]byte
	for i := range 4 {
		img := image.NewNRGBA(image.Rect(0, 0, 1366, 1000))
		for y := range 1000 {
			for x := range 1366 {
				quadrant := 0
				if x >= 683 {
					quadrant++
				}
				if y >= 500 {
					quadrant += 2
				}
				img.SetNRGBA(x, y, colors[(quadrant+i)%4])
			}
		}
		var encoded bytes.Buffer
		encoder := png.Encoder{CompressionLevel: png.NoCompression}
		if err := encoder.Encode(&encoded, img); err != nil {
			t.Fatal("could not create synthetic fixture")
		}
		originals = append(originals, encoded.Bytes())
	}
	body := responseUploadBody(t, originals, true, 0)
	doc, _ := decodeObject(body)
	doc["model"] = "openai/gpt-5.6-luna"
	doc["max_output_tokens"] = 256
	delete(doc, "reasoning")
	// The unit fixture deliberately carries a >53-bit JSON number to test
	// preservation. OpenAI's real metadata contract accepts strings only.
	delete(doc, "metadata")
	input := doc["input"].([]any)
	object(input[0])["content"] = "Describe each image's quadrant colors, in this order: top-left, top-right, bottom-left, bottom-right. Be concise."
	body, _ = json.Marshal(doc)
	base := a.transport
	var outgoing int
	verified := 0
	a.transport = imageLiveTransport(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		outgoing = len(raw)
		if outgoing > imageUploadRequestBudget {
			t.Error("real gateway would receive an oversized request")
			return nil, errImageAttachment
		}
		doc, err := decodeObject(raw)
		if err != nil {
			return nil, errImageAttachment
		}
		parts := object(doc["input"].([]any)[2])["output"].([]any)
		client := &http.Client{Transport: base, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for i, value := range parts[1:] {
			get, err := http.NewRequestWithContext(r.Context(), "GET", stringValue(object(value)["image_url"]), nil)
			if err != nil {
				return nil, errImageAttachment
			}
			response, err := client.Do(get)
			if err != nil {
				return nil, errImageAttachment
			}
			stored, readErr := io.ReadAll(io.LimitReader(response.Body, imageAttachmentMaxBytes+1))
			response.Body.Close()
			if response.StatusCode != 200 || readErr != nil || !bytes.Equal(stored, originals[i]) {
				t.Error("stored original differs from source")
				return nil, errImageAttachment
			}
			verified++
		}
		return base.RoundTrip(r)
	})
	r := httptest.NewRequest("POST", "http://127.0.0.1:18877/v1/responses", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer synthetic-local-probe")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.inferenceHandler(key, cfg.OrgID, "synthetic-local-probe", "127.0.0.1:18877").ServeHTTP(w, r)
	if a.imageUploadWarning != "" || deleted.Load() != 4 {
		t.Fatal("deletion of all four synthetic objects was not confirmed")
	}
	if w.Code != 200 || verified != 4 {
		t.Fatalf("live synthetic inference failed: HTTP %d, originals verified %d", w.Code, verified)
	}
	result, err := decodeObject(w.Body.Bytes())
	if err != nil {
		t.Fatal("invalid inference response")
	}
	var text strings.Builder
	outputs, _ := result["output"].([]any)
	for _, output := range outputs {
		parts, _ := object(output)["content"].([]any)
		for _, part := range parts {
			if object(part)["type"] == "output_text" {
				text.WriteString(strings.ToLower(stringValue(object(part)["text"])))
			}
		}
	}
	for _, color := range []string{"red", "green", "blue", "yellow"} {
		if !strings.Contains(text.String(), color) {
			t.Fatal("model did not identify the synthetic quadrant colors")
		}
	}
	t.Logf("Live synthetic upload passed: %d original request bytes -> %d outbound bytes; 4 exact original downloads and 4 confirmed deletions", len(body), outgoing)
}
