package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSynaraOptionalReadinessIsPureAndBindsExactRegistrations(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[second], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "future-owned-profile")
			prefs := synaraExplicitPreferencesFixture()
			prefs.Codex = false
			prefs.Claude = false
			prefs.KiloCodex = false
			prefs.KiloClaude = false
			prefs.Providers = []string{"opencode"}
			prefs.OpenCode2 = second
			prefs.OpenCodeConnection = "kilo"
			library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/exact", Models: []modelLibraryItem{{ID: "vendor/exact"}}}
			native := map[string]any{"driver": "opencode", "displayName": "OpenCode", "enabled": false, "config": map[string]any{"binaryPath": filepath.Join(root, "selected-opencode"), "customModels": []string{}}, "environment": []any{}}
			options := synaraProfileOptions{RootDir: root, Platform: "macos", Preferences: &prefs, Library: library, Port: 9988, LocalKey: "synthetic-key", OpenCodeKiloSupported: true, AdditionalProviders: map[string]any{"opencode": native}}
			provider, _, _, err := synaraPlanOpenCode(options)
			if err != nil {
				t.Fatal(err)
			}
			actual := map[string]any{}
			for _, id := range []string{synaraCodexNormalID, synaraCodexProxyID, synaraClaudeNormalID, synaraClaudeProxyID} {
				actual[id] = map[string]any{}
			}
			actual["opencode"] = redactT3CodeProvider(native)
			actual[synaraOpenCodeProxyID] = redactT3CodeProvider(provider)
			if !synaraAdditionalProvidersReady(options, actual) {
				t.Fatal("valid generated registrations are not ready")
			}
			if _, err = os.Stat(root); !os.IsNotExist(err) {
				t.Fatal("readiness created profile directories")
			}
			for _, name := range []string{"native-path", "kilo-path", "kilo-environment", "unknown-account"} {
				t.Run(name, func(t *testing.T) {
					copy := normalizeT3CodeProviders(actual)
					switch name {
					case "native-path":
						copy["opencode"].(map[string]any)["config"].(map[string]any)["binaryPath"] = filepath.Join(root, "other-native")
					case "kilo-path":
						copy[synaraOpenCodeProxyID].(map[string]any)["config"].(map[string]any)["binaryPath"] = filepath.Join(root, "other-kilo")
					case "kilo-environment":
						copy[synaraOpenCodeProxyID].(map[string]any)["environment"] = []any{}
					case "unknown-account":
						copy["muse"] = map[string]any{"driver": "muse", "enabled": false}
					}
					if synaraAdditionalProvidersReady(options, copy) {
						t.Fatal("changed provider registration accepted")
					}
					if _, err = os.Stat(root); !os.IsNotExist(err) {
						t.Fatal("failed readiness wrote profiles")
					}
				})
			}
		})
	}
}
