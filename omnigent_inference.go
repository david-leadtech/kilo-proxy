package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"
)

type omnigentRequestScopeKey struct{}
type omnigentEffortContextKey struct{}

// A native Claude process cannot select /effort none. Its private session
// header binds a locally confirmed selection to the exact prepared model;
// credentials and overrides remain restricted to the Claude Omnigent route.
type omnigentSessionEffort struct {
	Effort, Fingerprint string
	UpdatedAt           time.Time
}

var omnigentSessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (a *app) omnigentSessionEffortAPI(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SessionID string `json:"sessionId"`
		Model     string `json:"model"`
		Effort    string `json:"effort"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if !omnigentSessionID.MatchString(input.SessionID) || !effortNames[input.Effort] {
		jsonError(w, 400, "Invalid Omnigent session reasoning selection.")
		return
	}
	library, catalog, err := a.omnigentScopeLibrary("claude")
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	allowed := false
	for _, choice := range terminalLibraryChoices(library, catalog) {
		if choice.Model.ID == input.Model {
			levels, _ := nativeReasoningFor(choice)
			allowed = helperContains(levels, input.Effort) || len(levels) == 0 && input.Effort == "none"
			break
		}
	}
	if !allowed {
		jsonError(w, 400, "Choose a reasoning level enabled for this Omnigent model.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, err := a.readOmnigentPrepared()
	if err != nil || !a.omnigentReady(saved, a.launchRuntime()) {
		jsonError(w, 409, "Prepare Omnigent again before changing session reasoning.")
		return
	}
	if a.omnigentSessionEfforts == nil {
		a.omnigentSessionEfforts = map[string]omnigentSessionEffort{}
	}
	for key, value := range a.omnigentSessionEfforts {
		if value.Fingerprint != saved.Fingerprint || time.Since(value.UpdatedAt) > 24*time.Hour {
			delete(a.omnigentSessionEfforts, key)
		}
	}
	key := input.SessionID + ":" + input.Model
	if _, exists := a.omnigentSessionEfforts[key]; !exists && len(a.omnigentSessionEfforts) >= 2048 {
		jsonError(w, 409, "Too many active Omnigent reasoning selections. Restart its private workspace.")
		return
	}
	a.omnigentSessionEfforts[key] = omnigentSessionEffort{input.Effort, saved.Fingerprint, time.Now()}
	jsonResponse(w, 200, map[string]any{"ok": true, "sessionId": input.SessionID, "model": input.Model, "effort": input.Effort})
}

func (a *app) omnigentSessionEffortRequest(r *http.Request) (*http.Request, error) {
	session := r.Header.Get("X-Kilo-Omnigent-Session")
	if session == "" {
		return r, nil
	}
	fail := errors.New("Confirm the reasoning selection in this Omnigent session again.")
	if !omnigentSessionID.MatchString(session) {
		return r, fail
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return r, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(data))
	body, err := decodeClaudeDesktopObject(data)
	if err != nil {
		return r, fail
	}
	var model string
	if json.Unmarshal(body["model"], &model) != nil {
		return r, fail
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, err := a.readOmnigentPrepared()
	if err != nil {
		return r, fail
	}
	selection, exists := a.omnigentSessionEfforts[session+":"+model]
	if !exists || selection.Fingerprint != saved.Fingerprint || time.Since(selection.UpdatedAt) > 24*time.Hour {
		return r, fail
	}
	return r.Clone(context.WithValue(r.Context(), omnigentEffortContextKey{}, selection.Effort)), nil
}

func omnigentProxyToken(localKey, scope string) string {
	mac := hmac.New(sha256.New, []byte(localKey))
	mac.Write([]byte("kilo-omnigent-v1:" + scope))
	return hex.EncodeToString(mac.Sum(nil))
}
func omnigentProxyScope(token, localKey string) string {
	if localKey == "" {
		return ""
	}
	for _, scope := range []string{"codex", "claude"} {
		if secureEqual(token, omnigentProxyToken(localKey, scope)) {
			return scope
		}
	}
	return ""
}
func omnigentInferencePath(r *http.Request, scope string) (string, bool) {
	if r.URL.RawPath != "" || r.URL.ForceQuery || r.URL.RawQuery != "" && !(scope == "claude" && r.URL.RawQuery == "beta=true") {
		return "", false
	}
	base := "/omnigent/" + scope + "/v1/"
	if r.Method == http.MethodGet && r.URL.RawQuery == "" && r.URL.Path == base+"models" {
		return "/v1/models", true
	}
	if r.Method == http.MethodPost {
		if scope == "codex" && r.URL.Path == base+"responses" && r.URL.RawQuery == "" {
			return "/v1/responses", true
		}
		if scope == "claude" && r.URL.Path == base+"messages" {
			return "/v1/messages", true
		}
	}
	return "", false
}
func (a *app) omnigentScopeLibrary(scope string) (modelLibrary, []modelInfo, error) {
	fail := errors.New("Prepare Omnigent again in Kilo Proxy before using this connection.")
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, err := a.readOmnigentPrepared()
	if err != nil || !a.omnigentReady(saved, a.launchRuntime()) {
		return modelLibrary{}, nil, fail
	}
	library := saved.CodexLibrary
	enabled := saved.Preferences.Codex && saved.Preferences.CodexConnection == "kilo"
	if scope == "claude" {
		library = saved.ClaudeLibrary
		enabled = saved.Preferences.Claude && saved.Preferences.ClaudeConnection == "kilo"
	}
	if scope != "codex" && scope != "claude" || !enabled || len(library.Models) == 0 {
		return modelLibrary{}, nil, fail
	}
	return library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()), nil
}
func (a *app) serveOmnigentModels(w http.ResponseWriter, scope string) {
	library, catalog, err := a.omnigentScopeLibrary(scope)
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	models := []map[string]any{}
	for _, model := range omnigentModelSummaries(library, catalog) {
		models = append(models, map[string]any{"id": model.ID, "name": model.Name, "object": "model", "owned_by": "kilo-proxy", "is_default": model.IsDefault, "supported_reasoning_efforts": model.ReasoningLevels, "default_reasoning_effort": model.ReasoningEffort})
	}
	jsonResponse(w, 200, map[string]any{"object": "list", "data": models})
}

func prepareOmnigentModelRequest(r *http.Request, scope string, library modelLibrary, catalog []modelInfo) error {
	if r.Body == nil {
		return errors.New("Invalid Omnigent request JSON.")
	}
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return err
	}
	body, err := decodeClaudeDesktopObject(raw)
	if err != nil {
		return errors.New("Omnigent requests must contain valid JSON with unique keys.")
	}
	var id string
	if json.Unmarshal(body["model"], &id) != nil {
		return errors.New("Choose a model from the prepared Omnigent selection.")
	}
	var selected *nativeModelChoice
	for _, choice := range terminalLibraryChoices(library, catalog) {
		if choice.Model.ID == id {
			copy := choice
			selected = &copy
			break
		}
	}
	if selected == nil {
		return errors.New("This model is not selected for this Omnigent connection.")
	}
	levels, initial := nativeReasoningFor(*selected)
	var reasoning, output map[string]json.RawMessage
	if value, exists := body["reasoning"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if json.Unmarshal(value, &reasoning) != nil || reasoning == nil {
			return errors.New("Invalid Omnigent reasoning selection.")
		}
	}
	if value, exists := body["output_config"]; exists && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if json.Unmarshal(value, &output) != nil || output == nil {
			return errors.New("Invalid Omnigent output settings.")
		}
	}
	effort := initial
	for _, value := range []json.RawMessage{reasoning["effort"], body["reasoning_effort"], output["effort"]} {
		if len(value) > 0 {
			if json.Unmarshal(value, &effort) != nil || !effortNames[effort] {
				return errors.New("Invalid Omnigent reasoning effort.")
			}
		}
	}
	if confirmed, ok := r.Context().Value(omnigentEffortContextKey{}).(string); ok {
		effort = confirmed
	}
	if len(levels) == 0 {
		delete(body, "reasoning")
		delete(body, "reasoning_effort")
		delete(body, "thinking")
		delete(output, "effort")
	} else {
		if effort != "" && !helperContains(levels, effort) {
			return errors.New("Choose a reasoning level enabled for this Omnigent model.")
		}
		if scope == "codex" {
			if effort != "" {
				if reasoning == nil {
					reasoning = map[string]json.RawMessage{}
				}
				reasoning["effort"], _ = json.Marshal(effort)
				body["reasoning"], _ = json.Marshal(reasoning)
			}
			delete(body, "reasoning_effort")
		} else if effort != "" {
			delete(body, "reasoning")
			delete(body, "reasoning_effort")
			if effort == "none" {
				body["thinking"] = json.RawMessage(`{"type":"disabled"}`)
				delete(output, "effort")
			} else {
				if output == nil {
					output = map[string]json.RawMessage{}
				}
				output["effort"], _ = json.Marshal(effort)
				body["thinking"] = json.RawMessage(`{"type":"adaptive"}`)
			}
		}
	}
	if len(output) > 0 {
		body["output_config"], _ = json.Marshal(output)
	} else {
		delete(body, "output_config")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(data) > 32<<20 {
		return &http.MaxBytesError{Limit: 32 << 20}
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.GetBody = nil
	r.ContentLength = int64(len(data))
	r.Header.Del("Content-Length")
	return nil
}
