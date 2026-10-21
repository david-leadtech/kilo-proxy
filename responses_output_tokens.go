package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (a *app) prepareResponsesMaxOutputTokens(r *http.Request, model string) error {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Content-Encoding") != "" {
		return nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	doc, err := decodeObject(data)
	if err != nil {
		return nil
	}
	if _, exists := doc["max_output_tokens"]; exists {
		return nil
	}
	limit := a.responsesMaxOutputTokensLimit(model)
	if limit <= 0 {
		return nil
	}
	doc["max_output_tokens"] = limit
	data, err = json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(data) > bridgeLimit {
		return &http.MaxBytesError{Limit: bridgeLimit}
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Del("Content-Length")
	return nil
}

func (a *app) responsesMaxOutputTokensLimit(model string) int {
	id := strings.TrimPrefix(model, "~")
	a.mu.Lock()
	scope := a.catalogScopeLocked()
	a.mu.Unlock()
	publishedLimit := 0
	for _, m := range readNativeCatalogCache(a.dir, scope) {
		if m.ID == id && m.MaxOutputTokens > 0 {
			publishedLimit = m.MaxOutputTokens
			break
		}
	}
	if a.modelLibrary != nil {
		for _, item := range a.modelLibrary.snapshot().Library.Models {
			if item.ID == id && item.MaxOutputTokens > 0 {
				// Saved output preferences cannot exceed a known provider ceiling.
				if publishedLimit > 0 {
					return min(item.MaxOutputTokens, publishedLimit)
				}
				return item.MaxOutputTokens
			}
		}
	}
	return publishedLimit
}
