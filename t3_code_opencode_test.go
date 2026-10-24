package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func t3CodeOpenCodeTestApp(t *testing.T, second bool) (*app, string, string) {
	t.Helper()
	a := t3CodeTestApp(t, runtime.GOOS)
	a.config.T3Code.Codex, a.config.T3Code.Claude = false, false
	a.config.T3Code.KiloCodex, a.config.T3Code.KiloClaude = false, false
	a.config.T3Code.Providers = []string{"opencode"}
	a.config.T3Code.OpenCodeConnection, a.config.T3Code.OpenCode2 = "kilo", second
	resolve := a.launcher.resolve
	binary, err := resolve("t3-code", "")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := t3CodeOpenPackage(binary, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs", "apps/server/dist/client/index.html", "apps/server/dist/client/assets/settings.providers-Fixture.js", "apps/server/dist/client/assets/utils-Fixture.js"} {
		files[name], err = archive.read(name, 2<<20)
		if err != nil {
			t.Fatal(err)
		}
	}
	archivePath := archive.file.Name()
	archive.file.Close()
	files["apps/server/dist/bin.mjs"] = append(files["apps/server/dist/bin.mjs"], []byte("\n"+t3CodeOpenCodeKiloContractFixture()+`classifyOpenCodeCliVersion selectOpenCodeRuntimeAdapter selectOpenCodeRuntimeTextGeneration decodeApiInfo "/api/info" "@opencode/cli"`)...)
	writeT3CodeFixtureASAR(t, archivePath, files)
	version, client := "1.19.0", "opencode"
	if second {
		version, client = "2.0.27", "opencode2"
	}
	cli := filepath.Join(t.TempDir(), "OpenCode custom")
	marker := filepath.Join(filepath.Dir(cli), "probe-count")
	script := "#!/bin/sh\n[ \"$1\" = --version ] || exit 23\nprintf 'probe\\n' >> " + helperShellQuote(marker) + "\nprintf 'opencode v" + version + "\\n'\n"
	if runtime.GOOS == "windows" {
		cli += ".cmd"
		script = "@echo off\r\nif not \"%~1\"==\"--version\" exit /b 23\r\necho probe>>\"%~dp0probe-count\"\r\necho opencode v" + version + "\r\n"
	}
	if err := os.WriteFile(cli, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.launcher.resolve = func(id, custom string) (string, error) {
		if id == client {
			return cli, nil
		}
		if id == "codex-cli" || id == "claude" {
			t.Fatal("disabled Codex/Claude executable was resolved", id)
		}
		return resolve(id, custom)
	}
	t.Cleanup(a.requestQuit)
	return a, binary, marker
}

func TestT3CodeOpenCodeConnectionPreferencesCompatibilityAndStrictJSON(t *testing.T) {
	a := t3CodeTestApp(t, runtime.GOOS)
	prefs := normalizeT3CodePreferences(a.config.T3Code)
	prefs.Providers, prefs.OpenCodeConnection = []string{"opencode"}, "kilo"
	data, _ := json.Marshal(prefs)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	legacy := map[string]any{}
	json.Unmarshal(data, &legacy)
	delete(legacy, "openCodeConnection")
	legacy["openCode2"] = true
	data, _ = json.Marshal(legacy)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 || a.config.T3Code.OpenCodeConnection != "kilo" || !a.config.T3Code.OpenCode2 {
		t.Fatal("v1 omission reset the selected connection", response.Code, response.Body.String())
	}
	previous := a.config.T3Code
	for _, change := range []map[string]any{{"openCodeConnetion": "native"}, {"openCodeConnection": nil}, {"openCodeConnection": "guess"}} {
		input := map[string]any{}
		json.Unmarshal(data, &input)
		for key, value := range change {
			input[key] = value
		}
		invalid, _ := json.Marshal(input)
		response := adminRequest(a, "clients/t3-code/preferences", string(invalid))
		if response.Code != 400 || !reflect.DeepEqual(previous, a.config.T3Code) {
			t.Fatal("invalid or unknown JSON changed preferences", response.Code, response.Body.String())
		}
	}
	legacy["openCodeConnection"] = ""
	data, _ = json.Marshal(legacy)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 || a.config.T3Code.OpenCodeConnection != "native" {
		t.Fatal("explicit empty did not choose native", response.Code, response.Body.String())
	}
}

func TestT3CodeOpenCodeKiloProfilesExactModelsSecretsReadinessAndTamper(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(fmt.Sprint(second), func(t *testing.T) {
			a, binary, marker := t3CodeOpenCodeTestApp(t, second)
			// Unrelated disabled CLI overrides must not block the OpenCode account.
			t.Setenv("CODEX_HOME", "relative-codex")
			t.Setenv("CLAUDE_CONFIG_DIR", "relative-claude")
			t.Setenv("OPENCODE_CONFIG_CONTENT", `{"provider":{"wrong":{"options":{"apiKey":"hostile"}}}}`)
			t.Setenv("OPENCODE_CONFIG", filepath.Join(t.TempDir(), "unrelated-terminal-profile"))
			t.Setenv("KILO_OPENCODE2_API_KEY", "ambient-key")
			prepareT3CodeFixture(t, a)
			saved, err := a.readT3CodePrepared()
			if err != nil || len(saved.Files) != 2 || !reflect.DeepEqual(saved.OpenCodeLibrary, saved.Library) || !a.t3CodeReady(saved, binary, *a.launcher) {
				t.Fatal("OpenCode-only preparation is not ready", saved, err)
			}
			if !reflect.DeepEqual(saved.Runtime.OpenCodeVariants, map[string][]string{"kilo-local/vendor/one": {"low", "high"}, "kilo-local/vendor/two": {}}) {
				t.Fatal("private runtime did not retain the selected native reasoning levels", saved.Runtime.OpenCodeVariants)
			}
			before, _ := os.ReadFile(marker)
			if fingerprint := a.t3CodeFingerprint(binary, saved.Version, saved.Library, *a.launcher); fingerprint == "" || fingerprint != saved.Fingerprint {
				t.Fatal("unstable OpenCode fingerprint")
			}
			after, _ := os.ReadFile(marker)
			if !bytes.Equal(before, after) {
				t.Fatal("readiness or fingerprint spawned OpenCode")
			}
			paths := t3CodePaths(a.dir)
			settings, _ := os.ReadFile(paths.Settings)
			selection, _ := os.ReadFile(paths.Selection)
			_, configPath := t3CodeOpenCodePaths(paths.Root, second)
			config, _ := os.ReadFile(configPath)
			var nativeConfig map[string]any
			if json.Unmarshal(config, &nativeConfig) != nil || nativeConfig["model"] != "kilo-local/vendor/one" {
				t.Fatal("OpenCode default model is not the exact shared gateway selection")
			}
			providerField := "provider"
			if second {
				providerField = "providers"
			}
			localProvider := nativeConfig[providerField].(map[string]any)["kilo-local"].(map[string]any)
			models := localProvider["models"].(map[string]any)
			if len(models) != 2 || models["vendor/one"] == nil || models["vendor/two"] == nil {
				t.Fatal("OpenCode profile changed the exact shared model IDs", models)
			}
			one := models["vendor/one"].(map[string]any)
			limits := one["limit"].(map[string]any)
			if limits["context"] != float64(200000) || limits["output"] != float64(32000) || second && one["modelID"] != "vendor/one" {
				t.Fatal("OpenCode profile changed explicit shared context/output/model identity", one)
			}
			if !second {
				variants := one["variants"].(map[string]any)
				high := variants["high"].(map[string]any)
				if len(variants) != 2 || high["model"] != "vendor/one" || high["reasoningEffort"] != "high" || high["baseURL"] != nil || high["apiKey"] != nil || high["headers"] != nil {
					t.Fatal("OpenCode 1 declared variant leaked provider options")
				}
				options := localProvider["options"].(map[string]any)
				if options["baseURL"] != "http://127.0.0.1:9988/v1" || options["apiKey"] != "{env:"+t3CodeOpenCodeKey+"}" || len(options) != 2 {
					t.Fatal("OpenCode 1 provider options lost the routed credentials")
				}
			}
			for _, data := range [][]byte{settings, selection, config} {
				if bytes.Contains(data, []byte(a.config.LocalKey)) || bytes.Contains(data, []byte("ambient-key")) || bytes.Contains(data, []byte("hostile")) {
					t.Fatal("OpenCode public settings leaked or inherited a credential")
				}
			}
			secret, err := os.ReadFile(filepath.Join(paths.Secrets, t3CodeSecretName(t3CodeOpenCodeProxyID, t3CodeOpenCodeKey)))
			if err != nil || string(secret) != a.config.LocalKey {
				t.Fatal("OpenCode secret store lost the local key", err)
			}
			var document map[string]any
			json.Unmarshal(settings, &document)
			instances := document["providerInstances"].(map[string]any)
			if instances["opencode"].(map[string]any)["enabled"] != false || instances[t3CodeOpenCodeProxyID].(map[string]any)["enabled"] != true {
				t.Fatal("native and Kilo accounts were simultaneously enabled")
			}
			childEnvironment := map[string]string{}
			for _, raw := range instances[t3CodeOpenCodeProxyID].(map[string]any)["environment"].([]any) {
				variable := raw.(map[string]any)
				childEnvironment[variable["name"].(string)], _ = variable["value"].(string)
			}
			home, _ := t3CodeOpenCodePaths(paths.Root, second)
			if childEnvironment["HOME"] != home || home == a.launcher.home || childEnvironment["OPENCODE_CONFIG"] != configPath || childEnvironment["OPENCODE_CONFIG_CONTENT"] != string(config) {
				t.Fatal("T3 OpenCode did not use its own exact profile and explicit inline override", childEnvironment)
			}
			for _, name := range []string{"OPENCODE_CONFIG_PROJECT_DISABLE", "OPENCODE_DISABLE_PROJECT_CONFIG"} {
				if childEnvironment[name] != "" {
					t.Fatal("Kilo OpenCode disabled project AGENTS discovery", name)
				}
			}
			chosen := document["defaultModelSelection"].(map[string]any)
			if chosen["instanceId"] != t3CodeOpenCodeProxyID || chosen["model"] != "kilo-local/"+saved.Library.DefaultModel {
				t.Fatal("OpenCode-only default picked a disabled account or rewrote the gateway ID", chosen)
			}
			if response := adminRequest(a, "clients/t3-code", ""); response.Code != 200 || !strings.Contains(response.Body.String(), `"effectiveOpenCodeModels":[`) || strings.Contains(response.Body.String(), a.config.LocalKey) {
				t.Fatal(response.Code, response.Body.String())
			}
			brokenRuntime := saved
			brokenRuntime.Runtime.OpenCodeVariants = map[string][]string{"kilo-local/vendor/one": {"low"}, "kilo-local/vendor/two": {}}
			brokenData, _ := json.Marshal(brokenRuntime)
			if err := os.WriteFile(paths.Selection, brokenData, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.readT3CodePrepared(); err == nil {
				t.Fatal("a changed variant policy retained the private interface identity")
			}
			if err := os.WriteFile(paths.Selection, selection, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, append(config, ' '), 0600); err != nil {
				t.Fatal(err)
			}
			if a.t3CodeReady(saved, binary, *a.launcher) {
				t.Fatal("tampered OpenCode configuration stayed ready")
			}
			if err := os.WriteFile(configPath, config, 0600); err != nil {
				t.Fatal(err)
			}
			for relative := range saved.Files {
				broken := saved
				broken.Files = map[string]string{}
				for path, digest := range saved.Files {
					if path != relative {
						broken.Files[path] = digest
					}
				}
				data, _ := json.Marshal(broken)
				os.WriteFile(paths.Selection, data, 0600)
				if _, err := a.readT3CodePrepared(); err == nil {
					t.Fatal("missing active OpenCode configuration/secret was accepted")
				}
			}
		})
	}
}

func TestT3CodeOpenCodeKiloRejectsNormalAccountPathOverlap(t *testing.T) {
	for _, second := range []bool{false, true} {
		for _, variable := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR"} {
			t.Run(fmt.Sprint(second)+"/"+variable, func(t *testing.T) {
				a, _, _ := t3CodeOpenCodeTestApp(t, second)
				home, config := t3CodeOpenCodePaths(t3CodePaths(a.dir).Root, second)
				t.Setenv(variable, home)
				response := adminRequest(a, "clients/t3-code", `{}`)
				if response.Code != 409 || !strings.Contains(response.Body.String(), "separate from the normal OpenCode") {
					t.Fatal("normal account overlap was not rejected", response.Code, response.Body.String())
				}
				if _, err := os.Stat(config); !os.IsNotExist(err) {
					t.Fatal("rejected preparation wrote the overlapping configuration", err)
				}
			})
		}
	}
	for _, second := range []bool{false, true} {
		t.Run(fmt.Sprint(second)+"/future-alias", func(t *testing.T) {
			a, _, _ := t3CodeOpenCodeTestApp(t, second)
			home, config := t3CodeOpenCodePaths(t3CodePaths(a.dir).Root, second)
			alias := filepath.Join(t.TempDir(), "normal-config")
			if err := os.Symlink(home, alias); err != nil {
				t.Skip("directory aliases are unavailable on this runner", err)
			}
			t.Setenv("XDG_CONFIG_HOME", alias)
			response := adminRequest(a, "clients/t3-code", `{}`)
			if response.Code != 409 || !strings.Contains(response.Body.String(), "separate from the normal OpenCode") {
				t.Fatal("future normal-account alias overlap was not rejected", response.Code, response.Body.String())
			}
			if _, err := os.Stat(config); !os.IsNotExist(err) {
				t.Fatal("rejected alias preparation wrote the normal configuration", err)
			}
		})
	}
}

func TestT3CodeOpenCodeNormalAccountAliasChangeInvalidatesReadinessWithoutSpawn(t *testing.T) {
	a, binary, marker := t3CodeOpenCodeTestApp(t, false)
	alias := filepath.Join(t.TempDir(), "normal-config")
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Skip("directory aliases are unavailable on this runner", err)
	}
	t.Setenv("XDG_CONFIG_HOME", alias)
	prepareT3CodeFixture(t, a)
	saved, err := a.readT3CodePrepared()
	if err != nil || !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("independent normal configuration was rejected", err)
	}
	before, _ := os.ReadFile(marker)
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if a.t3CodeReady(saved, binary, *a.launcher) || a.t3CodeFingerprint(binary, saved.Version, saved.Library, *a.launcher) == saved.Fingerprint {
		t.Fatal("retargeting a normal account alias kept the Kilo profile ready")
	}
	after, _ := os.ReadFile(marker)
	if !bytes.Equal(before, after) {
		t.Fatal("identity-only readiness checks spawned OpenCode")
	}
}

func TestT3CodeOpenCode2UsesDeclaredVariantsCapabilitiesAndProviderPolicies(t *testing.T) {
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.OpenCode2 = true
	noTools := false
	catalog := []modelInfo{{ID: "vendor/one", ReasoningEfforts: []string{"low", "high"}, Tools: &noTools, InputModalities: []string{"declared-future-modality"}, OutputModalities: []string{"text"}}, {ID: "vendor/two", Tools: &noTools}}
	library := testModelLibrary()
	data, err := t3CodeOpenCodeConfig(t3CodeProfileOptions{Preferences: &prefs, Library: library, Catalog: catalog, Port: 9988})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("invalid native OpenCode 2 config")
	}
	policies := config["experimental"].(map[string]any)["policies"].([]any)
	if len(policies) != 2 || policies[0].(map[string]any)["effect"] != "deny" || policies[0].(map[string]any)["resource"] != "*" || policies[1].(map[string]any)["effect"] != "allow" || policies[1].(map[string]any)["resource"] != "kilo-local" {
		t.Fatal("OpenCode 2 catalog is not restricted to the Kilo provider", policies)
	}
	models := config["providers"].(map[string]any)["kilo-local"].(map[string]any)["models"].(map[string]any)
	one, two := models["vendor/one"].(map[string]any), models["vendor/two"].(map[string]any)
	body := one["body"].(map[string]any)
	settings := one["settings"].(map[string]any)
	if body["model"] != "vendor/one" || body["reasoning_effort"] != "high" || settings["baseURL"] != "http://127.0.0.1:9988/v1" || settings["apiKey"] != "{env:"+t3CodeOpenCodeKey+"}" || one["headers"].(map[string]any)["Authorization"] != "Bearer {env:"+t3CodeOpenCodeKey+"}" || one["package"] != "@opencode/ai/providers/openai-compatible" {
		t.Fatal("OpenCode 2 did not pin per-model route, native package, authentication and reasoning together")
	}
	variants := one["variants"].([]any)
	if len(variants) != 2 || variants[0].(map[string]any)["id"] != "low" || variants[1].(map[string]any)["body"].(map[string]any)["reasoning_effort"] != "high" || len(two["variants"].([]any)) != 0 {
		t.Fatal("native variants were invented or did not retain declared efforts", variants, two)
	}
	for _, raw := range variants {
		variant := raw.(map[string]any)
		if variant["body"].(map[string]any)["model"] != "vendor/one" || !reflect.DeepEqual(variant["settings"], settings) || !reflect.DeepEqual(variant["headers"], one["headers"]) {
			t.Fatal("a reasoning variant can replace its Kilo model route or authentication")
		}
	}
	capabilities := one["capabilities"].(map[string]any)
	if capabilities["tools"] != false || !reflect.DeepEqual(capabilities["input"], []any{"declared-future-modality"}) || !reflect.DeepEqual(capabilities["output"], []any{"text"}) || two["capabilities"] != nil {
		t.Fatal("OpenCode 2 capabilities were changed or emitted without required modality arrays", one, two)
	}
}

func TestT3CodeOpenCode2ResolvesDefaultOutsideDeclaredVariants(t *testing.T) {
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.OpenCode2 = true
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/one", Models: []modelLibraryItem{
		{ID: "vendor/one", ReasoningEffort: "medium", ContextWindow: 128000, MaxOutputTokens: 4096},
		{ID: "vendor/two", ReasoningEffort: "high", ContextWindow: 128000, MaxOutputTokens: 4096},
	}}
	catalog := []modelInfo{{ID: "vendor/one", ReasoningEfforts: []string{"low", "high"}}, {ID: "vendor/two", ReasoningEfforts: []string{"low", "high"}}}
	data, err := t3CodeOpenCodeConfig(t3CodeProfileOptions{Preferences: &prefs, Library: library, Catalog: catalog, Port: 9988})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("invalid native OpenCode 2 config")
	}
	models := config["providers"].(map[string]any)["kilo-local"].(map[string]any)["models"].(map[string]any)
	if got := models["vendor/one"].(map[string]any)["body"].(map[string]any)["reasoning_effort"]; got != "low" {
		t.Fatalf("default reasoning effort outside the declared variants was not resolved: %v", got)
	}
	if got := models["vendor/two"].(map[string]any)["body"].(map[string]any)["reasoning_effort"]; got != "high" {
		t.Fatalf("declared default reasoning effort was not preserved: %v", got)
	}
}

func TestT3CodeOpenCodeLaunchScopesAmbientOverridesAndKiloKeys(t *testing.T) {
	for _, connection := range []string{"native", "kilo"} {
		t.Run(connection, func(t *testing.T) {
			a, _, _ := t3CodeOpenCodeTestApp(t, false)
			a.config.T3Code.OpenCodeConnection = connection
			ambientConfig := filepath.Join(t.TempDir(), "native-opencode.json")
			t.Setenv("OPENCODE_CONFIG", ambientConfig)
			t.Setenv("OPENCODE_CONFIG_CONTENT", `{"native":"ambient-config"}`)
			t.Setenv("OPENCODE_SERVER_PASSWORD", "synthetic-ambient-server-key")
			for _, name := range []string{"KILO_OPENCODE_API_KEY", "KILO_OPENCODE2_API_KEY", t3CodeOpenCodeKey} {
				t.Setenv(name, "synthetic-ambient-local-key")
			}
			prepareT3CodeFixture(t, a)
			plan, err := a.planClientLaunch(clientLaunchRequest{Client: "t3-code"}, a.launchRuntime())
			if err != nil {
				t.Fatal(err)
			}
			child := map[string]string{}
			for _, entry := range clientChildEnvironment(os.Environ(), plan.Env, plan.Unset, runtime.GOOS) {
				name, value, _ := strings.Cut(entry, "=")
				child[name] = value
			}
			for _, name := range []string{"KILO_OPENCODE_API_KEY", "KILO_OPENCODE2_API_KEY", t3CodeOpenCodeKey} {
				if _, exists := child[name]; exists {
					t.Fatal("ambient Kilo key entered the root T3 process", name)
				}
			}
			if connection == "kilo" {
				for name := range child {
					if strings.HasPrefix(strings.ToUpper(name), "OPENCODE_") {
						t.Fatal("ambient OpenCode override entered the Kilo driver process", name)
					}
				}
			} else if child["OPENCODE_CONFIG"] != ambientConfig || child["OPENCODE_CONFIG_CONTENT"] != `{"native":"ambient-config"}` || child["OPENCODE_SERVER_PASSWORD"] != "synthetic-ambient-server-key" {
				t.Fatal("the native OpenCode account lost its own environment overrides")
			}
		})
	}
}

func t3CodeOpenCodeSetCapability(t *testing.T, binary string, supported bool) {
	t.Helper()
	archive, err := t3CodeOpenPackage(binary, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs", "apps/server/dist/client/index.html", "apps/server/dist/client/assets/settings.providers-Fixture.js", "apps/server/dist/client/assets/utils-Fixture.js"} {
		files[name], err = archive.read(name, 2<<20)
		if err != nil {
			t.Fatal(err)
		}
	}
	path := archive.file.Name()
	archive.file.Close()
	before, after := "supportsMultipleInstances: true", "supportsMultipleInstances: false"
	if supported {
		before, after = after, before
	}
	source := files["apps/server/dist/bin.mjs"]
	if !bytes.Contains(source, []byte(before)) {
		t.Fatal("invalid OpenCode capability fixture")
	}
	files["apps/server/dist/bin.mjs"] = bytes.Replace(source, []byte(before), []byte(after), 1)
	writeT3CodeFixtureASAR(t, path, files)
}

func TestT3CodeOpenCodeConnectionSwitchAndDowngradePreserveAccountsAndChats(t *testing.T) {
	a, binary, _ := t3CodeOpenCodeTestApp(t, false)
	prepareT3CodeFixture(t, a)
	paths := t3CodePaths(a.dir)
	_, configPath := t3CodeOpenCodePaths(paths.Root, false)
	config, _ := os.ReadFile(configPath)
	secretPath := filepath.Join(paths.Secrets, t3CodeSecretName(t3CodeOpenCodeProxyID, t3CodeOpenCodeKey))
	secret, _ := os.ReadFile(secretPath)
	chatPath := filepath.Join(paths.Data, "retained-chat-fixture.json")
	chat := []byte(`{"account":"kilo_opencode_proxy","messages":["keep this chat"]}`)
	if err := os.WriteFile(chatPath, chat, 0600); err != nil {
		t.Fatal(err)
	}
	setConnection := func(connection string) {
		t.Helper()
		prefs := normalizeT3CodePreferences(a.config.T3Code)
		prefs.OpenCodeConnection = connection
		data, _ := json.Marshal(prefs)
		if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	setConnection("native")
	prepareT3CodeFixture(t, a)
	if saved, err := a.readT3CodePrepared(); err != nil || len(saved.Files) != 0 || !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("native OpenCode retained managed files in its manifest", saved, err)
	}
	t3CodeOpenCodeSetCapability(t, binary, false)
	prepareT3CodeFixture(t, a)
	settings, _ := os.ReadFile(paths.Settings)
	var document map[string]any
	json.Unmarshal(settings, &document)
	instances := document["providerInstances"].(map[string]any)
	if instances[t3CodeOpenCodeProxyID] != nil || instances["opencode"].(map[string]any)["enabled"] != true {
		t.Fatal("unsupported native driver retained a second managed OpenCode registration")
	}
	if saved, err := a.readT3CodePrepared(); err != nil || !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("unsupported native OpenCode could not recover its own account", err)
	}
	t3CodeOpenCodeSetCapability(t, binary, true)
	setConnection("kilo")
	prepareT3CodeFixture(t, a)
	saved, err := a.readT3CodePrepared()
	if err != nil || saved.Providers[t3CodeOpenCodeProxyID].(map[string]any)["enabled"] != true || !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("supported driver did not recover the same Kilo account ID", err)
	}
	for path, previous := range map[string][]byte{configPath: config, secretPath: secret, chatPath: chat} {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, previous) {
			t.Fatal("connection switch/downgrade changed a retained profile, secret or chat", path, err)
		}
	}
}

func TestT3CodeOpenCodePrivatePickerFiltersKiloAndPreservesNative(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node runs this JavaScript fixture in CI")
	}
	policy := map[string][]string{t3CodeOpenCodeProxyID: {"kilo-local/vendor/one", "kilo-local/vendor/two"}}
	defaults := map[string]string{t3CodeOpenCodeProxyID: "kilo-local/vendor/one"}
	variants := map[string][]string{"kilo-local/vendor/one": {"high"}}
	script := string(t3CodeRuntimeModelFilter(policy, defaults, variants)) + `
const agent={id:"agent",type:"select",options:[{id:"build",label:"Build"}],currentValue:"build"};
const models=[{slug:"kilo-local/vendor/one",isDefault:false,capabilities:{optionDescriptors:[{id:"variant",type:"select",options:[{id:"high",label:"High"},{id:"evil-extra",label:"Other"}],currentValue:"evil-extra"},agent]}},{slug:"kilo-local/vendor/two",capabilities:{optionDescriptors:[{id:"variant",type:"select",options:[{id:"evil-extra",label:"Other"}],currentValue:"evil-extra"},agent]}},{slug:"other/builtin",isDefault:true}];
const kilo=__kiloT3InstanceModels("kilo_opencode_proxy","opencode",models);
if(kilo.length!==2||kilo[0].slug!=="kilo-local/vendor/one"||kilo[0].isDefault!==true)throw Error("Kilo picker changed model/default");
const descriptors=kilo[0].capabilities.optionDescriptors;
if(descriptors[0].options.length!==1||descriptors[0].options[0].id!=="high"||Object.hasOwn(descriptors[0],"currentValue")||descriptors[1]!==agent)throw Error("Variant filter lost native agent or retained an unselected project option");
if(kilo[1].capabilities.optionDescriptors.length!==1||kilo[1].capabilities.optionDescriptors[0]!==agent)throw Error("Empty variant descriptor can authorize stale options");
if(models[0].capabilities.optionDescriptors[0].options.length!==2)throw Error("Variant filtering mutated native capabilities");
if(__kiloT3InstanceModels("opencode","opencode",models)!==models)throw Error("Native account was filtered");
if(__kiloT3InstanceModels("kilo_opencode_proxy","other",models)!==models)throw Error("Other driver was filtered");
`
	command := exec.Command(node, "--input-type=module", "--eval", script)
	command.Env = []string{"HOME=" + t.TempDir(), "PATH=" + filepath.Dir(node)}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
}

func TestT3CodeOpenCodeDefaultSelectionDropsOnlyUnselectedVariant(t *testing.T) {
	policy := t3CodeModelPolicy{OpenCodeLibrary: testModelLibrary(), OpenCodeVariantLevels: map[string][]string{"vendor/one": {"low", "high"}, "vendor/two": {}}}
	providers := map[string]any{t3CodeOpenCodeProxyID: map[string]any{"enabled": true}}
	for _, document := range []string{
		`{"instanceId":"kilo_opencode_proxy","model":"kilo-local/vendor/one","options":{"variant":"evil-extra","agent":"plan","custom":true}}`,
		`{"instanceId":"kilo_opencode_proxy","model":"kilo-local/vendor/one","options":[{"id":"variant","value":"evil-extra"},{"id":"agent","value":"plan"},{"id":"custom","value":true}]}`,
	} {
		var selection map[string]any
		json.Unmarshal([]byte(document), &selection)
		result := t3CodeDefaultSelection(selection, providers, policy)
		data, _ := json.Marshal(result)
		if bytes.Contains(data, []byte("evil-extra")) || !bytes.Contains(data, []byte("plan")) || !bytes.Contains(data, []byte("custom")) || !bytes.Contains(data, []byte("kilo-local/vendor/one")) || !bytes.Contains(data, []byte(t3CodeOpenCodeProxyID)) {
			t.Fatal("default variant sanitizing changed the account, model or other options", string(data))
		}
		original, _ := json.Marshal(selection)
		if !bytes.Contains(original, []byte("evil-extra")) {
			t.Fatal("default sanitizing mutated the original settings document")
		}
	}
	selected := map[string]any{"instanceId": t3CodeOpenCodeProxyID, "model": "kilo-local/vendor/one", "options": map[string]any{"variant": "high", "agent": "plan"}}
	if result := t3CodeDefaultSelection(selected, providers, policy); !reflect.DeepEqual(result, selected) {
		t.Fatal("a selected native reasoning variant was removed")
	}
}

func TestT3CodeOpenCodeKiloSupportsMoreThan32WithoutClaudeProfiles(t *testing.T) {
	a, binary, _ := t3CodeOpenCodeTestApp(t, false)
	library := emptyModelLibrary()
	for i := 0; i < 40; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/model-%02d", i), ContextWindow: 100000, MaxOutputTokens: 1000})
	}
	library.DefaultModel = library.Models[0].ID
	input, _ := json.Marshal(map[string]any{"library": library})
	response := adminRequest(a, "clients/t3-code", string(input))
	if response.Code != 200 {
		t.Fatal("OpenCode incorrectly inherited the custom Claude/Codex 32 model limit", response.Code, response.Body.String())
	}
	saved, err := a.readT3CodePrepared()
	if err != nil || len(saved.OpenCodeLibrary.Models) != 40 || len(saved.ClaudeLibrary.Models) != 0 || len(saved.CodexLibrary.Models) != 0 || len(saved.Files) != 2 || !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("OpenCode-only preparation coupled to other Kilo providers", saved, err)
	}
	policy, _ := deriveT3CodeModelPolicy(saved.Preferences, saved.Library, nil)
	allowlist := t3CodeRuntimeModelAllowlist(policy)
	if len(allowlist[t3CodeOpenCodeProxyID]) != 40 || allowlist[t3CodeOpenCodeProxyID][0] != "kilo-local/vendor/model-00" || t3CodeRuntimeModelDefaults(policy)[t3CodeOpenCodeProxyID] != "kilo-local/"+library.DefaultModel {
		t.Fatal("native T3 OpenCode picker did not retain the exact shared selection", allowlist)
	}
}
