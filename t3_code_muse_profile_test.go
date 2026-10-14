package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func t3CodeMuseKiloTestApp(t *testing.T) (*app, string, *int) {
	t.Helper()
	a := t3CodeTestApp(t, runtime.GOOS, t3CodeNightlyVersion)
	a.config.T3Code = t3CodeMuseOnlyPreferences("kilo")
	binary := mustResolveT3CodeTest(t, a)
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
	files["apps/server/dist/bin.mjs"] = append(files["apps/server/dist/bin.mjs"], []byte("\n"+t3CodeMuseKiloContractFixture())...)
	writeT3CodeFixtureASAR(t, archivePath, files)
	muse := filepath.Join(a.launcher.home, "Muse custom selected")
	if runtime.GOOS == "windows" {
		muse += ".exe"
	}
	if err := os.WriteFile(muse, []byte("synthetic Muse executable; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	resolve := a.launcher.resolve
	a.launcher.resolve = func(id, custom string) (string, error) {
		if id == "muse" {
			return muse, nil
		}
		if id == "codex-cli" || id == "claude" {
			t.Fatal("disabled Codex or Claude was discovered", id)
		}
		return resolve(id, custom)
	}
	probes := new(int)
	a.museVersionProbe = func(path string) (string, error) {
		*probes++
		if path != muse {
			t.Fatal("selected Muse executable changed", path)
		}
		return "1.4.6-R6000.1", nil
	}
	t.Cleanup(a.requestQuit)
	return a, binary, probes
}

func TestT3CodeMuseKiloProfilesExactModelsScopedAuthAndReadiness(t *testing.T) {
	a, binary, probes := t3CodeMuseKiloTestApp(t)
	t.Setenv("META_API_KEY", "synthetic-native-Meta-account-not-copied")
	library := testModelLibrary()
	library.Models[1].ReasoningEffort = "low"
	state := a.modelLibrary.snapshot()
	if _, err := a.modelLibrary.save(library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	if len(saved.Files) != 3 || !reflect.DeepEqual(saved.MuseLibrary, library) || len(saved.CodexLibrary.Models) != 0 || len(saved.ClaudeLibrary.Models) != 0 || !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 {
		t.Fatal("Muse-only account was not prepared independently", saved, *probes)
	}
	if saved.Runtime.MuseReasoningDefaults["vendor/one"] != "low" || !reflect.DeepEqual(saved.Runtime.MuseReasoningLevels, map[string][]string{"vendor/one": {"low", "high"}, "vendor/two": {}}) {
		t.Fatal("saved reasoning/default selection was lost", saved.Runtime)
	}
	paths := t3CodePaths(a.dir)
	home, settingsPath, authPath := t3CodeMusePaths(paths.Root)
	settings, _ := os.ReadFile(settingsPath)
	auth, _ := os.ReadFile(authPath)
	var config struct {
		Model   string                 `json:"model"`
		Catalog []museKiloCatalogModel `json:"model_catalog"`
	}
	if json.Unmarshal(settings, &config) != nil || config.Model != library.DefaultModel || len(config.Catalog) != 2 || config.Catalog[0].ModelID != "vendor/two" || len(config.Catalog[0].ReasoningEffortVariants) != 0 || config.Catalog[1].ModelID != "vendor/one" {
		t.Fatal("private native Muse catalog changed model identities or invented levels", string(settings))
	}
	token := museProxyToken(a.config.LocalKey, "t3")
	if !bytes.Contains(auth, []byte(token)) || bytes.Contains(auth, []byte(a.config.LocalKey)) || bytes.Contains(auth, []byte("synthetic-native-Meta-account-not-copied")) {
		t.Fatal("private auth did not contain only its scoped local credential")
	}
	muse := saved.Providers[t3CodeMuseProxyID].(map[string]any)
	if muse["driver"] != "muse" || muse["enabled"] != true || saved.Providers["muse"].(map[string]any)["enabled"] != false {
		t.Fatal("native and Kilo accounts were not separated", saved.Providers)
	}
	for _, raw := range muse["environment"].([]any) {
		variable := raw.(map[string]any)
		if variable["name"] == "HOME" && variable["value"] != home || variable["sensitive"] == true && (variable["name"] != t3CodeMuseKey || variable["value"] != "" || variable["valueRedacted"] != true) || strings.Contains(fmt.Sprint(variable["value"]), token) {
			t.Fatal("T3 provider settings acquired account credentials", variable)
		}
	}
	before := clientInstallationTree(t, paths.Root)
	for i := 0; i < 2; i++ {
		response := adminRequest(a, "clients/t3-code", "")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"prepared":true`) || !strings.Contains(response.Body.String(), `"museKiloSupported":true`) || bytes.Contains(response.Body.Bytes(), []byte(token)) || bytes.Contains(response.Body.Bytes(), []byte(a.config.LocalKey)) {
			t.Fatal("Muse readiness metadata leaked a token or lost its preparation", response.Code, response.Body.String())
		}
	}
	if *probes != 1 || !reflect.DeepEqual(before, clientInstallationTree(t, paths.Root)) {
		t.Fatal("readiness spawned Muse or rewrote profiles", *probes)
	}
	for _, path := range []string{settingsPath, authPath} {
		old, _ := os.ReadFile(path)
		if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
			t.Fatal(err)
		}
		if a.t3CodeReady(saved, binary, a.launchRuntime()) {
			t.Fatal("tampered active Muse profile remained ready", path)
		}
		if err := os.WriteFile(path, old, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if a.t3CodeReady(saved, binary, a.launchRuntime()) {
			t.Fatal("missing active Muse profile remained ready", path)
		}
		if err := os.WriteFile(path, old, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 {
		t.Fatal("restored exact files required a process or remained stale")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(authPath, 0644); err != nil {
			t.Fatal(err)
		}
		if a.t3CodeReady(saved, binary, a.launchRuntime()) {
			t.Fatal("publicly readable Muse auth remained ready")
		}
		prepareT3CodeFixture(t, a)
		info, err := os.Stat(authPath)
		if err != nil || info.Mode().Perm() != 0600 || !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 2 {
			t.Fatal("explicit preparation did not repair private auth permissions", err, *probes)
		}
	}
}

func TestT3CodeMuseKiloSwitchRetainsNativeAccountChatsAndDisabledFiles(t *testing.T) {
	a, binary, probes := t3CodeMuseKiloTestApp(t)
	prepareT3CodeFixture(t, a)
	paths := t3CodePaths(a.dir)
	home, settingsPath, authPath := t3CodeMusePaths(paths.Root)
	chat := filepath.Join(home, ".local", "share", "muse", "synthetic-old-chat")
	if err := os.WriteFile(chat, []byte("retain synthetic chat"), 0600); err != nil {
		t.Fatal(err)
	}
	settingsBefore, _ := os.ReadFile(settingsPath)
	if err := os.WriteFile(authPath, []byte("malformed old disabled auth; do not read"), 0600); err != nil {
		t.Fatal(err)
	}
	prefs := a.config.T3Code
	prefs.MuseConnection = "native"
	data, _ := json.Marshal(prefs)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	if len(saved.Files) != 0 || len(saved.Library.Models) != 0 || len(saved.MuseLibrary.Models) != 0 || !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 || saved.Providers["muse"].(map[string]any)["enabled"] != true || saved.Providers[t3CodeMuseProxyID].(map[string]any)["enabled"] != false {
		t.Fatal("native switch retained Kilo dependency or probed Muse", saved, *probes)
	}
	settingsAfter, _ := os.ReadFile(settingsPath)
	authAfter, _ := os.ReadFile(authPath)
	if !bytes.Equal(settingsBefore, settingsAfter) || string(authAfter) != "malformed old disabled auth; do not read" {
		t.Fatal("switching to native read or rewrote disabled private files")
	}
	prefs.MuseConnection = "kilo"
	prefs.Providers = []string{}
	data, _ = json.Marshal(prefs)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	prepareT3CodeFixture(t, a)
	saved = mustReadT3CodePrepared(t, a)
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 {
		t.Fatal("disabled Kilo Muse required its stale files", *probes)
	}
	prefs.Providers = []string{"muse"}
	data, _ = json.Marshal(prefs)
	if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	prepareT3CodeFixture(t, a)
	saved = mustReadT3CodePrepared(t, a)
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 2 || saved.MuseLibrary.DefaultModel != testModelLibrary().DefaultModel {
		t.Fatal("reenabled Muse did not require fresh preparation", saved, *probes)
	}
	if data, err := os.ReadFile(chat); err != nil || string(data) != "retain synthetic chat" {
		t.Fatal("connection switch lost a conversation", err)
	}
}

func TestT3CodeMuseKiloRejectsVersionChangesAndUnsafeState(t *testing.T) {
	a, binary, _ := t3CodeMuseKiloTestApp(t)
	muse, _ := a.launcher.resolve("muse", "")
	a.museVersionProbe = func(string) (string, error) { return "1.4.8-unknown", nil }
	if response := adminRequest(a, "clients/t3-code", `{}`); response.Code != 409 {
		t.Fatal("unverified Muse release accepted", response.Code, response.Body.String())
	}
	a.museVersionProbe = func(string) (string, error) {
		if err := os.WriteFile(muse, []byte("changed while probing"), 0700); err != nil {
			t.Fatal(err)
		}
		return "1.4.6-R6000.1", nil
	}
	if response := adminRequest(a, "clients/t3-code", `{}`); response.Code != 409 || !strings.Contains(response.Body.String(), "changed") {
		t.Fatal("changed executable inherited the previous version proof", response.Code, response.Body.String())
	}
	a.museVersionProbe = func(string) (string, error) { return "1.4.6-R6000.1", nil }
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	home, _, _ := t3CodeMusePaths(t3CodePaths(a.dir).Root)
	if runtime.GOOS == "windows" {
		return // Symbolic-link privileges are runner-specific; other guards still run.
	}
	for _, dir := range museKiloStateDirectories(home) {
		old := dir + ".synthetic-retained"
		if err := os.Rename(dir, old); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatal(err)
		}
		if a.t3CodeReady(saved, binary, a.launchRuntime()) {
			t.Fatal("private Muse state symlink remained ready", dir)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(old, dir); err != nil {
			t.Fatal(err)
		}
	}
	if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("restored exact private directories remained stale")
	}
}

func TestT3CodeMuseKiloMoreThan32AndNormalOverlapRejected(t *testing.T) {
	a, binary, _ := t3CodeMuseKiloTestApp(t)
	library := emptyModelLibrary()
	for i := 0; i < 40; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/model-%d", i)})
	}
	library.DefaultModel = library.Models[35].ID
	state := a.modelLibrary.snapshot()
	if _, err := a.modelLibrary.save(library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	prepareT3CodeFixture(t, a)
	saved := mustReadT3CodePrepared(t, a)
	if len(saved.MuseLibrary.Models) != 40 || saved.MuseLibrary.DefaultModel != library.DefaultModel || !a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("native Muse was silently truncated to another driver's limit", saved)
	}
	home, _, _ := t3CodeMusePaths(t3CodePaths(a.dir).Root)
	t.Setenv("MUSE_HOME", home)
	before := clientInstallationTree(t, t3CodePaths(a.dir).Root)
	if a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("normal Meta home overlap remained ready")
	}
	response := adminRequest(a, "clients/t3-code", `{}`)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "separate") || !reflect.DeepEqual(before, clientInstallationTree(t, t3CodePaths(a.dir).Root)) {
		t.Fatal("normal Meta home overlap was written", response.Code, response.Body.String())
	}
	a.launcher.resolve = func(string, string) (string, error) { return "", errors.New("selected executable missing") }
	if a.t3CodeReady(saved, binary, a.launchRuntime()) {
		t.Fatal("missing explicit executable fell back to another Muse installation")
	}
}

func TestT3CodeMuseKiloPinsFileStoreWithoutChangingNativeAccount(t *testing.T) {
	for _, ambient := range []string{"keychain", "none"} {
		t.Run(ambient, func(t *testing.T) {
			t.Setenv("TBH_CREDENTIAL_BACKEND", ambient)
			a, binary, probes := t3CodeMuseKiloTestApp(t)
			prepareT3CodeFixture(t, a)
			saved := mustReadT3CodePrepared(t, a)
			if !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 {
				t.Fatal("file-backed Kilo Muse was not ready", *probes)
			}
			provider := saved.Providers[t3CodeMuseProxyID].(map[string]any)
			var backend map[string]any
			for _, raw := range provider["environment"].([]any) {
				variable := raw.(map[string]any)
				if variable["name"] == "TBH_CREDENTIAL_BACKEND" {
					if backend != nil {
						t.Fatal("duplicate credential-store selector")
					}
					backend = variable
				}
			}
			if backend == nil || backend["value"] != "file" || backend["sensitive"] != false {
				t.Fatal("ambient credential backend replaced the private file store", backend)
			}
			before := clientInstallationTree(t, t3CodePaths(a.dir).Root)
			for _, value := range []string{"", "keychain", "none"} {
				backend["value"] = value
				if a.t3CodeReady(saved, binary, a.launchRuntime()) {
					t.Fatal("tampered credential-store environment remained ready", value)
				}
			}
			backend["value"] = "file"
			if !a.t3CodeReady(saved, binary, a.launchRuntime()) || *probes != 1 || !reflect.DeepEqual(before, clientInstallationTree(t, t3CodePaths(a.dir).Root)) {
				t.Fatal("readiness probed Muse or rewrote files while checking its store", *probes)
			}
			prefs := a.config.T3Code
			prefs.MuseConnection = "native"
			data, _ := json.Marshal(prefs)
			if response := adminRequest(a, "clients/t3-code/preferences", string(data)); response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			prepareT3CodeFixture(t, a)
			native := mustReadT3CodePrepared(t, a)
			if !a.t3CodeReady(native, binary, a.launchRuntime()) || *probes != 1 || native.Providers["muse"].(map[string]any)["enabled"] != true {
				t.Fatal("native Meta account acquired a Kilo preparation or probe", *probes)
			}
			for _, id := range []string{"muse", t3CodeMuseProxyID} {
				for _, raw := range native.Providers[id].(map[string]any)["environment"].([]any) {
					if raw.(map[string]any)["name"] == "TBH_CREDENTIAL_BACKEND" {
						t.Fatal("Kilo store selection leaked into a native or disabled account", id)
					}
				}
			}
			if value := os.Getenv("TBH_CREDENTIAL_BACKEND"); value != ambient {
				t.Fatal("preparation changed the parent account's store selection", value)
			}
		})
	}
}
