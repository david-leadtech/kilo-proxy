package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Native-account tests supply the same imported/exported default contract as
// the installed package. These are metadata only, not account entitlements.
func addSynaraNativeDefaultsFixture(t *testing.T, binary, platform string) {
	t.Helper()
	archive, err := t3CodeOpenPackage(binary, platform)
	if err != nil {
		t.Fatal(err)
	}
	path := archive.file.Name()
	files := map[string][]byte{}
	var copyEntries func(t3CodeASAREntry, string)
	copyEntries = func(entry t3CodeASAREntry, prefix string) {
		for name, child := range entry.Files {
			name = prefix + name
			if child.Files != nil {
				copyEntries(child, name+"/")
				continue
			}
			if child.Size == 0 {
				files[name] = []byte{}
				continue
			}
			data, err := archive.read(name, synaraRuntimeSourceLimit)
			if err != nil {
				archive.file.Close()
				t.Fatal(err)
			}
			files[name] = data
		}
	}
	copyEntries(archive.tree, "")
	archive.file.Close()
	const imported = `import { is as DEFAULT_MODEL_BY_PROVIDER } from "./MigrationBackup-nativeFixture.mjs";`
	files["apps/server/dist/index.mjs"] = append(files["apps/server/dist/index.mjs"], []byte("\n"+imported+"\n"+synaraNativeDefaultsConsumer+"\n")...)
	files["apps/server/dist/MigrationBackup-nativeFixture.mjs"] = []byte(`const DEFAULT_MODEL_BY_PROVIDER = {codex: "gpt-6-astra", claudeAgent: "claude-sonnet-5", cursor: "auto", devin: "adaptive", antigravity: "Gemini 3.5 Flash", grok: "grok-4.6", droid: "claude-opus-4-8", opencode: "openai/gpt-5"}; export { DEFAULT_MODEL_BY_PROVIDER as is };`)
	writeT3CodeFixtureASAR(t, path, files)
}

func synaraTextSelectionSettings(t *testing.T, instances map[string]any) map[string]json.RawMessage {
	t.Helper()
	legacy := map[string]any{}
	for _, driver := range synaraDrivers() {
		legacy[driver] = map[string]any{"enabled": false}
	}
	legacy["codex"].(map[string]any)["accounts"] = []any{}
	settings := map[string]json.RawMessage{}
	claudeDesktopSet(settings, "providers", legacy)
	claudeDesktopSet(settings, "providerInstances", instances)
	return settings
}

func TestSynaraTextSelectionAllOffUsesDisabledDormantAccount(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	options := synaraProfileOptions{Preferences: &prefs, Policy: &t3CodeModelPolicy{}}
	providers := map[string]any{synaraCodexProxyID: map[string]any{"driver": "codex", "enabled": false, "config": map[string]any{"enabled": false}}}
	settings := synaraTextSelectionSettings(t, providers)
	want := map[string]any{"provider": "codex", "instanceId": synaraCodexProxyID, "model": "default"}
	got, err := synaraSettingsSelectionChecked(options, providers, settings)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("all-off selection is not a schema-valid disabled account", got, err)
	}
	claudeDesktopSet(settings, "textGenerationModelSelection", got)
	if err := validateSynaraSettings(settings); err != nil {
		t.Fatal("valid dormant selection was rejected", err)
	}
	again, err := synaraSettingsSelectionChecked(options, providers, settings)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("dormant selection was not stable", again, err)
	}
	settings["textGenerationModelSelection"] = json.RawMessage("null")
	if err := validateSynaraSettings(settings); err == nil {
		t.Fatal("upstream-invalid null selection was accepted")
	}
	claudeDesktopSet(settings, "textGenerationModelSelection", map[string]any{"provider": "codex", "instanceId": synaraCodexProxyID, "model": "   "})
	if err := validateSynaraSettings(settings); err == nil {
		t.Fatal("upstream-invalid empty model was accepted")
	}
}

func TestSynaraTextSelectionPreparedAllOffIsNonNullAndReady(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	a.config.Synara = prefs
	if w := adminRequest(a, "clients/synara", `{}`); w.Code != 200 {
		t.Fatal("all-off preparation failed", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(synaraPaths(a.dir).Data, "userdata", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, settings, err := synaraSettingsDocument(data)
	var selection map[string]any
	if err != nil || json.Unmarshal(settings["textGenerationModelSelection"], &selection) != nil || selection["instanceId"] != synaraCodexProxyID || selection["model"] != "default" {
		t.Fatal("prepared all-off settings lost the disabled non-null selection", selection, err)
	}
	saved, err := a.readSynaraPrepared()
	if err != nil || !a.synaraReady(saved, mustSynaraFixtureBinary(t, a), a.launchRuntime()) {
		t.Fatal("all-off settings are not ready", err)
	}
}

func TestSynaraTextSelectionUnknownEnabledNativeAccountRejectsBeforeSaving(t *testing.T) {
	a := synaraTestApp(t, "macos")
	prefs := defaultSynaraPreferences()
	prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false
	a.config.Synara = prefs
	binary := mustSynaraFixtureBinary(t, a)
	archive, err := t3CodeOpenPackage(binary, a.launcher.platform)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := archive.read("package.json", 64<<10)
	path := archive.file.Name()
	archive.file.Close()
	if err != nil {
		t.Fatal(err)
	}
	// An otherwise recognized package has no verified native default source.
	writeT3CodeFixtureASAR(t, path, map[string][]byte{"package.json": metadata})
	w := adminRequest(a, "clients/synara", `{}`)
	if w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("Choose a verified model")) {
		t.Fatal("unknown native model did not fail closed", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(synaraPaths(a.dir).Data, "userdata", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("failed native preparation wrote settings", err)
	}
}

func TestSynaraTextSelectionRejectsUnverifiedDedicatedFallback(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	options := synaraProfileOptions{Preferences: &prefs, Policy: &t3CodeModelPolicy{}}
	for _, driver := range []string{"codex", "claudeAgent", "cursor", "opencode", "droid", "future-driver"} {
		t.Run(driver, func(t *testing.T) {
			providers := map[string]any{
				synaraCodexProxyID: map[string]any{"driver": "codex", "enabled": false, "config": map[string]any{"enabled": false}},
				"retained-foreign": map[string]any{"driver": driver, "enabled": true, "config": map[string]any{}},
			}
			settings := synaraTextSelectionSettings(t, providers)
			if got, err := synaraSettingsSelectionChecked(options, providers, settings); err == nil || got != nil {
				t.Fatal("unknown native/retained fallback acquired an invented active model", got, err)
			}
			providers["retained-foreign"].(map[string]any)["config"].(map[string]any)["enabled"] = false
			settings = synaraTextSelectionSettings(t, providers)
			if got, err := synaraSettingsSelectionChecked(options, providers, settings); err != nil || got == nil {
				t.Fatal("effectively disabled retained account blocked dormant selection", got, err)
			}
		})
	}
}

func TestSynaraTextSelectionKeepsVerifiedNativeChoicesAndDefaults(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	options := synaraProfileOptions{Preferences: &prefs, Policy: &t3CodeModelPolicy{}}
	for _, driver := range []string{"grok", "antigravity", "pi", "devin", "omp"} {
		providers := map[string]any{
			synaraCodexProxyID: map[string]any{"driver": "codex", "enabled": false, "config": map[string]any{"enabled": false}},
			driver:             map[string]any{"driver": driver, "enabled": true, "config": map[string]any{"enabled": true}},
		}
		settings := synaraTextSelectionSettings(t, providers)
		got, err := synaraSettingsSelectionChecked(options, providers, settings)
		if err != nil || got.(map[string]any)["instanceId"] != synaraCodexProxyID {
			t.Fatal("chat-only account without a model acquired an invented active model", driver, got, err)
		}
	}
	providers := map[string]any{synaraCodexNormalID: map[string]any{"driver": "codex", "enabled": true, "config": map[string]any{"enabled": true}}}
	settings := synaraTextSelectionSettings(t, providers)
	choice := map[string]any{"provider": "codex", "instanceId": synaraCodexNormalID, "model": "native-user-choice", "options": map[string]any{"reasoningEffort": "high"}}
	claudeDesktopSet(settings, "textGenerationModelSelection", choice)
	if got, err := synaraSettingsSelectionChecked(options, providers, settings); err != nil || !reflect.DeepEqual(got, choice) {
		t.Fatal("existing enabled native choice changed", got, err)
	}
	delete(settings, "textGenerationModelSelection")
	options.NativeDefaults = map[string]string{synaraCodexNormalID: "exact-imported-default"}
	if got, err := synaraSettingsSelectionChecked(options, providers, settings); err != nil || got.(map[string]any)["model"] != "exact-imported-default" {
		t.Fatal("verified installed native default was not bound", got, err)
	}
}

func TestSynaraTextSelectionMatchesInheritedBuiltinEnabledFlags(t *testing.T) {
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	options := synaraProfileOptions{Preferences: &prefs, Policy: &t3CodeModelPolicy{}}
	providers := map[string]any{
		synaraCodexProxyID: map[string]any{"driver": "codex", "enabled": false, "config": map[string]any{"enabled": false}},
		"cursor":           map[string]any{"driver": "cursor", "enabled": true},
	}
	settings := synaraTextSelectionSettings(t, providers)
	if _, err := synaraSettingsSelectionChecked(options, providers, settings); err != nil {
		t.Fatal("missing built-in config.enabled did not inherit its disabled legacy flag", err)
	}
	providers["cursor"].(map[string]any)["config"] = map[string]any{"enabled": true}
	settings = synaraTextSelectionSettings(t, providers)
	if _, err := synaraSettingsSelectionChecked(options, providers, settings); err == nil {
		t.Fatal("explicit built-in config.enabled did not override disabled legacy flag")
	}
	providers["cursor"].(map[string]any)["driver"] = "grok"
	settings = synaraTextSelectionSettings(t, providers)
	if _, err := synaraSettingsSelectionChecked(options, providers, settings); err != nil {
		t.Fatal("retargeted canonical id was not ignored like upstream", err)
	}
}

func TestSynaraTextSelectionPrepareRejectionPreservesSettings(t *testing.T) {
	options := synaraProfileTestOptions(t)
	prefs := defaultSynaraPreferences()
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	options.Preferences, options.Policy = &prefs, &t3CodeModelPolicy{}
	providers := map[string]any{synaraCodexProxyID: map[string]any{"driver": "codex", "enabled": false, "config": map[string]any{"enabled": false}, "environment": []any{}}}
	path := filepath.Join(options.DataDir, "userdata", "settings.json")
	original := []byte(`{"revision":7,"settings":{"providerInstances":{"retained-foreign":{"driver":"codex","enabled":true,"config":{"enabled":true}}}}}`)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := planSynaraSettings(options, options.DataDir, providers); err == nil {
		t.Fatal("unverified foreign fallback did not reject planning")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, original) {
		t.Fatal("rejected preparation changed saved settings", err)
	}
}
