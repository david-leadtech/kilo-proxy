package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func compressionTestPNG(t *testing.T, width, height int, noise, alpha bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	state := uint32(17)
	for y := range height {
		for x := range width {
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			pixel := color.NRGBA{R: byte(x), G: byte(y), B: 93, A: 255}
			if noise {
				pixel.R, pixel.G, pixel.B = byte(state), byte(state>>8), byte(state>>16)
			}
			if alpha {
				pixel.A = 40 + byte(state>>24)%180
			}
			img.SetNRGBA(x, y, pixel)
		}
	}
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func compressionTestRequest(body []byte) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
}

func compressionTestImage(t *testing.T, request *http.Request) ([]byte, string, map[string]any) {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > imageUploadRequestBudget {
		t.Fatalf("adapted request exceeds budget: %d", len(body))
	}
	if request.ContentLength != int64(len(body)) || request.GetBody != nil || len(request.TransferEncoding) != 0 || request.Header.Get("Content-Length") != "" || request.Header.Get("Transfer-Encoding") != "" {
		t.Fatal("request retained stale transport metadata")
	}
	doc, err := decodeObject(body)
	if err != nil {
		t.Fatal(err)
	}
	parts := responseImageParts(doc)
	if len(parts) == 0 {
		t.Fatal("image disappeared")
	}
	header, encoded, _ := strings.Cut(stringValue(parts[0]["image_url"]), ",")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw, header, doc
}

func TestImageCompressionLosslessFirstPreservesAlphaPixelsAndOriginalFiles(t *testing.T) {
	original := compressionTestPNG(t, 3200, 400, false, true)
	path := filepath.Join(t.TempDir(), "original.png")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	body := responseUploadBody(t, [][]byte{original}, true, 0)
	if len(body) <= imageUploadRequestBudget {
		t.Fatal("fixture must require compression")
	}
	request := compressionTestRequest(body)
	request.Header.Set("Content-Length", "99999999")
	request.Header.Set("Transfer-Encoding", "chunked")
	request.TransferEncoding = []string{"chunked"}
	prepared, err := prepareCompressedResponseImages(request, "small")
	if err != nil {
		t.Fatal(err)
	}
	compressed, header, doc := compressionTestImage(t, prepared)
	if header != "data:image/png;base64" || len(compressed) >= len(original) {
		t.Fatal("lossless optimization was not used")
	}
	before, _ := png.Decode(bytes.NewReader(original))
	after, err := png.Decode(bytes.NewReader(compressed))
	if err != nil || before.Bounds() != after.Bounds() || after.Bounds().Dx() != 3200 {
		t.Fatal("lossless pass resized an image to the small profile", err)
	}
	for y := range before.Bounds().Dy() {
		for x := range before.Bounds().Dx() {
			if color.NRGBA64Model.Convert(before.At(x, y)) != color.NRGBA64Model.Convert(after.At(x, y)) {
				t.Fatalf("lossless pixel changed at %d,%d", x, y)
			}
		}
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, original) {
		t.Fatal("original file changed")
	}
	input := doc["input"].([]any)
	if object(input[2])["call_id"] != "call_original" || object(doc["metadata"])["exact"] != json.Number("9007199254740993") || object(doc["reasoning"])["effort"] != "high" {
		t.Fatal("non-image request fields changed")
	}
	restored, _ := io.ReadAll(request.Body)
	if !bytes.Equal(restored, body) || request.Header.Get("Content-Length") != "99999999" {
		t.Fatal("input request changed instead of creating an outgoing clone")
	}
}

func jpegFirstQuantizer(data []byte) byte {
	for offset := 2; offset+4 < len(data); {
		if data[offset] != 0xff {
			return 0
		}
		size := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if size < 2 || offset+size+2 > len(data) {
			return 0
		}
		if data[offset+1] == 0xdb && size >= 67 {
			return data[offset+5]
		}
		offset += size + 2
	}
	return 0
}

func TestImageCompressionSelectedProfileDimensionsAndQuality(t *testing.T) {
	original := compressionTestPNG(t, 3200, 400, true, false)
	body := responseUploadBody(t, [][]byte{original}, false, 0)
	for _, tt := range []struct {
		profile string
		width   int
		height  int
		quant   byte
	}{{"high", 3072, 384, 3}, {"balanced", 2048, 256, 5}, {"small", 1280, 160, 8}} {
		t.Run(tt.profile, func(t *testing.T) {
			prepared, err := prepareCompressedResponseImages(compressionTestRequest(body), tt.profile)
			if err != nil {
				t.Fatal(err)
			}
			raw, header, _ := compressionTestImage(t, prepared)
			cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
			if err != nil || header != "data:image/jpeg;base64" || format != "jpeg" || cfg.Width != tt.width || cfg.Height != tt.height {
				t.Fatalf("profile dimensions/format: %s %dx%d: %v", format, cfg.Width, cfg.Height, err)
			}
			if jpegFirstQuantizer(raw) != tt.quant {
				t.Fatal("selected JPEG quality was not used")
			}
		})
	}
}

func TestImageCompressionProfilePreservesTransparency(t *testing.T) {
	original := compressionTestPNG(t, 3600, 300, true, true)
	prepared, err := prepareCompressedResponseImages(compressionTestRequest(responseUploadBody(t, [][]byte{original}, false, 0)), "small")
	if err != nil {
		t.Fatal(err)
	}
	raw, header, _ := compressionTestImage(t, prepared)
	decoded, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "png" || header != "data:image/png;base64" || decoded.Bounds().Dx() != 1280 || decoded.Bounds().Dy() != 106 {
		t.Fatal("transparent image was flattened or resized incorrectly", err)
	}
	_, _, _, alpha := decoded.At(30, 30).RGBA()
	if alpha == 65535 || alpha == 0 {
		t.Fatal("partial transparency was not preserved")
	}
}

func TestImageCompressionNoSilentFallbackOrPartialMutation(t *testing.T) {
	original := compressionTestPNG(t, 3200, 400, true, false)
	body := responseUploadBody(t, [][]byte{original}, false, imageUploadRequestBudget-450_000)
	request := compressionTestRequest(body)
	prepared, err := prepareCompressedResponseImages(request, "high")
	var problem *imageUploadRequestError
	if !errors.As(err, &problem) || problem.status != 413 || prepared != request {
		t.Fatalf("high quality should refuse this request: %v", err)
	}
	unchanged, _ := io.ReadAll(request.Body)
	if !bytes.Equal(unchanged, body) {
		t.Fatal("refused request was partially changed")
	}
	// A smaller profile can fit, but must only be used when explicitly chosen.
	if _, err := prepareCompressedResponseImages(compressionTestRequest(body), "small"); err != nil {
		t.Fatal("fixture should fit only with the explicitly selected smaller profile", err)
	}
}

func TestImageCompressionBypassesSmallAndUnsupportedRequestsExactly(t *testing.T) {
	for _, tt := range []struct {
		name, body, path, encoding string
	}{
		{"small", " {\n \"input\":\"hello\" } ", "/v1/responses", ""},
		{"invalid-json", strings.Repeat("x", imageUploadRequestBudget+1), "/v1/responses", ""},
		{"chat", strings.Repeat("x", imageUploadRequestBudget+1), "/v1/chat/completions", ""},
		{"encoded", strings.Repeat("x", imageUploadRequestBudget+1), "/v1/responses", "gzip"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			r.Header.Set("Content-Encoding", tt.encoding)
			prepared, err := prepareCompressedResponseImages(r, "high")
			if err != nil || prepared != r {
				t.Fatal("unsupported/small request was adapted", err)
			}
			got, _ := io.ReadAll(prepared.Body)
			if string(got) != tt.body {
				t.Fatal("body bytes changed")
			}
		})
	}
}

func TestImageCompressionOnlyVisitsTypedInlineImages(t *testing.T) {
	original := compressionTestPNG(t, 3200, 400, false, false)
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(original)
	ignored := []any{
		map[string]any{"type": "function_call", "call_id": "unchanged", "arguments": `{"path":"/not/read/or/opened.png","image_url":"data:image/png;base64,invalid"}`},
		map[string]any{"type": "function_call_output", "call_id": "unchanged", "output": `{"type":"input_image","image_url":"data:image/png;base64,invalid"}`},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "data:image/png;base64,invalid /not/read/or/opened.png"},
			map[string]any{"type": "input_image", "image_url": "https://invalid.example/never-fetched.png"},
			map[string]any{"type": "input_image", "image_url": "file:///not/read/or/opened.png"},
			map[string]any{"type": "output_image", "image_url": "data:image/png;base64,invalid"},
		}},
	}
	doc := map[string]any{"input": append(ignored, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": url, "detail": "original"}}})}
	body, _ := json.Marshal(doc)
	prepared, err := prepareCompressedResponseImages(compressionTestRequest(body), "high")
	if err != nil {
		t.Fatal(err)
	}
	_, _, result := compressionTestImage(t, prepared)
	if !reflect.DeepEqual(result["input"].([]any)[:len(ignored)], ignored) {
		t.Fatal("tool arguments, text, remote URL or filesystem path changed")
	}
	if responseImageParts(result)[0]["detail"] != "original" {
		t.Fatal("image detail changed")
	}
}

func compressionHeaderPNG(width, height uint32) []byte {
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	data := make([]byte, 13)
	binary.BigEndian.PutUint32(data, width)
	binary.BigEndian.PutUint32(data[4:], height)
	data[8], data[9] = 8, 6
	_ = binary.Write(&out, binary.BigEndian, uint32(len(data)))
	out.WriteString("IHDR")
	out.Write(data)
	_ = binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), data...)))
	return out.Bytes()
}

func TestImageCompressionRejectsInvalidAndOversizedDecodes(t *testing.T) {
	pngBytes := compressionTestPNG(t, 2, 2, false, false)
	for _, tt := range []struct {
		value  string
		status int
	}{
		{"data:image/png;base64,!!!!", 400},
		{"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(pngBytes), 400},
		{"data:image/svg+xml;base64,PHN2Zz4=", 400},
		{"data:image/png;utf8,invalid", 400},
		{"data:image/png;base64," + base64.StdEncoding.EncodeToString(compressionHeaderPNG(10000, 4001)), 413},
		{"data:image/png;base64," + base64.StdEncoding.EncodeToString(compressionHeaderPNG(100000, 1)), 413},
	} {
		doc := map[string]any{"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": tt.value}, map[string]any{"type": "input_text", "text": strings.Repeat("p", imageUploadRequestBudget)}}}}}
		body, _ := json.Marshal(doc)
		_, err := prepareCompressedResponseImages(compressionTestRequest(body), "balanced")
		var problem *imageUploadRequestError
		if !errors.As(err, &problem) || problem.status != tt.status {
			t.Fatalf("expected safe HTTP %d, got %v", tt.status, err)
		}
	}
	parts := []any{}
	for _, size := range []uint32{3998, 3999, 4000} {
		parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(compressionHeaderPNG(10000, size))})
	}
	_, err := collectCompressionImages(map[string]any{"input": []any{map[string]any{"role": "user", "content": parts}}})
	var problem *imageUploadRequestError
	if !errors.As(err, &problem) || problem.status != 413 {
		t.Fatal("aggregate decoded-pixel limit was not enforced", err)
	}
}

func TestImageCompressionCancellationAndBodyLimit(t *testing.T) {
	body := responseUploadBody(t, [][]byte{compressionTestPNG(t, 3200, 400, false, false)}, false, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := prepareCompressedResponseImages(compressionTestRequest(body).WithContext(ctx), "high")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled work was not stopped", err)
	}
	_, err = prepareCompressedResponseImages(compressionTestRequest(bytes.Repeat([]byte{'x'}, bridgeLimit+1)), "high")
	var limit *http.MaxBytesError
	if !errors.As(err, &limit) {
		t.Fatal("request body limit was not enforced", err)
	}
}

func compressionTestPNGChunk(kind string, data []byte) []byte {
	var chunk bytes.Buffer
	_ = binary.Write(&chunk, binary.BigEndian, uint32(len(data)))
	chunk.WriteString(kind)
	chunk.Write(data)
	_ = binary.Write(&chunk, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), data...)))
	return chunk.Bytes()
}

func TestImageCompressionPreservesOrientationAndPNGColorMetadata(t *testing.T) {
	// TIFF orientation 6 (90 degrees clockwise), in its original byte order.
	exif := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	base := compressionTestPNG(t, 3200, 400, false, false)
	metadata := append(compressionTestPNGChunk("gAMA", []byte{0, 0, 177, 143}), compressionTestPNGChunk("eXIf", exif)...)
	original := append(append(append([]byte{}, base[:33]...), metadata...), base[33:]...)
	prepared, err := prepareCompressedResponseImages(compressionTestRequest(responseUploadBody(t, [][]byte{original}, false, 0)), "small")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := compressionTestImage(t, prepared)
	if !bytes.Contains(raw, metadata) {
		t.Fatal("lossless optimization removed color management or orientation")
	}
	// JPEG keeps its APP1 data when resized and re-encoded. Retaining the same
	// orientation is essential because the decoder does not rotate its pixels.
	decoded, _ := png.Decode(bytes.NewReader(compressionTestPNG(t, 3200, 400, true, false)))
	var jpegBuffer bytes.Buffer
	if err := jpeg.Encode(&jpegBuffer, decoded, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	app1 := []byte{0xff, 0xe1, 0, byte(len(exif) + 8)}
	app1 = append(app1, []byte("Exif\x00\x00")...)
	app1 = append(app1, exif...)
	jpegBytes := jpegBuffer.Bytes()
	jpegBytes = append(append(append([]byte{}, jpegBytes[:2]...), app1...), jpegBytes[2:]...)
	body := responseUploadBody(t, [][]byte{jpegBytes}, false, imageUploadRequestBudget-300_000)
	body = bytes.ReplaceAll(body, []byte("data:image/png;"), []byte("data:image/jpeg;"))
	prepared, err = prepareCompressedResponseImages(compressionTestRequest(body), "small")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ = compressionTestImage(t, prepared)
	if !bytes.Equal(compressionJPEGMetadata(raw), app1) {
		t.Fatal("JPEG orientation metadata was lost")
	}
}

func TestImageCompressionDoesNotFlattenAnimationOrReorientPNGAndWebP(t *testing.T) {
	base := compressionTestPNG(t, 3200, 400, true, false)
	for _, kind := range []string{"acTL", "eXIf"} {
		chunk := compressionTestPNGChunk(kind, []byte{0, 0, 0, 2, 0, 0, 0, 0})
		original := append(append(append([]byte{}, base[:33]...), chunk...), base[33:]...)
		request := compressionTestRequest(responseUploadBody(t, [][]byte{original}, false, 0))
		_, err := prepareCompressedResponseImages(request, "small")
		var problem *imageUploadRequestError
		if !errors.As(err, &problem) || problem.status != 413 {
			t.Fatalf("%s was silently flattened or reoriented: %v", kind, err)
		}
	}
	// A WebP EXIF chunk must also disable pixel conversion until its metadata
	// can be moved to the target container without changing orientation.
	webp := append([]byte("RIFF\x14\x00\x00\x00WEBPEXIF\x08\x00\x00\x00"), []byte("example!")...)
	profile, _ := compressionProfile("small")
	got, format, err := encodeResponseImage(context.Background(), &compressibleResponseImage{raw: webp, format: "webp"}, profile)
	if err != nil || format != "webp" || !bytes.Equal(got, webp) {
		t.Fatal("WebP orientation-bearing data was changed", err)
	}
	// VP8X stores width/height minus one as unsigned 24-bit little-endian
	// values. The animation fallback must apply the same pixel safety limits.
	animated := []byte{'R', 'I', 'F', 'F', 36, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'X', 10, 0, 0, 0, 2, 0, 0, 0,
		0xff, 0x03, 0, 0xff, 0x01, 0, 'A', 'N', 'I', 'M', 6, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	canvas, valid := animatedWebPCanvas(animated)
	if !valid || canvas.Width != 1024 || canvas.Height != 512 || !responseImageAnimated(animated, "webp") {
		t.Fatal("animated WebP header dimensions were not safely decoded")
	}
	animated[24], animated[25], animated[26] = 0xff, 0xff, 0xff
	parts := []any{map[string]any{"type": "input_image", "image_url": "data:image/webp;base64," + base64.StdEncoding.EncodeToString(animated)}}
	_, err = collectCompressionImages(map[string]any{"input": []any{map[string]any{"role": "user", "content": parts}}})
	var problem *imageUploadRequestError
	if !errors.As(err, &problem) || problem.status != 413 {
		t.Fatal("animated WebP bypassed the image dimension limits", err)
	}
}
