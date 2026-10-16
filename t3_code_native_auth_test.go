package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func t3CodeAuthTestEnvironment(t *testing.T, key string, value *string) {
	t.Helper()
	t.Setenv(key, "")
	if value == nil {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Setenv(key, *value); err != nil {
		t.Fatal(err)
	}
}

func TestT3CodeNativeClaudeAuthNamespace(t *testing.T) {
	for _, name := range []string{"default", "empty-config", "custom", "trailing", "dot", "unicode-decomposed", "secure-empty", "secure-custom", "secure-literal"} {
		t.Run(name, func(t *testing.T) {
			options := t3ProfileTestOptions(t)
			options.NormalEnvironment = nil
			custom := filepath.Join(options.NormalHome, "claude-custom")
			var config, secure *string
			switch name {
			case "empty-config":
				v := ""
				config = &v
			case "custom":
				config = &custom
			case "trailing":
				v := custom + string(filepath.Separator)
				config = &v
			case "dot":
				v := custom + string(filepath.Separator) + "."
				config = &v
			case "unicode-decomposed":
				v := filepath.Join(options.NormalHome, "cafe\u0301")
				config = &v
			case "secure-empty":
				config = &custom
				v := ""
				secure = &v
			case "secure-custom":
				v := filepath.Join(options.NormalHome, "secure")
				secure = &v
			case "secure-literal":
				config = &custom
				v := filepath.Join(options.NormalHome, "secure\u0301") + string(filepath.Separator) + "."
				secure = &v
			}
			parent := []string{"HOME=" + options.RootDir, "ANTHROPIC_AUTH_TOKEN=DO-NOT-COPY"}
			if config != nil {
				parent = append(parent, "CLAUDE_CONFIG_DIR="+*config)
			}
			if secure != nil {
				parent = append(parent, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+*secure)
			}
			options.NormalEnvironment = t3CodeNormalEnvironment(options.NormalHome, parent, "linux")
			for key, value := range map[string]*string{"CLAUDE_CONFIG_DIR": config, "CLAUDE_SECURESTORAGE_CONFIG_DIR": secure} {
				got, exists := options.NormalEnvironment[key]
				if exists != (value != nil) || value != nil && got != *value {
					t.Fatalf("literal/presence changed for %s: %q %t", key, got, exists)
				}
			}
			plan, err := planT3CodeProfiles(options)
			if err != nil {
				t.Fatal(err)
			}
			normal := plan.Providers[t3CodeClaudeNormalID].(map[string]any)
			if got := normal["config"].(map[string]any)["homePath"]; got != "" {
				t.Fatalf("native descriptor would resolve the auth override: %q", got)
			}
			env := map[string]string{}
			for _, item := range normal["environment"].([]map[string]any) {
				if item["sensitive"] != false {
					t.Fatal("native account acquired proxy secret", item["name"])
				}
				env[item["name"].(string)] = item["value"].(string)
			}
			if env["HOME"] != options.NormalHome {
				t.Fatal("native HOME redirected")
			}
			gotConfig, hasConfig := env["CLAUDE_CONFIG_DIR"]
			if hasConfig != (config != nil) || config != nil && gotConfig != *config {
				t.Fatal("config override changed or invented")
			}
			got, exists := env["CLAUDE_SECURESTORAGE_CONFIG_DIR"]
			if exists != (secure != nil) || secure != nil && got != *secure {
				t.Fatal("secure-storage override changed or invented")
			}
			// Synthetic auth lookup uses the same descriptor boundary: T3 exports
			// CLAUDE_CONFIG_DIR only for nonempty homePath. No CLI/keychain is called.
			child := map[string]string{"HOME": env["HOME"]}
			if hasConfig {
				child["CLAUDE_CONFIG_DIR"] = gotConfig
			}
			if exists {
				child["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = got
			}
			expectedNamespace := "default-login"
			if config != nil && *config != "" {
				expectedNamespace = "config:" + *config
			}
			if secure != nil {
				expectedNamespace = "secure:" + *secure
			}
			namespace := "default-login"
			if v, ok := child["CLAUDE_CONFIG_DIR"]; ok && v != "" {
				namespace = "config:" + v
			}
			if v, ok := child["CLAUDE_SECURESTORAGE_CONFIG_DIR"]; ok {
				namespace = "secure:" + v
			}
			if namespace != expectedNamespace {
				t.Fatal("synthetic native auth namespace changed")
			}
			_, normalClaude, err := t3CodeNormalProviderHomes(options)
			if err != nil || !filepath.IsAbs(normalClaude) {
				t.Fatal("filesystem safety home became blank", err)
			}
			proxy := plan.Providers[t3CodeClaudeProxyID].(map[string]any)
			if proxy["config"].(map[string]any)["homePath"] != plan.ClaudeHome || plan.ClaudeHome == gotConfig {
				t.Fatal("private proxy home lost isolation")
			}
			for id, raw := range plan.Providers {
				if id == t3CodeClaudeNormalID {
					continue
				}
				for _, item := range raw.(map[string]any)["environment"].([]map[string]any) {
					if item["name"] == "CLAUDE_CONFIG_DIR" || item["name"] == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
						t.Fatal("native auth override leaked to other account", id)
					}
				}
			}
		})
	}
}

func TestT3CodeNativeClaudeSecureStorageSafety(t *testing.T) {
	for _, kind := range []string{"relative", "newline", "nul", "private", "private-child", "private-parent", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			options := t3ProfileTestOptions(t)
			value := "relative"
			switch kind {
			case "newline":
				value = options.NormalHome + "\n"
			case "nul":
				value = options.NormalHome + "\x00"
			case "private":
				value = filepath.Join(options.RootDir, "profiles", "claude-kilo")
			case "private-child":
				value = filepath.Join(options.RootDir, "profiles", "claude-kilo", "child")
			case "private-parent":
				value = options.RootDir
			case "symlink":
				private := filepath.Join(options.RootDir, "profiles", "claude-kilo")
				if err := os.MkdirAll(private, 0700); err != nil {
					t.Fatal(err)
				}
				value = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(private, value); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			options.NormalEnvironment["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = value
			if _, err := planT3CodeProfiles(options); err == nil {
				t.Fatal("unsafe secure-storage path accepted", kind)
			}
		})
	}
}

func TestT3CodeNativeClaudeMigratesDefaultPreparation(t *testing.T) {
	a := t3CodeTestApp(t, "linux")
	a.config.T3Code.Codex, a.config.T3Code.KiloCodex, a.config.T3Code.Claude, a.config.T3Code.KiloClaude = false, false, true, false
	t3CodeAuthTestEnvironment(t, "CLAUDE_CONFIG_DIR", nil)
	t3CodeAuthTestEnvironment(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR", nil)
	paths := t3CodePaths(a.dir)
	marker := filepath.Join(a.launcher.home, ".claude.json")
	login := []byte("synthetic default login marker; never a real account\n")
	if err := os.WriteFile(marker, login, 0600); err != nil {
		t.Fatal(err)
	}
	prepareT3CodeFixture(t, a)
	saved, err := a.readT3CodePrepared()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Providers[t3CodeClaudeNormalID].(map[string]any)["config"].(map[string]any)["homePath"] != "" {
		t.Fatal("fresh preparation redirected default Claude auth")
	}
	rt := a.launchRuntime()
	binary := mustResolveT3CodeTest(t, a)
	installation, err := inspectT3CodeInstallation(binary, rt.platform)
	if err != nil {
		t.Fatal(err)
	}
	// Exact v0.67.0 revision-8 native-only fingerprint envelope. The old
	// provider descriptor explicitly materialized ~/.claude. Other data stays
	// identical so migration is not justified by unrelated model/config drift.
	normalEnv := t3CodeNormalEnvironment(rt.home, os.Environ(), rt.platform)
	delete(normalEnv, "CODEX_HOME")
	normalClaude, err := t3CodeCanonicalPath(filepath.Join(rt.home, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := rt.resolve("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	additional, err := planT3CodeOptionalProviders(saved.Preferences, installation, rt, paths.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	legacyData, err := json.Marshal([]any{binary, saved.Version, installation.SourceDigest, "", claude, saved.Preferences, additional, rt.home, normalEnv, "", normalClaude, 8})
	if err != nil {
		t.Fatal(err)
	}
	saved.Fingerprint = openDesignHash(legacyData)
	saved.Providers[t3CodeClaudeNormalID].(map[string]any)["config"].(map[string]any)["homePath"] = filepath.Join(rt.home, ".claude")
	selection, _ := json.Marshal(saved)
	if err := os.WriteFile(paths.Selection, selection, 0600); err != nil {
		t.Fatal(err)
	}
	settingsBytes, err := os.ReadFile(paths.Settings)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		t.Fatal(err)
	}
	settings["providerInstances"].(map[string]any)[t3CodeClaudeNormalID] = saved.Providers[t3CodeClaudeNormalID]
	settings["unmanagedSyntheticMarker"] = "preserve"
	settingsBytes, _ = json.Marshal(settings)
	if err := os.WriteFile(paths.Settings, settingsBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if a.t3CodeReady(saved, binary, rt) {
		t.Fatal("old default auth profile remained ready")
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "t3-code"}, rt); err == nil {
		t.Fatal("old default auth profile launched without migration")
	}
	prepareT3CodeFixture(t, a)
	migrated, err := a.readT3CodePrepared()
	if err != nil || !a.t3CodeReady(migrated, binary, rt) {
		t.Fatal("migrated preparation not ready", err)
	}
	if migrated.Providers[t3CodeClaudeNormalID].(map[string]any)["config"].(map[string]any)["homePath"] != "" {
		t.Fatal("migration retained explicit default override")
	}
	before, err := os.ReadFile(paths.Selection)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, err := os.ReadFile(paths.Settings)
	if err != nil {
		t.Fatal(err)
	}
	prepareT3CodeFixture(t, a)
	after, _ := os.ReadFile(paths.Selection)
	settingsAfter, _ := os.ReadFile(paths.Settings)
	if !bytes.Equal(before, after) || !bytes.Equal(settingsBefore, settingsAfter) {
		t.Fatal("repreparation was not stable")
	}
	if !bytes.Contains(settingsAfter, []byte("unmanagedSyntheticMarker")) {
		t.Fatal("migration discarded unrelated settings")
	}
	if current, err := os.ReadFile(marker); err != nil || !bytes.Equal(current, login) {
		t.Fatal("native login marker modified", err)
	}
}

func TestT3CodeNativeClaudeLiteralOverridesInvalidatePreparation(t *testing.T) {
	a := t3CodeTestApp(t, "linux")
	config := filepath.Join(a.launcher.home, "custom") + string(filepath.Separator) + "."
	secure := filepath.Join(a.launcher.home, "secure") + string(filepath.Separator)
	t3CodeAuthTestEnvironment(t, "CLAUDE_CONFIG_DIR", &config)
	t3CodeAuthTestEnvironment(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR", &secure)
	prepareT3CodeFixture(t, a)
	saved, err := a.readT3CodePrepared()
	if err != nil || !a.t3CodeReady(saved, mustResolveT3CodeTest(t, a), a.launchRuntime()) {
		t.Fatal("literal overrides not ready", err)
	}
	for _, key := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		t.Run(key, func(t *testing.T) {
			original := config
			if key == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
				original = secure
			}
			changed := filepath.Clean(original)
			if reflect.DeepEqual(original, changed) {
				t.Fatal("fixture must change only literal spelling")
			}
			t.Setenv(key, changed)
			if a.t3CodeReady(saved, mustResolveT3CodeTest(t, a), a.launchRuntime()) {
				t.Fatal("literal auth namespace change retained stale readiness")
			}
		})
	}
}

func TestT3CodeNativeClaudeSecureAliasRetargetInvalidatesPreparation(t *testing.T) {
	a := t3CodeTestApp(t, "linux")
	target := filepath.Join(a.launcher.home, "secure-target")
	alias := filepath.Join(a.launcher.home, "secure-alias")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	t3CodeAuthTestEnvironment(t, "CLAUDE_SECURESTORAGE_CONFIG_DIR", &alias)
	prepareT3CodeFixture(t, a)
	saved, err := a.readT3CodePrepared()
	if err != nil || !a.t3CodeReady(saved, mustResolveT3CodeTest(t, a), a.launchRuntime()) {
		t.Fatal("initial secure alias not ready", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t3CodePaths(a.dir).Root, alias); err != nil {
		t.Fatal(err)
	}
	if a.t3CodeReady(saved, mustResolveT3CodeTest(t, a), a.launchRuntime()) {
		t.Fatal("secure alias retarget retained readiness")
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "t3-code"}, a.launchRuntime()); err == nil {
		t.Fatal("secure alias retarget launched")
	}
}
