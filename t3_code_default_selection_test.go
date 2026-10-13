package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestT3CodeDefaultSelectionUsesEnabledAccountAndOwnLibrary(t *testing.T) {
	codex := modelLibrary{SchemaVersion: 1, DefaultModel: "openai/gpt-6-astra", Models: []modelLibraryItem{{ID: "openai/gpt-6-astra"}}}
	claude := modelLibrary{SchemaVersion: 1, DefaultModel: "anthropic/claude-opus-5-5", Models: []modelLibraryItem{{ID: "anthropic/claude-opus-5-5"}}}
	routes, err := t3CodeBuildClaudeRoutes(claude)
	if err != nil {
		t.Fatal(err)
	}
	policy := t3CodeModelPolicy{CodexLibrary: codex, ClaudeLibrary: claude, ClaudeRoutes: routes}
	for _, tc := range []struct {
		name          string
		codex, claude bool
		current, want any
	}{
		{"claude_only", false, true, nil, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "claude-opus-5-5"}},
		{"codex_only", true, false, nil, map[string]any{"instanceId": t3CodeCodexProxyID, "model": "openai/gpt-6-astra"}},
		{"native_claude_retained", true, true, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "claude-opus-5-5", "options": map[string]any{"effort": "high"}}, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "claude-opus-5-5", "options": map[string]any{"effort": "high"}}},
		{"legacy_gateway_claude_upgraded", true, true, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "anthropic/claude-opus-5-5", "options": map[string]any{"effort": "high"}}, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "claude-opus-5-5", "options": map[string]any{"effort": "high"}}},
		{"disabled_default_replaced", false, true, map[string]any{"instanceId": t3CodeCodexProxyID, "model": "openai/gpt-6-astra"}, map[string]any{"instanceId": t3CodeClaudeProxyID, "model": "claude-opus-5-5"}},
		{"direct_native_retained", false, false, map[string]any{"instanceId": t3CodeClaudeNormalID, "model": "future-native-model", "options": map[string]any{"effort": "high"}}, map[string]any{"instanceId": t3CodeClaudeNormalID, "model": "future-native-model", "options": map[string]any{"effort": "high"}}},
		{"optional_native_retained", false, false, map[string]any{"instanceId": "pi", "model": "default"}, map[string]any{"instanceId": "pi", "model": "default"}},
		{"native_fallback", false, false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providers := map[string]any{t3CodeCodexProxyID: map[string]any{"enabled": tc.codex}, t3CodeClaudeProxyID: map[string]any{"enabled": tc.claude}, t3CodeClaudeNormalID: map[string]any{"enabled": true}, "pi": map[string]any{"enabled": true}}
			before, _ := json.Marshal(tc.current)
			if actual := t3CodeDefaultSelection(tc.current, providers, policy); !reflect.DeepEqual(actual, tc.want) {
				t.Fatal(actual, tc.want)
			}
			after, _ := json.Marshal(tc.current)
			if string(before) != string(after) {
				t.Fatal("selection was mutated")
			}
		})
	}
	if actual := t3CodeDefaultSelection(map[string]any{"instanceId": t3CodeCodexProxyID, "model": codex.DefaultModel}, map[string]any{}, policy); actual != nil {
		t.Fatal("all-off profile retained a selected account", actual)
	}
}

func TestT3CodeClaudeOnlyWorkspaceDefaultIsEnabled(t *testing.T) {
	a := t3CodeTestApp(t, "macos")
	prefs := normalizeT3CodePreferences(a.config.T3Code)
	prefs.Codex = false
	prefs.Claude = false
	prefs.KiloCodex = false
	prefs.KiloClaude = true
	prefs.ClaudeModels = "shared"
	a.config.T3Code = prefs
	prepareT3CodeFixture(t, a)
	data, err := os.ReadFile(t3CodePaths(a.dir).Settings)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	selection := settings["defaultModelSelection"].(map[string]any)
	id := selection["instanceId"].(string)
	provider := settings["providerInstances"].(map[string]any)[id].(map[string]any)
	if id != t3CodeClaudeProxyID || provider["enabled"] != true {
		t.Fatalf("prepared default selects disabled or wrong provider %s: %+v", id, selection)
	}
	binary, _ := a.launcher.resolve("t3-code", "")
	if !a.t3CodeReady(mustReadT3CodePrepared(t, a), binary, *a.launcher) {
		t.Fatal("Claude-only profile is not ready")
	}
}
