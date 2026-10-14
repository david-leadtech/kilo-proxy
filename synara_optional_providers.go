package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const synaraOpenCodeProxyID = "kilo_opencode_proxy"
const synaraOpenCodeKey = "KILO_SYNARA_OPENCODE_API_KEY"

func synaraOpenCodeKiloEnabled(prefs synaraPreferences) bool {
	return prefs.OpenCodeConnection == "kilo" && helperContains(prefs.Providers, "opencode")
}

func synaraCapabilityProviders(capabilities map[string]any) []t3CodeProviderSupport {
	providers, _ := capabilities["supportedProviders"].([]t3CodeProviderSupport)
	return providers
}

func (a *app) populateSynaraAdditionalOptions(options *synaraProfileOptions, rt clientLaunchRuntime, binary string) error {
	if options.Preferences == nil {
		return nil
	}
	prefs := normalizeSynaraPreferences(*options.Preferences)
	capabilities, inspectErr := synaraProviderCapabilities(binary, rt.platform)
	if inspectErr != nil && len(prefs.Providers) > 0 {
		return inspectErr
	}
	options.OpenCode2Supported, _ = capabilities["openCode2Supported"].(bool)
	options.OpenCodeKiloSupported, _ = capabilities["openCodeKiloSupported"].(bool)
	options.SupportedProviders = synaraCapabilityProviders(capabilities)
	options.AdditionalSourceDigest, _ = capabilities["sourceDigest"].(string)
	options.NativeDefaults = map[string]string{}
	if defaults, ok := capabilities["nativeDefaults"].(map[string]string); ok {
		for driver, model := range defaults {
			id := driver
			if driver == "codex" {
				id = synaraCodexNormalID
			} else if driver == "claudeAgent" {
				id = synaraClaudeNormalID
			}
			options.NativeDefaults[id] = model
		}
	}
	options.NormalOpenCodePaths = t3CodeOpenCodeNormalPaths(options.RootDir, rt)
	additional, err := synaraDiscoverAdditionalProviders(prefs, capabilities, rt, options.RootDir, false)
	if err != nil {
		return err
	}
	options.AdditionalProviders = additional
	return nil
}

func (a *app) synaraAdditionalAvailability(binary string, rt clientLaunchRuntime, prefs synaraPreferences, verifyVersions bool) error {
	if len(prefs.Providers) == 0 {
		return nil
	}
	capabilities, err := synaraProviderCapabilities(binary, rt.platform)
	if err != nil {
		return err
	}
	_, err = synaraDiscoverAdditionalProviders(prefs, capabilities, rt, synaraPaths(a.dir).Root, verifyVersions)
	return err
}

// Synara resolves its own native account credentials. Only the explicitly
// selected OpenCode executable is resolved here, using Kilo's common managed
// and custom-path preference. Version probes run in disposable state only on
// preparation/launch, never during polling or fingerprint computation.
func synaraDiscoverAdditionalProviders(prefs synaraPreferences, capabilities map[string]any, rt clientLaunchRuntime, root string, verifyVersions bool) (map[string]any, error) {
	prefs = normalizeSynaraPreferences(prefs)
	if err := validateSynaraPreferences(prefs); err != nil {
		return nil, err
	}
	supported := map[string]bool{}
	for _, provider := range synaraCapabilityProviders(capabilities) {
		supported[provider.ID] = true
	}
	for _, id := range prefs.Providers {
		if !supported[id] {
			return nil, errors.New("This Synara installation does not include the selected " + id + " provider. Update Synara before preparing it.")
		}
	}
	result := map[string]any{}
	for _, provider := range synaraCapabilityProviders(capabilities) {
		selected := helperContains(prefs.Providers, provider.ID)
		config := map[string]any{"customModels": []string{}}
		environment := t3CodeOptionalProviderEnvironment(rt.home, os.Environ(), rt.platform)
		switch provider.ID {
		case "grok", "pi":
			config["binaryPath"] = provider.ID
		case "opencode":
			config["binaryPath"], config["serverUrl"], config["serverPassword"] = "opencode", "", ""
			if selected {
				second, _ := capabilities["openCode2Supported"].(bool)
				kilo, _ := capabilities["openCodeKiloSupported"].(bool)
				if prefs.OpenCode2 && !second || synaraOpenCodeKiloEnabled(prefs) && !kilo {
					return nil, errors.New("This Synara installation cannot use the selected OpenCode runtime or isolate its Kilo account. Update Synara or choose its native connection.")
				}
				client, major, minimum := "opencode", "1.", "1.14.19"
				if prefs.OpenCode2 {
					client, major, minimum = "opencode2", "2.", "2.0.18"
					environment = t3CodeOptionalProviderEnvironment(filepath.Join(root, "profiles", "opencode2"), nil, rt.platform)
				}
				if rt.resolve == nil {
					return nil, errors.New("Cannot resolve the selected OpenCode executable.")
				}
				binary, err := rt.resolve(client, "")
				if err != nil || !packageUpdateAbsolute(binary) || strings.ContainsAny(binary, "\x00\r\n") {
					return nil, errors.New("Install OpenCode or choose its executable in Kilo Settings before enabling its Synara account.")
				}
				if verifyVersions {
					version, err := managedToolProbeVersion(context.Background(), binary)
					if err != nil || !strings.HasPrefix(version, major) || strings.ContainsAny(version, "-+") || managedToolCompareNumericVersion(version, minimum) < 0 {
						return nil, errors.New("The selected Synara account requires OpenCode " + minimum + " or newer within major version " + major[:1] + ".")
					}
				}
				config["binaryPath"] = binary
			}
		}
		result[provider.ID] = map[string]any{"driver": provider.ID, "displayName": provider.Label, "enabled": selected && !(provider.ID == "opencode" && synaraOpenCodeKiloEnabled(prefs)), "config": config, "environment": environment}
	}
	return normalizeT3CodeProviders(result), nil
}

func synaraPlanAdditionalProviders(options synaraProfileOptions) (map[string]any, []profileFile, error) {
	result := normalizeT3CodeProviders(options.AdditionalProviders)
	files := []profileFile{}
	if options.Preferences == nil {
		return result, files, nil
	}
	provider, path, data, err := synaraPlanOpenCode(options)
	if err != nil {
		return nil, nil, err
	}
	if provider != nil {
		if path != "" {
			if err := safeEditorDir(options.RootDir, filepath.Dir(path)); err != nil {
				return nil, nil, err
			}
			file, err := prepareProfileFile(path, data)
			if err != nil {
				return nil, nil, err
			}
			files = append(files, file)
		}
		result[synaraOpenCodeProxyID] = provider
	}
	return normalizeT3CodeProviders(result), files, nil
}

// Build public registration and config bytes without opening profiles, creating
// directories or starting a process. Readiness reuses this exact account plan.
func synaraPlanOpenCode(options synaraProfileOptions) (map[string]any, string, []byte, error) {
	if options.Preferences == nil {
		return nil, "", nil, nil
	}
	prefs := normalizeSynaraPreferences(*options.Preferences)
	t3prefs := synaraCoreT3Preferences(prefs)
	t3prefs.Providers, t3prefs.OpenCode2, t3prefs.OpenCodeConnection = prefs.Providers, prefs.OpenCode2, prefs.OpenCodeConnection
	t3options := t3CodeProfileOptions{Preferences: &t3prefs, RootDir: options.RootDir, Platform: options.Platform, NormalOpenCodePaths: options.NormalOpenCodePaths, AdditionalProviders: options.AdditionalProviders, OpenCodeKiloSupported: options.OpenCodeKiloSupported, Library: options.Library, Catalog: options.Catalog, Port: options.Port, LocalKey: options.LocalKey}
	provider, path, data, err := planT3CodeOpenCodeWithKey(t3options, synaraOpenCodeKey)
	if err != nil {
		return nil, "", nil, errors.New(strings.ReplaceAll(strings.ReplaceAll(err.Error(), "T3 Code", "Synara"), "T3 OpenCode", "Synara OpenCode"))
	}
	if provider != nil {
		// The underlying driver config belongs to the separate native slot.
		// Clone it before adding Kilo's catalog; never change that native slot.
		config := map[string]any{}
		for key, value := range provider["config"].(map[string]any) {
			config[key] = value
		}
		provider["config"] = config
		if path != "" {
			picker := t3CodeOpenCodePickerLibrary(options.Library)
			models := []string{}
			for _, model := range picker.Models {
				models = append(models, model.ID)
			}
			config["customModels"] = models
		}
	}
	return provider, path, data, nil
}

func synaraAdditionalFingerprint(options synaraProfileOptions, rt clientLaunchRuntime) (any, error) {
	var err error
	identities := []string{}
	if options.Preferences != nil && synaraOpenCodeKiloEnabled(*options.Preferences) {
		identities, err = t3CodeOpenCodeIsolation(options.RootDir, options.Preferences.OpenCode2, options.NormalOpenCodePaths)
		if err != nil {
			return nil, err
		}
	}
	binaries := map[string]any{}
	for _, raw := range options.AdditionalProviders {
		instance, _ := raw.(map[string]any)
		config, _ := instance["config"].(map[string]any)
		binary, _ := config["binaryPath"].(string)
		if !packageUpdateAbsolute(binary) {
			continue
		}
		canonical, err := filepath.EvalSymlinks(binary)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(canonical)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("The selected Synara provider executable is unavailable.")
		}
		binaries[binary] = []any{canonical, info.Size(), info.ModTime().UnixNano()}
	}
	return []any{options.AdditionalProviders, binaries, identities, options.NativeDefaults, options.AdditionalSourceDigest, options.OpenCode2Supported, options.OpenCodeKiloSupported}, nil
}

func validateSynaraAdditionalManifest(saved synaraPrepared) error {
	if saved.Preferences == nil {
		return nil
	}
	prefs := *saved.Preferences
	expected := map[string]bool{synaraCodexNormalID: true, synaraClaudeNormalID: true, synaraCodexProxyID: true, synaraClaudeProxyID: true}
	for _, provider := range saved.SupportedProviders {
		if expected[provider.ID] || !helperContains([]string{"cursor", "grok", "opencode", "antigravity", "pi"}, provider.ID) {
			return errors.New("Invalid private Synara provider manifest.")
		}
		expected[provider.ID] = true
		instance, ok := saved.Providers[provider.ID].(map[string]any)
		enabled := helperContains(prefs.Providers, provider.ID) && !(provider.ID == "opencode" && synaraOpenCodeKiloEnabled(prefs))
		if !ok || instance["driver"] != provider.ID || instance["enabled"] != enabled {
			return errors.New("Invalid private Synara optional account selection.")
		}
	}
	if saved.OpenCodeKiloSupported {
		expected[synaraOpenCodeProxyID] = true
		instance, ok := saved.Providers[synaraOpenCodeProxyID].(map[string]any)
		if !ok || instance["driver"] != "opencode" || instance["enabled"] != synaraOpenCodeKiloEnabled(prefs) {
			return errors.New("Invalid private Synara OpenCode account selection.")
		}
	}
	if len(expected) != len(saved.Providers) {
		return errors.New("Invalid private Synara provider manifest.")
	}
	for id := range saved.Providers {
		if !expected[id] {
			return errors.New("Invalid private Synara provider manifest.")
		}
	}
	root := ""
	// Manifest paths are relative to the same private Synara root on every OS.
	for _, second := range []bool{false, true} {
		_, config := t3CodeOpenCodePaths(root, second)
		_, present := saved.Files[config]
		if present != (synaraOpenCodeKiloEnabled(prefs) && prefs.OpenCode2 == second) {
			return errors.New("Prepare the selected Synara OpenCode account again.")
		}
	}
	secret := filepath.Join("data", "userdata", "secrets", t3CodeSecretName(synaraOpenCodeProxyID, synaraOpenCodeKey))
	_, present := saved.Files[secret]
	if present != synaraOpenCodeKiloEnabled(prefs) {
		return errors.New("Prepare the selected Synara OpenCode account again.")
	}
	if synaraOpenCodeKiloEnabled(prefs) && (!saved.OpenCodeKiloSupported || prefs.OpenCode2 && !saved.OpenCode2Supported || len(saved.OpenCodeLibrary.Models) == 0) {
		return errors.New("Invalid private Synara OpenCode runtime selection.")
	}
	return nil
}
