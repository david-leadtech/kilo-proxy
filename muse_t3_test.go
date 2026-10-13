package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMuseT3RequiresSelectedExecutableOnlyWhenEnabled(t *testing.T) {
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	installation := t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}
	root, home := t.TempDir(), t.TempDir()
	calls := 0
	rt := clientLaunchRuntime{platform: "linux", home: home, resolve: func(id, custom string) (string, error) {
		calls++
		if id != "muse" || custom != "" {
			t.Fatal("Muse resolved another provider", id, custom)
		}
		return "", errors.New("missing")
	}}
	if _, err := planT3CodeOptionalProviders(prefs, installation, rt, root); err != nil || calls != 0 {
		t.Fatal("disabled Muse inspected the host", calls, err)
	}
	prefs.Providers = []string{"muse"}
	if _, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{}, rt, root); err == nil || calls != 0 {
		t.Fatal("unsupported T3 package resolved Muse", calls, err)
	}
	if _, err := planT3CodeOptionalProviders(prefs, installation, rt, root); err == nil || calls != 1 || !strings.Contains(err.Error(), "Kilo Settings") {
		t.Fatal("missing Muse was accepted", calls, err)
	}
	rt.resolve = nil
	if _, err := planT3CodeOptionalProviders(prefs, installation, rt, root); err == nil {
		t.Fatal("missing resolver accepted")
	}
	for _, invalid := range []string{"", "muse", filepath.Join(home, "muse") + "\n--model other"} {
		rt.resolve = func(string, string) (string, error) { return invalid, nil }
		if _, err := planT3CodeOptionalProviders(prefs, installation, rt, root); err == nil {
			t.Fatal("invalid selected executable accepted", invalid)
		}
	}
}

func TestMuseT3UsesNativeCatalogAndNormalHomeWithoutStartingCLI(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(t.TempDir(), "custom Muse executable")
	// This is deliberately not an executable program. Preparation must leave
	// status, model/list and authentication to T3's native MSP driver.
	if err := os.WriteFile(binary, []byte("synthetic Muse CLI; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.Providers = []string{"muse"}
	t.Setenv("META_API_KEY", "ambient-meta-key-do-not-copy")
	t.Setenv("KILO_LOCAL_API_KEY", "ambient-kilo-key-do-not-copy")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-claude-key-do-not-copy")
	t.Setenv("OPENAI_API_KEY", "ambient-openai-key-do-not-copy")
	t.Setenv("OPENAI_BASE_URL", "http://kilo.invalid")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "configured-muse-home"))
	rt := clientLaunchRuntime{platform: "linux", home: home, resolve: func(id, custom string) (string, error) {
		if id != "muse" || custom != "" {
			t.Fatal(id, custom)
		}
		return binary, nil
	}}
	for _, verify := range []bool{false, true} {
		providers, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}, rt, t.TempDir(), verify)
		if err != nil {
			t.Fatal("planning executed a Muse session", verify, err)
		}
		muse := providers["muse"].(map[string]any)
		want := map[string]any{"binaryPath": binary, "customModels": []any{}}
		if muse["driver"] != "muse" || muse["displayName"] != "Muse Code" || muse["enabled"] != true || !reflect.DeepEqual(muse["config"], want) {
			t.Fatal("native Muse model catalog or driver was replaced", muse)
		}
		env := t3CodeOptionalEnvironmentMap(muse)
		if env["HOME"] != home || env["XDG_CONFIG_HOME"] != filepath.Join(home, "configured-muse-home") {
			t.Fatal("native login location changed", env)
		}
		encoded, _ := json.Marshal(muse)
		for _, unwanted := range []string{"ambient-", "http://kilo.invalid", "ANTHROPIC_", "OPENAI_", "META_API_KEY", "KILO_LOCAL_API_KEY"} {
			if strings.Contains(string(encoded), unwanted) {
				t.Fatal("native Muse received an API or gateway override", unwanted)
			}
		}
	}
}

func TestMuseT3PathChangesRequireReprepareAndKeepNativeSelection(t *testing.T) {
	a := t3CodeTestApp(t, "linux", t3CodeNightlyVersion)
	addMuseT3CapabilityFixture(t, a)
	binary := mustResolveT3CodeTest(t, a)
	a.config.T3Code.Providers = []string{"muse"}
	selected := filepath.Join(t.TempDir(), "muse-custom-release")
	priorResolve := a.launcher.resolve
	a.launcher.resolve = func(id, custom string) (string, error) {
		if id == "muse" {
			return selected, nil
		}
		return priorResolve(id, custom)
	}
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	muse := saved.Providers["muse"].(map[string]any)
	if muse["config"].(map[string]any)["binaryPath"] != selected || !a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("selected Muse installation was not prepared", muse)
	}
	before := clientInstallationTree(t, t3CodePaths(a.dir).Root)
	selected = filepath.Join(t.TempDir(), "muse-another-custom-release")
	if a.t3CodeReady(saved, binary, a.launchRuntime()) || !reflect.DeepEqual(before, clientInstallationTree(t, t3CodePaths(a.dir).Root)) {
		t.Fatal("changed Muse reused old readiness or rewrote its workspace")
	}
	selection := map[string]any{"instanceId": "muse", "model": "muse-spark-1.3", "options": map[string]any{"reasoningEffort": "max"}}
	if got := t3CodeDefaultSelection(selection, saved.Providers, t3CodeModelPolicy{}); !reflect.DeepEqual(got, selection) {
		t.Fatal("native Muse model or reasoning selection was replaced", got)
	}
}

func addMuseT3CapabilityFixture(t *testing.T, a *app) {
	t.Helper()
	binary := mustResolveT3CodeTest(t, a)
	archivePath := filepath.Join(filepath.Dir(binary), "resources", "app.asar")
	archive, err := t3CodeOpenASAR(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, path := range []string{"package.json", "apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs", "apps/server/dist/client/index.html", "apps/server/dist/client/assets/settings.providers-Fixture.js", "apps/server/dist/client/assets/utils-Fixture.js"} {
		files[path], err = archive.read(path, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
	}
	archive.file.Close()
	files["apps/server/dist/bin.mjs"] = append(files["apps/server/dist/bin.mjs"], []byte("\n"+t3CodeOptionalCapabilityFixture())...)
	writeT3CodeFixtureASAR(t, archivePath, files)
}

func TestMuseT3NativeOnlyPreparesAndLaunchesWithoutGatewayOrSharedModels(t *testing.T) {
	a := t3CodeTestApp(t, "linux", t3CodeNightlyVersion)
	addMuseT3CapabilityFixture(t, a)
	prefs := normalizeT3CodePreferences(a.config.T3Code)
	prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
	prefs.Providers = []string{"muse"}
	a.config.T3Code = prefs
	a.apiKey, a.config.OrgID, a.config.LocalKey = "", "", ""
	a.modelLibrary.mu.Lock()
	a.modelLibrary.state.Library = emptyModelLibrary()
	a.modelLibrary.state.RecoveryRequired = true
	a.modelLibrary.state.Warning = "synthetic recovery must not gate native accounts"
	a.modelLibrary.mu.Unlock()
	selected := filepath.Join(t.TempDir(), "muse custom")
	previous := a.launcher.resolve
	a.launcher.resolve = func(id, custom string) (string, error) {
		switch id {
		case "muse":
			return selected, nil
		case "codex-cli", "claude":
			t.Fatal("disabled CLI was resolved", id)
		}
		return previous(id, custom)
	}
	// Custom native authentication remains inherited by T3, never copied into
	// its account settings or replaced by the isolated T3 UI home.
	auth := filepath.Join(a.launcher.home, "custom-muse-auth.json")
	t.Setenv("MUSE_AUTH_PATH", auth)
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	binary := mustResolveT3CodeTest(t, a)
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) || len(saved.Library.Models) != 0 || len(saved.Files) != 0 {
		t.Fatal("native preparation requires shared models or Kilo files", saved)
	}
	paths := t3CodePaths(a.dir)
	if entries, err := os.ReadDir(paths.Secrets); err != nil || len(entries) != 0 {
		t.Fatal("native preparation wrote proxy secrets", entries, err)
	}
	if _, err := os.Stat(filepath.Join(paths.Root, "profiles")); !os.IsNotExist(err) {
		t.Fatal("native preparation created Kilo CLI profiles", err)
	}
	clientSettings := []byte(`{"theme":"dark","providerModelPreferences":{"muse":{"hiddenModels":[],"modelOrder":["muse-spark-1.3"]}}}`)
	conversation := filepath.Join(paths.Data, "userdata", "synthetic-conversation.db")
	for path, data := range map[string][]byte{paths.ClientSettings: clientSettings, conversation: []byte("synthetic conversation must remain intact")} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	settingsData, err := os.ReadFile(paths.Settings)
	var settings map[string]any
	if err != nil || json.Unmarshal(settingsData, &settings) != nil {
		t.Fatal("cannot inspect native settings", err)
	}
	selection := map[string]any{"instanceId": "muse", "model": "muse-spark-1.3", "options": map[string]any{"reasoningEffort": "max"}}
	settings["defaultModelSelection"] = selection
	settingsData, _ = json.Marshal(settings)
	if err := os.WriteFile(paths.Settings, settingsData, 0600); err != nil {
		t.Fatal(err)
	}
	selected = filepath.Join(t.TempDir(), "another selected Muse")
	prepareT3CodeFixture(t, a)
	saved = mustReadT3CodePrepared(t, a)
	for path, expected := range map[string]string{paths.ClientSettings: string(clientSettings), conversation: "synthetic conversation must remain intact"} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != expected {
			t.Fatal("native re-preparation changed chats or client settings", path, err)
		}
	}
	settingsData, _ = os.ReadFile(paths.Settings)
	if json.Unmarshal(settingsData, &settings) != nil || !reflect.DeepEqual(settings["defaultModelSelection"], selection) {
		t.Fatal("native re-preparation replaced Muse model or reasoning selection")
	}
	preparedData, _ := os.ReadFile(paths.Selection)
	saved.Providers[t3CodeCodexProxyID].(map[string]any)["enabled"] = true
	tampered, _ := json.Marshal(saved)
	if err := os.WriteFile(paths.Selection, tampered, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.readT3CodePrepared(); err == nil {
		t.Fatal("native preparation accepted a Kilo account enabled outside its preferences")
	}
	if err := os.WriteFile(paths.Selection, preparedData, 0600); err != nil {
		t.Fatal(err)
	}
	saved = mustReadT3CodePrepared(t, a)
	data, _ := json.Marshal(saved.Providers)
	for _, unwanted := range []string{"META_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "KILO_LOCAL_API_KEY", "MUSE_AUTH_PATH", auth} {
		if strings.Contains(string(data), unwanted) {
			t.Fatal("native configuration copied authentication or proxy state", unwanted)
		}
	}
	// Gateway edits are unrelated to Muse's catalog/login and must not require
	// re-preparing or rewriting an otherwise valid native workspace.
	a.config.OrgID, a.config.LocalKey, a.config.Port = "unrelated-org", "unrelated-key", 0
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("unrelated gateway edits invalidated a native workspace")
	}
	var launched clientLaunchPlan
	a.launcher.start = func(plan clientLaunchPlan) error { launched = plan; return nil }
	response := adminRequest(a, "clients/launch", `{"client":"t3-code"}`)
	if response.Code != 200 || launched.Client != "t3-code" || a.proxyListener != nil || !launched.skipProxy {
		t.Fatal("native T3 launch required the Kilo gateway", response.Code, response.Body.String(), launched)
	}
	child := clientChildEnvironment(os.Environ(), launched.Env, launched.Unset, "linux")
	if !helperContains(child, "MUSE_AUTH_PATH="+auth) {
		t.Fatal("T3 launch discarded the user's native Muse auth path")
	}
	a.closeT3CodeAssets()
	a.t3CodeLaunchUntil = a.t3CodeLaunchUntil.Add(-time.Minute)
	// Switching back to a Kilo account restores the complete shared-model and
	// connection requirements, and never reuses the native-only preparation.
	a.config.T3Code.KiloCodex = true
	if a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("native preparation reused for a Kilo account")
	}
	response = adminRequest(a, "clients/t3-code", `{}`)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "shared models") {
		t.Fatal("Kilo account ignored shared-model recovery", response.Code, response.Body.String())
	}
	a.modelLibrary.mu.Lock()
	a.modelLibrary.state.Library = testModelLibrary()
	a.modelLibrary.state.RecoveryRequired, a.modelLibrary.state.Warning = false, ""
	a.modelLibrary.mu.Unlock()
	response = adminRequest(a, "clients/t3-code", `{}`)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "Connect") {
		t.Fatal("Kilo account ignored its connection", response.Code, response.Body.String())
	}
}

func TestMuseT3NativeOnlyIgnoresDisabledProviderHomes(t *testing.T) {
	for _, environment := range []string{"CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Run(environment, func(t *testing.T) {
			a := t3CodeTestApp(t, "linux", t3CodeNightlyVersion)
			addMuseT3CapabilityFixture(t, a)
			prefs := normalizeT3CodePreferences(a.config.T3Code)
			prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
			prefs.Providers = []string{"muse"}
			a.config.T3Code = prefs
			previous := a.launcher.resolve
			a.launcher.resolve = func(id, custom string) (string, error) {
				if id == "muse" {
					return filepath.Join(a.launcher.home, "selected-muse"), nil
				}
				return previous(id, custom)
			}
			// Disabled providers cannot make Muse depend on their unrelated
			// invalid overrides or on an unreadable/cyclic default-home alias.
			t.Setenv(environment, "relative/provider-home")
			defaultHome := ".codex"
			if environment == "CLAUDE_CONFIG_DIR" {
				defaultHome = ".claude"
			}
			cycle := filepath.Join(a.launcher.home, defaultHome)
			cycleErr := os.Symlink(cycle, cycle)
			if cycleErr != nil && runtime.GOOS != "windows" {
				t.Fatal(cycleErr)
			}
			prepareT3CodeFixture(t, a)
			saved := mustReadT3CodePrepared(t, a)
			binary := mustResolveT3CodeTest(t, a)
			if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
				t.Fatal("disabled invalid provider home blocked Muse")
			}
			t.Setenv(environment, filepath.Join(t.TempDir(), "another-provider-home"))
			if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
				t.Fatal("disabled provider-home edit invalidated Muse")
			}
			// Opting into the native provider restores its home validation.
			if environment == "CODEX_HOME" {
				a.config.T3Code.Codex = true
			} else {
				a.config.T3Code.Claude = true
			}
			t.Setenv(environment, "relative/provider-home")
			if response := adminRequest(a, "clients/t3-code", `{}`); response.Code != 409 || !strings.Contains(response.Body.String(), "absolute paths") {
				t.Fatal("enabled provider accepted an invalid home", response.Code, response.Body.String())
			}
			if cycleErr != nil {
				// Windows may deny symlink creation without developer/admin rights;
				// relative-home isolation above is still checked on that platform.
				return
			}
			t.Setenv(environment, "")
			if response := adminRequest(a, "clients/t3-code", `{}`); response.Code != 409 || !strings.Contains(response.Body.String(), "resolve the normal") {
				t.Fatal("enabled provider accepted its cyclic home", response.Code, response.Body.String())
			}
		})
	}
}
