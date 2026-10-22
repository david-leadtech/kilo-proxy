package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSynaraPolicyDelegationUsesAccountLibrariesAndNativeClaudeEffort(t *testing.T) {
	options := synaraProfileTestOptions(t)
	prefs := defaultSynaraPreferences()
	prefs.KiloCodex = false
	prefs.Providers, prefs.OpenCodeConnection = []string{"opencode"}, "kilo"
	options.Preferences = &prefs
	opus := "anthropic/claude-opus-5.5-20261014"
	sonnet := "anthropic/claude-sonnet-5.5"
	fable := "anthropic/claude-fable-5.1"
	options.Library = modelLibrary{SchemaVersion: 1, DefaultModel: opus, Models: []modelLibraryItem{
		{ID: opus, ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "high"},
		{ID: sonnet, ReasoningCustom: true, ReasoningLevels: []string{}},
		{ID: fable, ReasoningCustom: true, ReasoningLevels: []string{"high"}},
	}}
	options.Catalog = []modelInfo{{ID: opus}, {ID: sonnet}, {ID: fable}}
	options.ClaudeCaps = claudeCaps("2.1.296")
	policy, err := deriveSynaraModelPolicy(prefs, options.Library, options.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	options.Policy = &policy
	models, err := synaraDelegationModels(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(models["codex"].([]any)) != 0 || len(models["claudeAgent"].([]any)) != 3 {
		t.Fatal("disabled Codex or curated Claude scope changed", models)
	}
	if models["defaultModels"].(map[string]string)["claudeAgent"] != "claude-opus-5-5" {
		t.Fatal("dated gateway lost its native default")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node verifies the actual private Synara delegation helpers")
	}
	data, _ := json.Marshal(map[string]any{"models": models, "helpers": synaraDelegationHelpers})
	path := filepath.Join(t.TempDir(), "policy.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, "scripts/verify_synara_policy_delegation.mjs", path).CombinedOutput()
	if err != nil {
		t.Fatalf("policy delegation: %v\n%s", err, output)
	}
	// A native-only profile must not acquire the upstream Kilo fallback catalog.
	empty := t3CodeModelPolicy{CodexLibrary: emptyModelLibrary(), ClaudeLibrary: emptyModelLibrary()}
	options.Policy = &empty
	if models, err = synaraDelegationModels(options); err != nil || len(models["codex"].([]any)) != 0 || len(models["claudeAgent"].([]any)) != 0 {
		t.Fatal("empty disabled Kilo accounts gained models", models, err)
	}
}
