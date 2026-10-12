package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

type t3CodeRequestContextKey struct{}

func t3CodeInferencePath(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/t3-code/v1/messages" && r.URL.RawPath == "" && (r.URL.RawQuery == "" || r.URL.RawQuery == "beta=true") && !r.URL.ForceQuery
}

func t3CodeResolveModel(library modelLibrary, routes map[string]string, requested string) (string, error) {
	if err := validateT3CodeClaudeRoutes(library, routes); err != nil {
		return "", err
	}
	for _, model := range library.Models {
		if model.ID == requested {
			return requested, nil
		}
	}
	if target := routes[requested]; target != "" {
		return target, nil
	}
	return "", errors.New("This model is not selected for Claude · Kilo in the prepared T3 Code profile. Choose an available model or prepare T3 Code again.")
}

func (a *app) prepareT3CodeRequest(r *http.Request) (*http.Request, error) {
	if managed, _ := r.Context().Value(t3CodeRequestContextKey{}).(bool); !managed {
		return r, nil
	}
	if r.Body == nil {
		return r, errors.New("Invalid T3 Code request JSON.")
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return r, err
	}
	restore := func(data []byte) {
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.GetBody = nil
		r.ContentLength = int64(len(data))
		r.Header.Del("Content-Length")
	}
	restore(raw)
	request, err := decodeClaudeDesktopObject(raw)
	if err != nil {
		return r, errors.New("T3 Code requests must contain valid JSON with unique keys.")
	}
	var requested string
	if json.Unmarshal(request["model"], &requested) != nil || requested == "" {
		return r, errors.New("Choose a model in the prepared T3 Code selection.")
	}
	a.mu.Lock()
	rt := a.launchRuntime()
	binary, resolveErr := rt.resolve("t3-code", "")
	saved, readErr := a.readT3CodePrepared()
	ready := resolveErr == nil && readErr == nil && a.t3CodeReady(saved, binary, rt)
	a.mu.Unlock()
	if !ready {
		return r, errors.New("Prepare T3 Code again before using its managed models.")
	}
	model, err := t3CodeResolveModel(saved.ClaudeLibrary, saved.ClaudeRoutes, requested)
	if err != nil {
		return r, err
	}
	if model == requested {
		return r, nil
	}
	request["model"], _ = json.Marshal(model)
	encoded, err := json.Marshal(request)
	if err != nil {
		return r, err
	}
	// Only the native client-facing response uses the selected slug. Upstream
	// requests, accounting and traces retain the exact prepared gateway model.
	r = r.Clone(context.WithValue(r.Context(), claudeDesktopAliasContextKey{}, claudeDesktopAliasRoute{Alias: requested, Model: model}))
	restore(encoded)
	if observer, _ := r.Context().Value(usageContextKey{}).(*usageObserver); observer != nil {
		observer.mu.Lock()
		observer.usage.Model = model
		observer.mu.Unlock()
	}
	return r, nil
}
