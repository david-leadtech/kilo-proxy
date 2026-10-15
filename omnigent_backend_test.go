package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestOmnigentPreferencesStrictAndScopedTokens(t *testing.T) {
	p := normalizeOmnigentPreferences(omnigentPreferences{})
	raw, _ := json.Marshal(p)
	if actual, err := decodeOmnigentPreferences(raw); err != nil || actual != p {
		t.Fatalf("valid preferences rejected: %v", err)
	}
	for _, bad := range []string{`null`, `{}`, strings.Replace(string(raw), `"codex":true`, `"codex":null`, 1), strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1), strings.Replace(string(raw), `"codexConnection":"kilo"`, `"codexConnection":"other"`, 1)} {
		if _, err := decodeOmnigentPreferences([]byte(bad)); err == nil {
			t.Fatalf("accepted malformed preferences: %s", bad)
		}
	}
	key := "synthetic-local-key"
	for _, scope := range []string{"codex", "claude"} {
		token := omnigentProxyToken(key, scope)
		if token == key || omnigentProxyScope(token, key) != scope || omnigentProxyScope(token, key+"changed") != "" {
			t.Fatal("token scope or key binding failed")
		}
		for _, path := range []string{"/v1/models", "/omnigent/other/v1/models", "/omnigent/" + scope + "/v1/models?bad=true"} {
			req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
			if _, ok := omnigentInferencePath(req, scope); ok {
				t.Fatalf("accepted out-of-scope route %s", path)
			}
		}
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/omnigent/"+scope+"/v1/models", nil)
		if route, ok := omnigentInferencePath(req, scope); !ok || route != "/v1/models" {
			t.Fatal("scoped catalog rejected")
		}
	}
	if omnigentProxyScope(key, key) != "" {
		t.Fatal("general key was accepted as an Omnigent scoped token")
	}
}

func TestOmnigentModelPolicyExactAccountsAndConfiguration(t *testing.T) {
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/nested/two", Models: []modelLibraryItem{{ID: "openai/exact-one", ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "low"}, {ID: "vendor/nested/two", ReasoningCustom: true, ReasoningLevels: []string{}}}}
	p := normalizeOmnigentPreferences(omnigentPreferences{})
	p.CodexModels = "official"
	p.ClaudeConnection = "native"
	policy, err := deriveOmnigentModelPolicy(p, library, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.CodexLibrary.Models) != 1 || policy.CodexLibrary.DefaultModel != "openai/exact-one" || len(policy.ClaudeLibrary.Models) != 0 {
		t.Fatalf("wrong per-account projection: %+v", policy)
	}
	p.CodexModels = "shared"
	p.ClaudeConnection = "kilo"
	policy, err = deriveOmnigentModelPolicy(p, library, nil)
	if err != nil || !reflect.DeepEqual(policy.CodexLibrary, library) || !reflect.DeepEqual(policy.ClaudeLibrary, library) {
		t.Fatalf("shared IDs changed: %v", err)
	}
	o := omnigentRuntimeOptions{Preferences: p, CodexPath: "/synthetic/codex", ClaudePath: "/synthetic/claude", CodexLibrary: policy.CodexLibrary, ClaudeLibrary: policy.ClaudeLibrary, CodexToken: "synthetic-codex-token", ClaudeToken: "synthetic-claude-token"}
	config, inference, err := omnigentConfiguration(o, 8877)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{o.CodexToken, o.ClaudeToken} {
		if strings.Contains(string(config), secret) || strings.Contains(string(inference), secret) {
			t.Fatal("secret value was embedded in provider configuration")
		}
	}
	var document struct {
		Inference struct {
			Harnesses map[string]struct {
				DefaultModel   string   `json:"default_model"`
				ModelAllowlist []string `json:"model_allowlist"`
			}
		}
	}
	if json.Unmarshal(inference, &document) != nil {
		t.Fatal("invalid generated inference document")
	}
	for _, harness := range []string{"codex", "codex-native", "claude-sdk", "claude-native"} {
		binding := document.Inference.Harnesses[harness]
		if binding.DefaultModel != library.DefaultModel || !reflect.DeepEqual(binding.ModelAllowlist, []string{"openai/exact-one", "vendor/nested/two"}) {
			t.Fatalf("wrong exact binding for %s: %+v", harness, binding)
		}
	}
	p.ClaudeModels = "official"
	catalog := []modelInfo{{ID: "anthropic/claude-opus-5.5"}, {ID: "anthropic/claude-sonnet-5.5"}, {ID: "anthropic/claude-fable-5.1"}}
	if policy, err = deriveOmnigentModelPolicy(p, library, catalog); err != nil || len(policy.ClaudeLibrary.Models) != 3 {
		t.Fatalf("official Claude models unavailable: %v", err)
	}
	catalog = append(catalog, modelInfo{ID: "anthropic/claude-opus-5-5-20261015"})
	if _, err = deriveOmnigentModelPolicy(p, library, catalog); err == nil {
		t.Fatal("ambiguous official gateway destination accepted")
	}
}

func TestOmnigentRequestReasoningExactModelAndTools(t *testing.T) {
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/exact", Models: []modelLibraryItem{{ID: "vendor/exact", ReasoningCustom: true, ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "low"}, {ID: "vendor/plain", ReasoningCustom: true, ReasoningLevels: []string{}}}}
	for _, scope := range []string{"codex", "claude"} {
		for _, effort := range []string{"", "high", "none"} {
			t.Run(scope+"/"+effort, func(t *testing.T) {
				body := map[string]any{"model": "vendor/exact", "tools": []any{map[string]any{"name": "synthetic_echo", "input_schema": map[string]any{"type": "object"}}}, "input": []any{map[string]any{"role": "user", "content": "synthetic only"}}}
				if effort != "" {
					body["reasoning_effort"] = effort
				}
				original, _ := json.Marshal(body)
				req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(string(original)))
				if err := prepareOmnigentModelRequest(req, scope, library, nil); err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(req.Body)
				var result map[string]any
				if json.Unmarshal(data, &result) != nil {
					t.Fatal("invalid rewritten JSON")
				}
				if result["model"] != "vendor/exact" || !reflect.DeepEqual(result["tools"], body["tools"]) || !reflect.DeepEqual(result["input"], body["input"]) {
					t.Fatal("model/tools/input changed")
				}
				expected := effort
				if expected == "" {
					expected = "low"
				}
				if scope == "codex" {
					if result["reasoning"].(map[string]any)["effort"] != expected {
						t.Fatal("default/explicit effort changed")
					}
				} else if expected == "none" {
					if result["thinking"].(map[string]any)["type"] != "disabled" {
						t.Fatal("none did not disable Claude thinking")
					}
				} else if result["output_config"].(map[string]any)["effort"] != expected {
					t.Fatal("Claude effort changed")
				}
			})
		}
	}
	for _, raw := range []string{`{"model":"outside/selection"}`, `{"model":"vendor/exact","model":"vendor/plain"}`, `{"model":"vendor/exact","reasoning_effort":"max"}`} {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(raw))
		if prepareOmnigentModelRequest(req, "codex", library, nil) == nil {
			t.Fatalf("invalid model/request accepted: %s", raw)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(`{"model":"vendor/plain","reasoning":{"effort":"high"},"thinking":{"type":"adaptive"},"output_config":{"effort":"high","format":{"type":"json_schema"}}}`))
	if err := prepareOmnigentModelRequest(req, "claude", library, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(req.Body)
	var result map[string]any
	_ = json.Unmarshal(data, &result)
	if result["reasoning"] != nil || result["thinking"] != nil || result["output_config"].(map[string]any)["effort"] != nil || result["output_config"].(map[string]any)["format"] == nil {
		t.Fatal("[] did not remove only reasoning fields")
	}
}

func TestOmnigentConfirmedSessionNoneOverridesNativeOldHigh(t *testing.T) {
	for _, levels := range [][]string{{"none", "low", "high"}, {}} {
		t.Run(strings.Join(levels, ","), func(t *testing.T) {
			library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/exact", Models: []modelLibraryItem{{ID: "vendor/exact", ReasoningCustom: true, ReasoningLevels: levels, ReasoningEffort: "low"}}}
			request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", strings.NewReader(`{"model":"vendor/exact","thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"tools":[{"name":"synthetic_echo"}]}`))
			request = request.WithContext(context.WithValue(request.Context(), omnigentEffortContextKey{}, "none"))
			if err := prepareOmnigentModelRequest(request, "claude", library, nil); err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(request.Body)
			var body map[string]any
			_ = json.Unmarshal(data, &body)
			if body["model"] != "vendor/exact" || body["output_config"] != nil || body["tools"] == nil {
				t.Fatal("confirmed none changed scope/model/tools or retained high")
			}
			if len(levels) == 0 {
				if body["thinking"] != nil {
					t.Fatal("unsupported thinking retained")
				}
			} else if body["thinking"].(map[string]any)["type"] != "disabled" {
				t.Fatal("native high overrode confirmed none")
			}
		})
	}
	for _, scope := range []string{"codex", "claude"} {
		request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/omnigent/claude/v1/session-effort", strings.NewReader(`{}`))
		if _, ok := omnigentInferencePath(request, scope); ok {
			t.Fatal("session control leaked into the generic inference route")
		}
	}
}
