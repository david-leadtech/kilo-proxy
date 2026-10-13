package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func cliPreferencesTestExecutable(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("synthetic native CLI; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func postCLISelection(t *testing.T, a *app, input any, want int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	w := adminRequest(a, "clients/cli/preferences", string(data))
	if w.Code != want {
		t.Fatal("CLI preferences", w.Code, w.Body.String())
	}
	return w
}

func TestCLIExecutablePreferencesPersistPreserveOmittedAndResetIndependently(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	paths := [2]string{cliPreferencesTestExecutable(t, "private Codex"), cliPreferencesTestExecutable(t, "private Claude")}
	postCLISelection(t, a, cliPreferences{Version: 1, CodexExecutable: paths[0], ClaudeExecutable: paths[1]}, 200)
	for i, client := range []string{"codex-cli", "claude"} {
		if got, err := a.launchRuntime().resolve(client, ""); err != nil || got != paths[i] {
			t.Fatal("explicit CLI lost to automatic discovery", client, got, err)
		}
	}
	if got, err := a.launchRuntime().resolve("codex", ""); err != nil || got == paths[0] {
		t.Fatal("Codex Desktop was replaced by the CLI selection", got, err)
	}
	postCLISelection(t, a, map[string]any{"version": 1}, 200)
	stored, err := readSettings(a.dir)
	if err != nil || stored.CodexCLIExecutable != paths[0] || stored.ClaudeCodeExecutable != paths[1] {
		t.Fatal("omitted fields lost a selection", err)
	}
	reloaded, err := newApp(a.dir, a.vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.stop)
	for i, client := range []string{"codex-cli", "claude"} {
		if got, err := reloaded.launchRuntime().resolve(client, ""); err != nil || got != paths[i] {
			t.Fatal("restart lost the CLI selection", client, got, err)
		}
	}
	postCLISelection(t, a, map[string]any{"version": 1, "codexExecutable": ""}, 200)
	if got, err := a.launchRuntime().resolve("codex-cli", ""); err != nil || got == paths[0] {
		t.Fatal("reset did not restore automatic Codex discovery", got, err)
	}
	if got, err := a.launchRuntime().resolve("claude", ""); err != nil || got != paths[1] {
		t.Fatal("resetting Codex changed Claude", got, err)
	}
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("claude", ""); err == nil || got != "" {
		t.Fatal("missing explicit Claude silently fell back", got, err)
	}
	// A tool removed after shutdown should remain a visible selection error,
	// rather than preventing Kilo itself from opening its settings.
	if _, err := readSettings(a.dir); err != nil {
		t.Fatal("a missing selected CLI prevented settings recovery", err)
	}
}

func TestCLIExecutablePreferencesStrictAuthInputAndFailedSave(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Header.Del("Authorization") }, func(r *http.Request) { r.Host = "evil.example" }, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }} {
			r := httptest.NewRequest(method, "http://"+a.adminHost+cliPreferencesEndpoint, strings.NewReader(`{"version":1}`))
			r.Header.Set("Authorization", "Bearer "+a.adminToken)
			r.Header.Set("Content-Type", "application/json")
			mutate(r)
			w := httptest.NewRecorder()
			a.adminHandler().ServeHTTP(w, r)
			if w.Code != 401 && w.Code != 403 {
				t.Fatal("untrusted selection request accepted", method, w.Code)
			}
		}
	}
	for _, body := range []string{`{}`, `{"version":2}`, `{"version":1,"command":"x"}`, `{"version":1,"codexExecutable":42}`, `{"version":1} {}`} {
		if w := adminRequest(a, "clients/cli/preferences", body); w.Code != 400 {
			t.Fatal("invalid selection input accepted", body, w.Code)
		}
	}
	for _, path := range []string{"relative/claude", "~/claude", "/tmp/bad\npath", "/tmp/bad\x00path", "/" + strings.Repeat("x", 8192), filepath.Join(t.TempDir(), "not-installed"), t.TempDir()} {
		postCLISelection(t, a, cliPreferences{Version: 1, ClaudeExecutable: path}, 400)
	}
	path := cliPreferencesTestExecutable(t, "custom Claude")
	previous := a.config
	a.launchMu.Lock()
	postCLISelection(t, a, cliPreferences{Version: 1, ClaudeExecutable: path}, 409)
	a.launchMu.Unlock()
	if !reflect.DeepEqual(a.config, previous) || a.cliExecutable("claude") != "" {
		t.Fatal("busy preparation changed the selection")
	}
	if err := os.Mkdir(filepath.Join(a.dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	postCLISelection(t, a, cliPreferences{Version: 1, ClaudeExecutable: path}, 500)
	if !reflect.DeepEqual(a.config, previous) || a.cliExecutable("claude") != "" {
		t.Fatal("failed persistence changed effective CLI selection")
	}
}

func TestCLIExecutablePreferencesManagedCurrentCannotBypassIntegrity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installations")
	}
	for _, client := range []string{"codex-cli", "claude", "opencode", "muse"} {
		t.Run(client, func(t *testing.T) {
			a := launchTestApp(t)
			record := managedToolRegistryFixture(t, a.dir, client, "current")
			if err := writeManagedTool(a.dir, record); err != nil {
				t.Fatal(err)
			}
			if err := a.validateSelectedClientExecutable(client, record.Executable); err != nil {
				t.Fatal("verified current entrypoint rejected", err)
			}
			retained := managedToolRegistryFixture(t, a.dir, client, "retained")
			if a.validateSelectedClientExecutable(client, retained.Executable) == nil || a.validateSelectedClientExecutable("opencode2", record.Executable) == nil {
				t.Fatal("retained release or other client's entrypoint bypassed ownership")
			}
			alias := filepath.Join(t.TempDir(), "managed-alias")
			if err := os.Symlink(record.Executable, alias); err != nil {
				t.Fatal(err)
			}
			if a.validateSelectedClientExecutable(client, alias) == nil {
				t.Fatal("external symlink bypassed the exact current entrypoint")
			}
			if err := os.WriteFile(record.Executable, []byte("modified executable"), 0700); err != nil {
				t.Fatal(err)
			}
			if a.validateSelectedClientExecutable(client, record.Executable) == nil {
				t.Fatal("manual path bypassed a corrupt managed digest")
			}
		})
	}
}

func TestCLIExecutablePreferencesCodexWrapperStaysWithSelectedInstallation(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	root := t.TempDir()
	wrapper := writeOpenDesignCodexFixture(t, filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js"), []byte("#!/usr/bin/env node\n"))
	suffix, target := openDesignCodexTarget("macos", runtime.GOARCH)
	native := writeOpenDesignCodexFixture(t, filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-"+suffix, "vendor", target, "codex", "codex"), openDesignCodexNativeFixture("macos", runtime.GOARCH))
	other := writeOpenDesignCodexFixture(t, filepath.Join(a.launcher.home, "Applications", "Codex.app", "Contents", "Resources", "codex"), openDesignCodexNativeFixture("macos", runtime.GOARCH))
	postCLISelection(t, a, cliPreferences{Version: 1, CodexExecutable: wrapper}, 200)
	for _, resolve := range []func() (string, error){func() (string, error) { return a.launchRuntime().resolve("codex-cli", "") }, func() (string, error) { return resolveOpenDesignCLI("codex-cli", a.launchRuntime()) }} {
		if got, err := resolve(); err != nil || got != native || got == other {
			t.Fatal("selected package was replaced by Codex Desktop", got, err)
		}
	}
	if err := os.Remove(native); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveOpenDesignCLI("codex-cli", a.launchRuntime()); err == nil || got != "" {
		t.Fatal("broken selected package silently fell back to Desktop", got, err)
	}
}

func TestCLIExecutablePreferencesT3UsesSameSelectedPathsAndRequiresReprepare(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "custom", true: "managed-current"}[managed], func(t *testing.T) {
			if managed && runtime.GOOS == "windows" {
				t.Skip("managed Unix installations")
			}
			a := t3CodeTestApp(t, "macos", t3CodeNightlyVersion)
			paths := [2]string{cliPreferencesTestExecutable(t, "custom Codex 0.162.1"), cliPreferencesTestExecutable(t, "custom Claude 2.1.288")}
			if managed {
				for i, client := range []string{"codex-cli", "claude"} {
					record := managedToolRegistryFixture(t, a.dir, client, "current")
					if err := writeManagedTool(a.dir, record); err != nil {
						t.Fatal(err)
					}
					paths[i] = record.Executable
				}
			}
			probed := ""
			a.t3CodeProbeClaude = func(path string, _ clientLaunchRuntime) claudeCapabilities {
				probed = path
				return claudeCaps("2.1.288 (Claude Code)")
			}
			postCLISelection(t, a, cliPreferences{Version: 1, CodexExecutable: paths[0], ClaudeExecutable: paths[1]}, 200)
			prepareT3CodeFixture(t, a)
			saved := mustReadT3CodePrepared(t, a)
			for _, expected := range []struct{ id, path string }{{t3CodeCodexNormalID, paths[0]}, {t3CodeCodexProxyID, paths[0]}, {t3CodeClaudeNormalID, paths[1]}, {t3CodeClaudeProxyID, paths[1]}} {
				provider := saved.Providers[expected.id].(map[string]any)
				config := provider["config"].(map[string]any)
				if config["binaryPath"] != expected.path {
					t.Fatal("T3 selected a different CLI", expected.id, config["binaryPath"], expected.path)
				}
			}
			if probed != paths[1] {
				t.Fatal("Claude capability guard probed another installation", probed)
			}
			binary, _ := a.launchRuntime().resolve("t3-code", "")
			if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
				t.Fatal("prepared exact selected paths were not ready")
			}
			before := clientInstallationTree(t, t3CodePaths(a.dir).Root)
			postCLISelection(t, a, map[string]any{"version": 1, "codexExecutable": cliPreferencesTestExecutable(t, "different Codex")}, 200)
			if a.t3CodeReady(saved, binary, a.launchRuntime()) || !reflect.DeepEqual(before, clientInstallationTree(t, t3CodePaths(a.dir).Root)) {
				t.Fatal("path change reused old preparation or rewrote existing workspace")
			}
		})
	}
}

func TestCLIExecutablePreferencesCodexWrapperCannotBypassManagedNativeDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installations")
	}
	a := launchTestApp(t)
	a.config.Port = 8877
	record := managedToolRegistryFixture(t, a.dir, "codex-cli", "current")
	data := openDesignCodexNativeFixture("macos", runtime.GOARCH)
	if err := os.WriteFile(record.Executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	record.Digest, err = managedToolFingerprint(record.Executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagedTool(a.dir, record); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wrapper := writeOpenDesignCodexFixture(t, filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js"), []byte("#!/usr/bin/env node\n"))
	suffix, target := openDesignCodexTarget("macos", runtime.GOARCH)
	alias := filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-"+suffix, "vendor", target, "codex", "codex")
	if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(record.Executable, alias); err != nil {
		t.Fatal(err)
	}
	postCLISelection(t, a, cliPreferences{Version: 1, CodexExecutable: wrapper}, 200)
	canonical, err := filepath.EvalSymlinks(record.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("codex-cli", ""); err != nil || got != canonical {
		t.Fatal("verified managed dependency rejected", got, err)
	}
	if err := os.WriteFile(record.Executable, append(data, []byte("changed")...), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("codex-cli", ""); err == nil || got != "" {
		t.Fatal("external wrapper bypassed its managed native dependency digest", got, err)
	}
}

func TestCLIExecutablePreferencesConcurrentResolverUsesSavedSnapshot(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	paths := [2]string{cliPreferencesTestExecutable(t, "Claude A"), cliPreferencesTestExecutable(t, "Claude B")}
	postCLISelection(t, a, cliPreferences{Version: 1, ClaudeExecutable: paths[0]}, 200)
	var workers sync.WaitGroup
	workers.Add(1)
	errors := make(chan string, 1)
	go func() {
		defer workers.Done()
		for range 100 {
			got, err := a.launchRuntime().resolve("claude", "")
			if err != nil || got != paths[0] && got != paths[1] {
				errors <- "resolver lost the atomic selected-path snapshot"
				return
			}
		}
	}()
	for i := range 10 {
		postCLISelection(t, a, map[string]any{"version": 1, "claudeExecutable": paths[i%2]}, 200)
	}
	workers.Wait()
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
}

func TestCLIExecutablePreferencesSynaraPersistOmittedResetAndNoFallback(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	a.launcher.platform = synaraTestPlatform("macos")
	selected := syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform)
	automatic := syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform)
	codex := cliPreferencesTestExecutable(t, "selected Codex")
	fallbackCalls := 0
	previousResolve := a.launcher.resolve
	a.launcher.resolve = func(client, custom string) (string, error) {
		if client == "synara" {
			fallbackCalls++
			return automatic, nil
		}
		return previousResolve(client, custom)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "codexExecutable": codex, "synaraExecutable": selected}, 200)
	if got, err := a.launchRuntime().resolve("synara", ""); err != nil || got != selected || fallbackCalls != 0 {
		t.Fatal("explicit Synara did not retain priority over automatic discovery", got, err, fallbackCalls)
	}
	response := postCLISelection(t, a, map[string]any{"version": 1}, 200)
	var preferences cliPreferences
	if err := json.Unmarshal(response.Body.Bytes(), &preferences); err != nil || preferences.SynaraExecutable != selected || preferences.CodexExecutable != codex {
		t.Fatal("omitted executable preferences were not preserved", preferences, err)
	}
	stored, err := readSettings(a.dir)
	if err != nil || stored.SynaraExecutable != selected || stored.CodexCLIExecutable != codex {
		t.Fatal("Synara selection was not persisted", stored, err)
	}
	reloaded, err := newApp(a.dir, a.vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.stop)
	reloaded.launcher = &clientLaunchRuntime{platform: a.launcher.platform, resolve: a.launcher.resolve}
	if got, err := reloaded.launchRuntime().resolve("synara", ""); err != nil || got != selected || fallbackCalls != 0 {
		t.Fatal("restart lost Synara's explicit selection", got, err, fallbackCalls)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": ""}, 200)
	if got, err := a.launchRuntime().resolve("synara", ""); err != nil || got != automatic || fallbackCalls != 1 {
		t.Fatal("empty Synara selection did not restore automatic discovery", got, err, fallbackCalls)
	}
	if a.cliExecutable("codex-cli") != codex {
		t.Fatal("resetting Synara changed another executable selection")
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": selected}, 200)
	if err := os.RemoveAll(selected); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("synara", ""); err == nil || got != "" || fallbackCalls != 1 {
		t.Fatal("missing selected Synara silently fell back", got, err, fallbackCalls)
	}
	if stored, err := readSettings(a.dir); err != nil || stored.SynaraExecutable != selected {
		t.Fatal("missing Synara prevented settings recovery", stored, err)
	}
}

func TestCLIExecutablePreferencesSynaraStrictInputBusyAndFailedSave(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	a.launcher.platform = synaraTestPlatform("macos")
	for _, body := range []string{`{"version":1,"synaraExecutable":42}`, `{"version":1,"synaraExecutable":"relative/synara"}`, `{"version":1,"synaraExecutable":"~/Synara.app"}`, `{"version":1,"synaraExecutable":"/tmp/Synara\n.app"}`} {
		if w := adminRequest(a, "clients/cli/preferences", body); w.Code != 400 {
			t.Fatal("invalid Synara selection accepted", body, w.Code)
		}
	}
	for _, path := range []string{"/tmp/bad\x00path", "/" + strings.Repeat("x", 8192), filepath.Join(t.TempDir(), "missing"), t.TempDir(), cliPreferencesTestExecutable(t, "unrelated executable")} {
		postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": path}, 400)
	}
	selected := syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform)
	previous := a.config
	a.launchMu.Lock()
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": selected}, 409)
	a.launchMu.Unlock()
	if !reflect.DeepEqual(a.config, previous) || a.cliExecutable("synara") != "" {
		t.Fatal("busy preparation changed Synara's selection")
	}
	if err := os.Mkdir(filepath.Join(a.dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": selected}, 500)
	if !reflect.DeepEqual(a.config, previous) || a.cliExecutable("synara") != "" {
		t.Fatal("failed persistence activated a Synara selection")
	}
}

func TestCLIExecutablePreferencesSynaraResolverUsesSeparateSavedSnapshot(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	a.launcher.platform = synaraTestPlatform("macos")
	paths := [2]string{syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform), syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform)}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": paths[0]}, 200)
	// Preparation holds app.mu while consulting executable paths. The resolver
	// must read the separate persisted snapshot without reacquiring that lock.
	a.mu.Lock()
	got, err := a.launchRuntime().resolve("synara", "")
	a.mu.Unlock()
	if err != nil || got != paths[0] {
		t.Fatal("Synara's saved-path snapshot was unavailable during preparation", got, err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	errors := make(chan string, 1)
	go func() {
		defer workers.Done()
		for range 100 {
			got, err := a.launchRuntime().resolve("synara", "")
			if err != nil || got != paths[0] && got != paths[1] {
				errors <- "Synara resolver lost the atomic saved-path snapshot"
				return
			}
		}
	}()
	for i := range 10 {
		postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": paths[i%2]}, 200)
	}
	workers.Wait()
	select {
	case err := <-errors:
		t.Fatal(err)
	default:
	}
}

func TestCLIExecutablePreferencesSynaraManagedCurrentIntegrityAndNoFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installations")
	}
	a := launchTestApp(t)
	a.config.Port = 8877
	a.launcher.platform = "macos"
	makeRecord := func(token string) managedToolRecord {
		root := filepath.Join(a.dir, "tools", "synara", "releases", token)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		executable := syntheticSynaraExecutable(t, root, a.launcher.platform)
		digest, err := managedToolFingerprint(executable, runtime.GOOS)
		if err != nil {
			t.Fatal(err)
		}
		return managedToolRecord{Schema: 1, Client: "synara", Version: synaraTestVersion, Channel: "beta", Root: root, Executable: executable, Digest: digest}
	}
	current := makeRecord("current")
	if err := writeManagedTool(a.dir, current); err != nil {
		t.Fatal(err)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": current.Executable}, 200)
	retained := makeRecord("retained")
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": retained.Executable}, 400)
	alias := filepath.Join(t.TempDir(), "external-managed-alias.app")
	if err := os.Symlink(current.Executable, alias); err != nil {
		t.Fatal(err)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": alias}, 400)
	fallbackCalls := 0
	a.launcher.resolve = func(string, string) (string, error) {
		fallbackCalls++
		return retained.Executable, nil
	}
	if got, err := a.launchRuntime().resolve("synara", ""); err != nil || got != current.Executable || fallbackCalls != 0 {
		t.Fatal("verified current Synara selection was rejected", got, err, fallbackCalls)
	}
	native := managedToolBundleExecutable(current.Executable)
	if err := os.WriteFile(native, []byte("changed native executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("synara", ""); err == nil || got != "" || fallbackCalls != 0 {
		t.Fatal("corrupt managed Synara selection bypassed its digest or fell back", got, err, fallbackCalls)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": current.Executable}, 400)
	if a.cliExecutable("synara") != current.Executable {
		t.Fatal("invalid replacement changed the previous selection")
	}
}

func TestCLIExecutablePreferencesSynaraAppImageIsSavedWithoutExecuting(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	a.launcher.platform = "linux"
	path := filepath.Join(t.TempDir(), "Synara-1.0.3-x86_64.AppImage")
	header := make([]byte, 64)
	copy(header, "\x7fELF")
	header[4], header[5] = 2, 1
	copy(header[8:], []byte{'A', 'I', 2})
	// A raw package does not need execution permission to be detected. No
	// application or extraction process may be started to save this choice.
	if err := os.WriteFile(path, header, 0600); err != nil {
		t.Fatal(err)
	}
	starts := 0
	a.launcher.start = func(clientLaunchPlan) error { starts++; return nil }
	postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": path}, 200)
	if got, err := a.launchRuntime().resolve("synara", ""); err != nil || got != path {
		t.Fatal("saved raw AppImage was not detected as a package", got, err)
	}
	if _, err := synaraPackageMetadata(path, a.launcher.platform); err == nil || err.Error() != synaraAppImagePending {
		t.Fatal("raw AppImage was treated as a runnable prepared installation", err)
	}
	if starts != 0 {
		t.Fatal("saving or detecting an AppImage started an application")
	}
	if data, err := os.ReadFile(path); err != nil || !reflect.DeepEqual(data, header) {
		t.Fatal("AppImage source was changed", err)
	}
}
