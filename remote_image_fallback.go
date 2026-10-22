package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const remoteImageFallbackMarker = "[Kilo Proxy: unavailable image omitted]"
const remoteImageFallbackResponseLimit = 256 << 10

type remoteImageFallbackPlan struct {
	body    []byte
	skipped int
}

type remoteImageFallbackContextKey struct{}
type remoteImageFallbackAttemptedContextKey struct{}

// prepareRemoteImageFallback makes one replayable fallback request for image
// download failures. It only inspects documented image slots; arbitrary URLs in
// text, tool arguments and other JSON remain untouched.
func prepareRemoteImageFallback(r *http.Request) (*http.Request, error) {
	if r.Method != http.MethodPost || !imageTransportPath(r.URL.Path) || r.Header.Get("Content-Encoding") != "" || r.Body == nil {
		return r, nil
	}
	data, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return r, err
	}
	if len(data) > bridgeLimit {
		return r, &http.MaxBytesError{Limit: bridgeLimit}
	}
	doc, err := decodeObject(data)
	if err != nil {
		return r, nil // Preserve the gateway's normal invalid-JSON response.
	}
	skipped := replaceRemoteImageSlots(doc, r.URL.Path)
	if skipped == 0 {
		return r, nil
	}
	fallback, err := json.Marshal(doc)
	if err != nil {
		return r, err
	}
	if len(fallback) > bridgeLimit {
		return r, &http.MaxBytesError{Limit: bridgeLimit}
	}
	plan := &remoteImageFallbackPlan{body: fallback, skipped: skipped}
	prepared := r.WithContext(context.WithValue(r.Context(), remoteImageFallbackContextKey{}, plan))
	prepared.Body = io.NopCloser(bytes.NewReader(data))
	prepared.ContentLength = int64(len(data))
	prepared.GetBody = nil
	prepared.TransferEncoding = nil
	prepared.Header.Del("Content-Length")
	prepared.Header.Del("Transfer-Encoding")
	return prepared, nil
}

func replaceRemoteImageSlots(doc map[string]any, path string) int {
	if path == "/v1/responses" {
		input, _ := doc["input"].([]any)
		total := 0
		for _, value := range input {
			item := object(value)
			if item == nil {
				continue
			}
			switch stringValue(item["type"]) {
			case "function_call_output":
				if output, ok := item["output"].([]any); ok {
					updated, skipped := replaceRemoteImageContent(output, path)
					item["output"], total = updated, total+skipped
				}
			case "", "message":
				if content, ok := item["content"].([]any); ok {
					updated, skipped := replaceRemoteImageContent(content, path)
					item["content"], total = updated, total+skipped
				}
			}
		}
		return total
	}

	messages, _ := doc["messages"].([]any)
	total := 0
	for _, value := range messages {
		message := object(value)
		if message == nil {
			continue
		}
		if content, ok := message["content"].([]any); ok {
			updated, skipped := replaceRemoteImageContent(content, path)
			message["content"], total = updated, total+skipped
		}
	}
	return total
}

func replaceRemoteImageContent(content []any, path string) ([]any, int) {
	updated := make([]any, 0, len(content))
	skipped := 0
	for _, value := range content {
		part := object(value)
		if part == nil {
			updated = append(updated, value)
			continue
		}
		if _, ok := remoteImageURL(part, path); ok {
			updated = append(updated, remoteImageFallbackPlaceholder(path))
			skipped++
			continue
		}
		if path == "/v1/messages" && stringValue(part["type"]) == "tool_result" {
			if nested, ok := part["content"].([]any); ok {
				replaced, nestedSkipped := replaceRemoteImageContent(nested, path)
				part["content"], skipped = replaced, skipped+nestedSkipped
			}
		}
		updated = append(updated, value)
	}
	return updated, skipped
}

func remoteImageURL(part map[string]any, path string) (string, bool) {
	var value string
	switch path {
	case "/v1/responses":
		if stringValue(part["type"]) != "input_image" {
			return "", false
		}
		value = stringValue(part["image_url"])
	case "/v1/chat/completions":
		if stringValue(part["type"]) != "image_url" {
			return "", false
		}
		if imageURL, ok := part["image_url"].(string); ok {
			value = imageURL
		} else {
			value = stringValue(object(part["image_url"])["url"])
		}
	case "/v1/messages":
		if stringValue(part["type"]) != "image" || stringValue(object(part["source"])["type"]) != "url" {
			return "", false
		}
		value = stringValue(object(part["source"])["url"])
	default:
		return "", false
	}
	parsed, err := url.ParseRequestURI(value)
	return value, err == nil && parsed.Host != "" &&
		(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https"))
}

func remoteImageFallbackPlaceholder(path string) map[string]any {
	if path == "/v1/responses" {
		return map[string]any{"type": "input_text", "text": remoteImageFallbackMarker}
	}
	return map[string]any{"type": "text", "text": remoteImageFallbackMarker}
}

type remoteImageFallbackTransport struct {
	base http.RoundTripper
}

func (t remoteImageFallbackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	plan, _ := r.Context().Value(remoteImageFallbackContextKey{}).(*remoteImageFallbackPlan)
	if err != nil || response == nil || plan == nil || plan.skipped == 0 || r.Context().Value(remoteImageFallbackAttemptedContextKey{}) != nil {
		return response, err
	}
	// Never buffer successful responses: Responses, Messages, and Chat
	// Completions commonly stream indefinitely until the client finishes. The
	// fallback is only relevant to the gateway's known HTTP 400 download error.
	if response.StatusCode != http.StatusBadRequest {
		return response, err
	}

	body, complete, replay := readReplayableResponseBody(response.Body, remoteImageFallbackResponseLimit)
	if !complete {
		response.Body = replay
		return response, err
	}
	if !isRemoteImageDownloadFailure(response, body) {
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		return response, err
	}
	// The complete response body was already closed by the bounded reader.
	if contextErr := r.Context().Err(); contextErr != nil {
		return nil, contextErr
	}

	ctx := context.WithValue(r.Context(), remoteImageFallbackAttemptedContextKey{}, true)
	retry := r.Clone(ctx)
	retry.Body = io.NopCloser(bytes.NewReader(plan.body))
	retry.ContentLength = int64(len(plan.body))
	retry.GetBody = nil
	retry.TransferEncoding = nil
	retry.Header = retry.Header.Clone()
	retry.Header.Del("Content-Length")
	retry.Header.Del("Transfer-Encoding")
	fallback, retryErr := t.base.RoundTrip(retry)
	if retryErr == nil && fallback != nil {
		return fallback, nil
	}
	if contextErr := r.Context().Err(); contextErr != nil {
		return nil, contextErr
	}
	if errors.Is(retryErr, context.Canceled) || errors.Is(retryErr, context.DeadlineExceeded) {
		return nil, retryErr
	}

	// If the fallback itself could not be sent, preserve the original provider
	// error instead of returning a misleading transport failure.
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return response, nil
}

type replayableResponseBody struct {
	io.Reader
	io.Closer
}

func readReplayableResponseBody(body io.ReadCloser, limit int) ([]byte, bool, io.ReadCloser) {
	if body == nil {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, int64(limit)+1))
	if err != nil || len(data) > limit {
		readers := []io.Reader{bytes.NewReader(data)}
		if err != nil {
			readers = append(readers, &replayableResponseReadError{err: err})
		}
		readers = append(readers, body)
		return nil, false, &replayableResponseBody{Reader: io.MultiReader(readers...), Closer: body}
	}
	_ = body.Close()
	return data, true, nil
}

// Replay an error consumed during inspection before resuming the original body.
type replayableResponseReadError struct{ err error }

func (r *replayableResponseReadError) Read([]byte) (int, error) {
	if r.err == nil {
		return 0, io.EOF
	}
	err := r.err
	r.err = nil
	return 0, err
}

func isRemoteImageDownloadFailure(response *http.Response, body []byte) bool {
	if response.StatusCode != http.StatusBadRequest {
		return false
	}
	var envelope struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Error == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(envelope.Error.Message), "Error while downloading file.")
}
