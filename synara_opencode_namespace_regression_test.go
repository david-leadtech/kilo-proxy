package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSynaraOpenCodeNamespaceKeepsLiteralGatewayModelIDs(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "v2"}[second], func(t *testing.T) {
			root := t.TempDir()
			prefs := synaraExplicitPreferencesFixture()
			prefs.Codex = false
			prefs.Claude = false
			prefs.KiloCodex = false
			prefs.KiloClaude = false
			prefs.Providers = []string{"opencode"}
			prefs.OpenCode2 = second
			prefs.OpenCodeConnection = "kilo"
			id := "vendor/" + t3CodeOpenCodeKey
			library := modelLibrary{SchemaVersion: 1, DefaultModel: id, Models: []modelLibraryItem{{ID: id}}}
			native := map[string]any{"driver": "opencode", "displayName": "OpenCode", "enabled": false, "config": map[string]any{"binaryPath": filepath.Join(root, "synthetic-opencode")}, "environment": []any{}}
			options := synaraProfileOptions{RootDir: root, Platform: "macos", Preferences: &prefs, Library: library, Port: 9988, LocalKey: "synthetic-scoped-key", OpenCodeKiloSupported: true, AdditionalProviders: map[string]any{"opencode": native}}
			providers, files, err := synaraPlanAdditionalProviders(options)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(providers["opencode"], normalizeT3CodeProviders(map[string]any{"opencode": native})["opencode"]) {
				t.Fatal("Kilo model augmentation changed native OpenCode registration")
			}
			if len(files) != 1 {
				t.Fatalf("wrong config files: %d", len(files))
			}
			var config map[string]any
			if json.Unmarshal(files[0].new, &config) != nil {
				t.Fatal("invalid generated config")
			}
			if !bytes.Contains(files[0].new, []byte(id)) {
				t.Fatalf("literal gateway model ID changed during secret namespace conversion: %s", files[0].new)
			}
			if !bytes.Contains(files[0].new, []byte("{env:"+synaraOpenCodeKey+"}")) {
				t.Fatal("Synara config does not use its own environment key")
			}
			provider := providers[synaraOpenCodeProxyID].(map[string]any)
			found := false
			for _, raw := range provider["environment"].([]any) {
				variable := raw.(map[string]any)
				if variable["name"] == synaraOpenCodeKey {
					found = true
				}
				if variable["name"] == t3CodeOpenCodeKey {
					t.Fatal("Synara retains T3 environment key")
				}
			}
			if !found {
				t.Fatal("missing Synara secret environment registration")
			}
			if _, err = os.Stat(filepath.Join(root, "t3-code")); !os.IsNotExist(err) {
				t.Fatal("Synara modified a T3 workspace")
			}
		})
	}
}
