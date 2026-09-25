package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestClaudeContextBudgetAcrossModelSwitches(t *testing.T) {
	selection := claudeFixture()
	selection.Models[0].Context, selection.Models[0].Output = 272000, 68000
	selection.Models[1].Context, selection.Models[1].Output = 128000, 32000
	old := []byte(`{"permissions":{"deny":["Read(.env)"]},"autoCompactWindow":999999,"autoCompactEnabled":false,"env":{"DISABLE_COMPACT":"1","DISABLE_AUTO_COMPACT":"1","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"999999","CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT":"1","CLAUDE_AUTOCOMPACT_PCT_OVERRIDE":"100","CLAUDE_CODE_MAX_OUTPUT_TOKENS":"128000","KEEP":"yes"}}`)
	merged, err := mergeClaudeSettings(old, selection, claudeCaps("2.1.263"), 8877, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Window  int               `json:"autoCompactWindow"`
		Enabled bool              `json:"autoCompactEnabled"`
		Env     map[string]string `json:"env"`
	}
	if err := json.Unmarshal(merged, &result); err != nil {
		t.Fatal(err)
	}
	if result.Window != 128000 || !result.Enabled || result.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "128000" || result.Env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "32000" || result.Env["DISABLE_COMPACT"] != "0" || result.Env["DISABLE_AUTO_COMPACT"] != "0" || result.Env["KEEP"] != "yes" {
		t.Fatalf("wrong session budget: %+v", result)
	}
	for _, name := range []string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT", "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"} {
		if _, exists := result.Env[name]; exists {
			t.Fatalf("conflicting override retained: %s", name)
		}
	}
	if !bytes.Contains(merged, []byte("Read(.env)")) {
		t.Fatal("unrelated permissions changed")
	}
	again, err := mergeClaudeSettings(merged, selection, claudeCaps("2.1.263"), 8877, "fixture")
	if err != nil || !bytes.Equal(again, merged) {
		t.Fatal("context profile is not idempotent", err)
	}
	selection.Initial = selection.Models[1].ID
	if window, output := claudeContextBudget(selection); window != 128000 || output != 32000 {
		t.Fatal("default model changes must not widen session budget")
	}
}

func TestClaudeContextSupportedRangeAndLegacyPreservation(t *testing.T) {
	selection := claudeFixture()
	selection.Models[0].Context = 1050000
	selection.Models[1].Context = 2000000
	if window, _ := claudeContextBudget(selection); window != 1000000 {
		t.Fatal(window)
	}
	selection.Models[0].Context = 64000
	if _, err := mergeClaudeSettings(nil, selection, claudeCaps("2.1.263"), 8877, "fixture"); err == nil {
		t.Fatal("silently raised a budget below Claude's supported range")
	}
	selection.Models[0].Context, selection.Models[1].Context = 0, 0
	legacy, err := mergeClaudeSettings([]byte(`{"autoCompactWindow":150000,"env":{"CLAUDE_CODE_MAX_OUTPUT_TOKENS":"5000"}}`), selection, claudeCaps("2.1.263"), 8877, "fixture")
	if err != nil || !bytes.Contains(legacy, []byte(`"autoCompactWindow": 150000`)) || !bytes.Contains(legacy, []byte(`"CLAUDE_CODE_MAX_OUTPUT_TOKENS": "5000"`)) {
		t.Fatal("legacy settings unexpectedly changed", err, string(legacy))
	}
}

func TestOpenCodeContextExportWithoutOutputLimit(t *testing.T) {
	data, err := mergeEditorSettings(nil, "opencode", exampleEditorSelection(), "http://127.0.0.1:8877/v1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Provider map[string]struct {
			Models map[string]struct {
				Limit map[string]int `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	limits := config.Provider["kilo-local"].Models["vendor/two"].Limit
	if limits["context"] != 128000 {
		t.Fatalf("context missing: %+v", limits)
	}
	if _, exists := limits["output"]; exists {
		t.Fatal("invented explicit output limit")
	}
}

func TestCodexPerModelBudgetsReplaceGlobalOverrides(t *testing.T) {
	var catalog map[string]any
	if err := json.Unmarshal([]byte(testCatalog), &catalog); err != nil {
		t.Fatal(err)
	}
	for _, value := range catalog["models"].([]any) {
		value.(map[string]any)["context_window"] = 128000
	}
	models, _ := json.Marshal(catalog)
	old := []byte("model_context_window = 1050000\nmodel_auto_compact_token_limit = 999999\nmodel_auto_compact_token_limit_scope = 'body_after_prefix'\nprofile = 'work'\n[profiles.work]\nmodel_context_window = 1050000\nmodel_auto_compact_token_limit = 999999\nsandbox_mode = 'workspace-write'\n[profiles.other]\nmodel_context_window = 65536\n")
	merged, err := mergeCodexConfig(old, models, 8877)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := toml.Unmarshal(merged, &config); err != nil {
		t.Fatal(err)
	}
	profiles := config["profiles"].(map[string]any)
	active := profiles["work"].(map[string]any)
	for _, table := range []map[string]any{config, active} {
		for _, key := range []string{"model_context_window", "model_auto_compact_token_limit", "model_auto_compact_token_limit_scope"} {
			if _, exists := table[key]; exists {
				t.Fatalf("global override masks catalog: %s", key)
			}
		}
	}
	if active["sandbox_mode"] != "workspace-write" || profiles["other"].(map[string]any)["model_context_window"] != int64(65536) {
		t.Fatal("unrelated profile settings changed")
	}
}

func TestCodexCatalogRejectsInvalidContextMetadata(t *testing.T) {
	for _, fields := range []string{
		`"context_window": -1`,
		`"context_window": 128000.5`,
		`"context_window": 100000001`,
		`"context_window": 128000, "auto_compact_token_limit": 128000`,
		`"context_window": 128000, "auto_compact_token_limit": -1`,
		`"auto_compact_token_limit": 100000`,
	} {
		data := []byte(`{"models":[{"slug":"fixture/model","supported_reasoning_levels":[],"default_reasoning_level":null,` + fields + `}]}`)
		if _, err := validateCatalog(data); err == nil {
			t.Fatalf("accepted %s", fields)
		}
	}
}
