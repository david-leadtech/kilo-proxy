package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSynaraOptionalNativeDefaultSelectionUsesImportedEnabledAccount(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	for _, driver := range []string{"cursor", "grok", "opencode", "antigravity", "pi"} {
		t.Run(driver, func(t *testing.T) {
			prefs.Providers = []string{driver}
			model := driver + "/exact-installed-default"
			options := synaraProfileOptions{Preferences: &prefs, NativeDefaults: map[string]string{driver: model}, Policy: &t3CodeModelPolicy{}}
			providers := map[string]any{driver: map[string]any{"driver": driver, "enabled": true}}
			want := map[string]any{"provider": driver, "instanceId": driver, "model": model}
			if got := synaraDefaultSelection(options, providers, nil); !reflect.DeepEqual(got, want) {
				t.Fatalf("enabled optional native account lost its imported default: got=%v want=%v", got, want)
			}
			// An existing native choice and its options remain the user's choice.
			current := map[string]any{"provider": driver, "instanceId": driver, "model": driver + "/user-choice", "options": map[string]any{"variant": "selected-by-user"}}
			raw, _ := json.Marshal(current)
			if got := synaraDefaultSelection(options, providers, raw); !reflect.DeepEqual(got, current) {
				t.Fatal("valid native account selection changed", got)
			}
		})
	}
}

func TestSynaraOptionalNativeDefaultSelectionDoesNotInventOrEnableAccounts(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	prefs.Providers = []string{"cursor", "opencode"}
	options := synaraProfileOptions{Preferences: &prefs, NativeDefaults: map[string]string{"cursor": "cursor/imported", "opencode": "opencode/imported"}, Policy: &t3CodeModelPolicy{}}
	providers := map[string]any{
		"opencode": map[string]any{"driver": "opencode", "enabled": true},
		"cursor":   map[string]any{"driver": "cursor", "enabled": true},
	}
	want := map[string]any{"provider": "cursor", "instanceId": "cursor", "model": "cursor/imported"}
	if got := synaraDefaultSelection(options, providers, nil); !reflect.DeepEqual(got, want) {
		t.Fatal("native fallback order is not deterministic", got)
	}
	providers["cursor"].(map[string]any)["enabled"] = false
	want = map[string]any{"provider": "opencode", "instanceId": "opencode", "model": "opencode/imported"}
	if got := synaraDefaultSelection(options, providers, nil); !reflect.DeepEqual(got, want) {
		t.Fatal("disabled native account was selected", got)
	}
	delete(options.NativeDefaults, "opencode")
	if got := synaraDefaultSelection(options, providers, nil); got != nil {
		t.Fatal("unknown optional model acquired an invented default", got)
	}
	providers["opencode"].(map[string]any)["enabled"] = false
	options.NativeDefaults["opencode"] = "opencode/imported"
	if got := synaraDefaultSelection(options, providers, nil); got != nil {
		t.Fatal("all-disabled accounts acquired a default", got)
	}
}
