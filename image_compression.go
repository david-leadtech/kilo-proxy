package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"

	"golang.org/x/image/draw"
)

const imageCompressionPixelLimit = 40_000_000
const imageCompressionRequestPixelLimit = 80_000_000
const imageCompressionSideLimit = 32_768

type imageCompressionProfile struct {
	side    int
	quality int
}

func compressionProfile(name string) (imageCompressionProfile, bool) {
	switch name {
	case "high":
		return imageCompressionProfile{side: 3072, quality: 92}, true
	case "balanced":
		return imageCompressionProfile{side: 2048, quality: 85}, true
	case "small":
		return imageCompressionProfile{side: 1280, quality: 75}, true
	default:
		return imageCompressionProfile{}, false
	}
}

type compressibleResponseImage struct {
	raw      []byte
	format   string
	config   image.Config
	parts    []inferenceImagePart
	animated bool
}

// This runs only in the explicitly selected compression mode. The caller limits
// concurrent large-image work; decoding is sequential and bounded per image and
// per request. Local files and remote URLs are never opened by this adapter.
func prepareCompressedResponseImages(r *http.Request, profile string) (*http.Request, error) {
	if r.Method != http.MethodPost || !imageTransportPath(r.URL.Path) || r.Header.Get("Content-Encoding") != "" || r.Body == nil {
		return r, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, bridgeLimit+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return r, err
	}
	if len(data) > bridgeLimit {
		return r, &http.MaxBytesError{Limit: bridgeLimit}
	}
	if len(data) <= imageUploadRequestBudget {
		return r, nil
	}
	selected, ok := compressionProfile(profile)
	if !ok {
		return r, imageUploadProblem(400, "Choose a valid image compression profile in Settings.")
	}
	doc, err := decodeObject(data)
	if err != nil {
		return r, nil // Preserve existing gateway validation for invalid JSON.
	}
	images, err := collectInferenceCompressionImages(doc, r.URL.Path)
	if err != nil {
		return r, err
	}
	// First re-encode static PNGs without changing their pixels or dimensions.
	// This often removes the entire excess without applying the lossy profile.
	for _, candidate := range images {
		if err := r.Context().Err(); err != nil {
			return r, err
		}
		if candidate.format != "png" || candidate.animated {
			continue
		}
		raw, err := losslessResponsePNG(r.Context(), candidate)
		if err != nil {
			return r, err
		}
		if len(raw) < len(candidate.raw) {
			candidate.raw = raw
			setCompressedImage(candidate.parts, raw, "png")
		}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return r, err
	}
	if len(encoded) <= imageUploadRequestBudget {
		return compressedResponseRequest(r, encoded), nil
	}
	for _, candidate := range images {
		if err := r.Context().Err(); err != nil {
			return r, err
		}
		// GIF and animated PNG/WebP stay intact. Decoding the first frame alone
		// would silently remove information from the conversation.
		if candidate.format == "gif" || candidate.animated {
			continue
		}
		raw, format, err := encodeResponseImage(r.Context(), candidate, selected)
		if err != nil {
			return r, err
		}
		if len(raw) < len(candidate.raw) {
			setCompressedImage(candidate.parts, raw, format)
		}
	}
	encoded, err = json.Marshal(doc)
	if err != nil {
		return r, err
	}
	if len(encoded) > imageUploadRequestBudget {
		return r, imageUploadProblem(413, "This request still exceeds Kilo's 4.5 MB limit with the selected image compression profile. Choose another profile, enable experimental temporary uploads, or reduce attachments. No lower-quality profile was applied, no files were changed, and no inference was sent.")
	}
	return compressedResponseRequest(r, encoded), nil
}

func compressedResponseRequest(r *http.Request, data []byte) *http.Request {
	prepared := r.Clone(r.Context())
	prepared.Body = io.NopCloser(bytes.NewReader(data))
	prepared.ContentLength = int64(len(data))
	prepared.GetBody = nil
	prepared.TransferEncoding = nil
	prepared.Header.Del("Content-Length")
	prepared.Header.Del("Transfer-Encoding")
	return prepared
}

func collectCompressionImages(doc map[string]any) ([]*compressibleResponseImage, error) {
	return collectInferenceCompressionImages(doc, "/v1/responses")
}

func collectInferenceCompressionImages(doc map[string]any, path string) ([]*compressibleResponseImage, error) {
	groups := make(map[string]*compressibleResponseImage)
	var images []*compressibleResponseImage
	pixels := 0
	for _, part := range inferenceImageParts(doc, path) {
		value := part.inline()
		if existing := groups[value]; existing != nil {
			existing.parts = append(existing.parts, part)
			continue
		}
		header, encoded, ok := strings.Cut(value, ",")
		if !ok || !strings.HasSuffix(header, ";base64") {
			return nil, imageCompressionInvalid()
		}
		mime := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64"))
		format := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif", "image/webp": "webp"}[mime]
		if format == "" || base64.StdEncoding.DecodedLen(len(encoded)) > bridgeLimit {
			return nil, imageCompressionInvalid()
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) == 0 {
			return nil, imageCompressionInvalid()
		}
		animated := responseImageAnimated(raw, format)
		cfg, actual, err := image.DecodeConfig(bytes.NewReader(raw))
		// x/image/webp does not decode animation. Its extended header still
		// provides a bounded canvas size, so animated data can pass unchanged.
		if format == "webp" && animated {
			canvas, valid := animatedWebPCanvas(raw)
			if !valid {
				return nil, imageCompressionInvalid()
			}
			cfg, actual, err = canvas, "webp", nil
		}
		if err != nil || actual != format || cfg.Width < 1 || cfg.Height < 1 {
			return nil, imageCompressionInvalid()
		}
		if cfg.Width > imageCompressionSideLimit || cfg.Height > imageCompressionSideLimit || cfg.Width > imageCompressionPixelLimit/cfg.Height {
			return nil, imageUploadProblem(413, "An image is too large to safely decompress locally (maximum 40 million pixels and 32,768 pixels per side). Use temporary image uploads or reduce attachments. No image was changed.")
		}
		pixels += cfg.Width * cfg.Height
		if pixels > imageCompressionRequestPixelLimit {
			return nil, imageUploadProblem(413, "These images exceed the local compression limit of 80 million unique pixels per request. Use temporary image uploads or reduce attachments. No image was changed.")
		}
		candidate := &compressibleResponseImage{raw: raw, format: format, config: cfg, parts: []inferenceImagePart{part}, animated: animated}
		groups[value] = candidate
		images = append(images, candidate)
	}
	return images, nil
}

func imageCompressionInvalid() error {
	return imageUploadProblem(400, "An inline image could not be safely decoded as its declared PNG, JPEG, GIF or WebP format. No image was changed or sent.")
}

// Check container chunk boundaries, never arbitrary compressed pixel bytes.
func responseImageAnimated(raw []byte, format string) bool {
	switch format {
	case "png":
		for offset := 8; offset+12 <= len(raw); {
			size := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
			if size+12 > uint64(len(raw)-offset) {
				return false
			}
			if string(raw[offset+4:offset+8]) == "acTL" {
				return true
			}
			offset += int(size) + 12
		}
	case "webp":
		for offset := 12; offset+8 <= len(raw); {
			size := uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
			if size+8 > uint64(len(raw)-offset) {
				return false
			}
			if string(raw[offset:offset+4]) == "ANIM" || string(raw[offset:offset+4]) == "ANMF" {
				return true
			}
			offset += int(size+(size&1)) + 8
		}
	}
	return false
}

func animatedWebPCanvas(raw []byte) (image.Config, bool) {
	if len(raw) < 30 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(raw[4:8]))+8 != uint64(len(raw)) {
		return image.Config{}, false
	}
	// The extended format requires VP8X to be its first chunk.
	if string(raw[12:16]) != "VP8X" || binary.LittleEndian.Uint32(raw[16:20]) != 10 || raw[20]&2 == 0 {
		return image.Config{}, false
	}
	width := 1 + int(raw[24]) + int(raw[25])<<8 + int(raw[26])<<16
	height := 1 + int(raw[27]) + int(raw[28])<<8 + int(raw[29])<<16
	return image.Config{Width: width, Height: height}, true
}

var errCompressionOutputLimit = errors.New("image encoding would not reduce its size")

type compressionImageWriter struct {
	bytes.Buffer
	ctx   context.Context
	limit int
}

func (w *compressionImageWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(data) > w.limit-w.Len() {
		return 0, errCompressionOutputLimit
	}
	return w.Buffer.Write(data)
}

func losslessResponsePNG(ctx context.Context, candidate *compressibleResponseImage) ([]byte, error) {
	decoded, format, err := image.Decode(bytes.NewReader(candidate.raw))
	if err != nil || format != candidate.format {
		return nil, imageCompressionInvalid()
	}
	writer := &compressionImageWriter{ctx: ctx, limit: len(candidate.raw) - 1}
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	err = encoder.Encode(writer, decoded)
	if errors.Is(err, errCompressionOutputLimit) {
		return candidate.raw, nil
	}
	if err != nil {
		return nil, imageCompressionEncodeError(ctx)
	}
	// Go's PNG encoder omits color management and EXIF chunks. Retain them
	// when no pixels changed, otherwise an apparently lossless re-encode could
	// change color rendering or lose the original orientation.
	encoded := preserveLosslessPNGMetadata(candidate.raw, writer.Bytes())
	if len(encoded) >= len(candidate.raw) {
		return candidate.raw, nil
	}
	return encoded, nil
}

func preserveLosslessPNGMetadata(original, encoded []byte) []byte {
	var metadata []byte
	for offset := 8; offset+12 <= len(original); {
		size := uint64(binary.BigEndian.Uint32(original[offset : offset+4]))
		if size+12 > uint64(len(original)-offset) {
			break
		}
		end := offset + int(size) + 12
		switch string(original[offset+4 : offset+8]) {
		case "iCCP", "gAMA", "cHRM", "sRGB", "cICP", "mDCV", "cLLI", "eXIf", "pHYs":
			metadata = append(metadata, original[offset:end]...)
		case "sBIT":
			if len(original) > 25 && len(encoded) > 25 && original[25] == encoded[25] {
				metadata = append(metadata, original[offset:end]...)
			}
		}
		offset = end
	}
	if len(metadata) == 0 || len(encoded) < 33 {
		return encoded
	}
	// Color management chunks precede PLTE and IDAT, directly after IHDR.
	out := make([]byte, 0, len(encoded)+len(metadata))
	out = append(out, encoded[:33]...)
	out = append(out, metadata...)
	return append(out, encoded[33:]...)
}

func encodeResponseImage(ctx context.Context, candidate *compressibleResponseImage, profile imageCompressionProfile) ([]byte, string, error) {
	// EXIF-bearing PNG/WebP conversions need an orientation-aware metadata
	// conversion. Keep them intact until that is supported. JPEG APP1 metadata
	// can be retained verbatim because its pixels stay in the same orientation.
	if (candidate.format == "png" && responseImageHasChunk(candidate.raw, "png", "eXIf")) ||
		(candidate.format == "webp" && responseImageHasChunk(candidate.raw, "webp", "EXIF")) {
		return candidate.raw, candidate.format, nil
	}
	decoded, format, err := image.Decode(bytes.NewReader(candidate.raw))
	if err != nil || format != candidate.format {
		return nil, "", imageCompressionInvalid()
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if side := max(candidate.config.Width, candidate.config.Height); side > profile.side {
		width := max(1, candidate.config.Width*profile.side/side)
		height := max(1, candidate.config.Height*profile.side/side)
		resized := image.NewNRGBA(image.Rect(0, 0, width, height))
		draw.ApproxBiLinear.Scale(resized, resized.Bounds(), decoded, decoded.Bounds(), draw.Src, nil)
		decoded = resized
	}
	var metadata []byte
	if candidate.format == "jpeg" {
		metadata = compressionJPEGMetadata(candidate.raw)
	}
	writer := &compressionImageWriter{ctx: ctx, limit: len(candidate.raw) - len(metadata) - 1}
	format = "png"
	if opaque, ok := decoded.(interface{ Opaque() bool }); ok && opaque.Opaque() {
		format = "jpeg"
		err = jpeg.Encode(writer, decoded, &jpeg.Options{Quality: profile.quality})
	} else {
		encoder := png.Encoder{CompressionLevel: png.BestCompression}
		err = encoder.Encode(writer, decoded)
	}
	if errors.Is(err, errCompressionOutputLimit) {
		return candidate.raw, candidate.format, nil
	}
	if err != nil {
		return nil, "", imageCompressionEncodeError(ctx)
	}
	encoded := writer.Bytes()
	if format == "jpeg" && len(metadata) > 0 {
		out := make([]byte, 0, len(encoded)+len(metadata))
		out = append(out, encoded[:2]...)
		out = append(out, metadata...)
		encoded = append(out, encoded[2:]...)
	}
	return encoded, format, nil
}

func responseImageHasChunk(raw []byte, format, name string) bool {
	offset := 8
	if format == "webp" {
		offset = 12
	}
	for offset+12 <= len(raw) {
		size := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
		kind := string(raw[offset+4 : offset+8])
		advance := size + 12
		if format == "webp" {
			size = uint64(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
			kind = string(raw[offset : offset+4])
			advance = size + (size & 1) + 8
		}
		if advance > uint64(len(raw)-offset) {
			return false
		}
		if kind == name {
			return true
		}
		offset += int(advance)
	}
	return false
}

func compressionJPEGMetadata(raw []byte) []byte {
	var metadata []byte
	for offset := 2; offset+4 <= len(raw); {
		if raw[offset] != 0xff {
			break
		}
		if raw[offset+1] == 0xff { // Padding between markers.
			offset++
			continue
		}
		marker := raw[offset+1]
		if marker == 0xda || marker == 0xd9 { // Start of scan / end of image.
			break
		}
		size := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
		if size < 2 || size+2 > len(raw)-offset {
			break
		}
		if marker == 0xe1 { // EXIF and XMP orientation live in APP1.
			metadata = append(metadata, raw[offset:offset+size+2]...)
		}
		offset += size + 2
	}
	return metadata
}

func imageCompressionEncodeError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return imageUploadProblem(400, "An inline image could not be compressed safely. No image was changed or sent.")
}

func setCompressedImage(parts []inferenceImagePart, raw []byte, format string) {
	encoded := base64.StdEncoding.EncodeToString(raw)
	for _, part := range parts {
		part.setInline("image/"+format, encoded)
	}
}
