package main

import "testing"

func TestSynaraRuntimeOpenCodeOnlyUsesSharedPickerPolicy(t *testing.T) {
	options := synaraProfileTestOptions(t)
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	prefs.Providers, prefs.OpenCodeConnection = []string{"opencode"}, "kilo"
	options.Preferences = &prefs
	options.Library = modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/exact", Models: []modelLibraryItem{
		{ID: "vendor/exact", ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "high"},
		{ID: "vendor/no-reasoning", ReasoningCustom: true, ReasoningLevels: []string{}},
	}}
	policy, err := deriveSynaraModelPolicy(prefs, options.Library, nil)
	if err != nil {
		t.Fatal(err)
	}
	options.Policy = &policy
	web := synaraRuntimePickerOptions(options)
	if !synaraPrivateWebPolicyEnabled(web.Policy) || web.Policy.InstanceID != "" || len(web.Policy.Models) != 0 || len(web.Policy.OpenCodeModels) != 2 {
		t.Fatal("OpenCode-only profile lost its private UI policy", web.Policy)
	}
	if err := validateSynaraPickerPolicy(web.Policy); err != nil {
		t.Fatal(err)
	}
	if row := web.Policy.OpenCodeModels[0]; row.Slug != "kilo-local/vendor/exact" || row.GatewayID != "vendor/exact" || row.DefaultVariant != "high" || len(row.VariantLevels) != 2 {
		t.Fatal("exact routing or saved variant changed", row)
	}
	if row := web.Policy.OpenCodeModels[1]; len(row.VariantLevels) != 0 || row.DefaultVariant != "" {
		t.Fatal("explicit empty levels gained a native fallback", row)
	}
	for _, platform := range []string{"linux", "windows"} {
		if saved, err := prepareSynaraPrivateRuntime(options, "unused", platform); err == nil || saved.Root != "" {
			t.Fatal("unsupported strict OpenCode picker was prepared", platform)
		}
		if synaraPrivateRuntimeReady(options, "unused", platform, synaraPrivateRuntime{}) {
			t.Fatal("unsupported strict OpenCode picker was reported ready", platform)
		}
	}
}
