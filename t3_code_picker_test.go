package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestT3CodePreparesPrivateClaudePickerAndChecksOwnedPolicy(t *testing.T) {
	for _, version := range []string{t3CodeSupportedVersion, t3CodeNightlyVersion} {
		t.Run(version, func(t *testing.T) {
			a := t3CodeTestApp(t, "macos", version)
			paths := t3CodePaths(a.dir)
			prepareT3CodeFixture(t, a)
			data, err := readCatalogFile(paths.ClientSettings)
			if err != nil || !t3CodeClientSettingsReady(data, testModelLibrary()) {
				t.Fatal("private picker was not prepared", err)
			}
			info, _ := os.Stat(paths.ClientSettings)
			if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
				t.Fatal("client settings permissions", info.Mode())
			}
			saved, err := a.readT3CodePrepared()
			if err != nil {
				t.Fatal(err)
			}
			for relative := range saved.Files {
				if filepath.Join(paths.Root, relative) == paths.ClientSettings {
					t.Fatal("mutable client preferences were hashed as immutable profiles")
				}
			}
			var document map[string]json.RawMessage
			_ = json.Unmarshal(data, &document)
			document["theme"] = json.RawMessage(`"dark"`)
			document["futurePreference"] = json.RawMessage(`9007199254740993`)
			data, _ = json.Marshal(document)
			_ = os.WriteFile(paths.ClientSettings, data, 0600)
			binary, _ := a.launcher.resolve("t3-code", "")
			if !a.t3CodeReady(saved, binary, *a.launcher) {
				t.Fatal("ordinary client preferences invalidated prepared profiles")
			}
			var preferences map[string]json.RawMessage
			_ = json.Unmarshal(document["providerModelPreferences"], &preferences)
			var claude map[string]json.RawMessage
			_ = json.Unmarshal(preferences[t3CodeClaudeProxyID], &claude)
			claude["hiddenModels"] = json.RawMessage(`[]`)
			preferences[t3CodeClaudeProxyID], _ = json.Marshal(claude)
			document["providerModelPreferences"], _ = json.Marshal(preferences)
			data, _ = json.Marshal(document)
			_ = os.WriteFile(paths.ClientSettings, data, 0600)
			if a.t3CodeReady(saved, binary, *a.launcher) {
				t.Fatal("an exposed builtin still counted as a prepared picker")
			}
			a.t3CodeCheckRunning = func(string) (bool, error) { return true, nil }
			if w := adminRequest(a, "clients/t3-code", `{}`); w.Code != 409 {
				t.Fatal("running workspace was modified", w.Code)
			}
			a.t3CodeCheckRunning = func(string) (bool, error) { return false, nil }
			prepareT3CodeFixture(t, a)
			data, _ = readCatalogFile(paths.ClientSettings)
			if !bytes.Contains(data, []byte(`9007199254740993`)) || !bytes.Contains(data, []byte(`"dark"`)) || !a.t3CodeReady(mustReadT3CodePrepared(t, a), binary, *a.launcher) {
				t.Fatal("repreparation lost unrelated preferences or failed to restore picker")
			}
		})
	}
}

func mustReadT3CodePrepared(t *testing.T, a *app) t3CodePrepared {
	t.Helper()
	saved, err := a.readT3CodePrepared()
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestT3CodeInvalidClientSettingsCannotPartiallyPrepare(t *testing.T) {
	a := t3CodeTestApp(t, "macos")
	prepareT3CodeFixture(t, a)
	paths := t3CodePaths(a.dir)
	saved := mustReadT3CodePrepared(t, a)
	before := map[string][]byte{}
	for relative := range saved.Files {
		path := filepath.Join(paths.Root, relative)
		before[path], _ = os.ReadFile(path)
	}
	before[paths.Selection], _ = os.ReadFile(paths.Selection)
	before[paths.Settings], _ = os.ReadFile(paths.Settings)
	changed := testModelLibrary()
	changed.Models = append(changed.Models, modelLibraryItem{ID: "vendor/new-picker-model"})
	changed.DefaultModel = "vendor/new-picker-model"
	if _, err := a.modelLibrary.save(changed, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	invalid := []byte(`{"providerModelPreferences":{"kilo_claude_proxy":{"hiddenModels":"invalid"}}}`)
	if err := os.WriteFile(paths.ClientSettings, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/t3-code", `{}`); w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("client settings")) {
		t.Fatal("invalid client preferences were not rejected", w.Code, w.Body.String())
	}
	before[paths.ClientSettings] = invalid
	for path, expected := range before {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			t.Fatal("failed preparation changed a file", path, err)
		}
	}
}

func TestT3CodeIncludesRefreshedPrivateClaudeManifest(t *testing.T) {
	a := t3CodeTestApp(t, "macos", t3CodeNightlyVersion)
	prepareT3CodeFixture(t, a)
	paths := t3CodePaths(a.dir)
	cache := []byte(`{"manifest":{"currentModels":{"claudeAgent":["claude-opus-6"]},"providers":{"claudeAgent":{"models":[{"slug":"claude-sonnet-6"},{"slug":"vendor/not-claude"}]}}}}`)
	if err := os.WriteFile(filepath.Join(paths.Data, "userdata", "model-manifest.json"), cache, 0600); err != nil {
		t.Fatal(err)
	}
	binary, _ := a.launcher.resolve("t3-code", "")
	saved := mustReadT3CodePrepared(t, a)
	if !a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("an upstream manifest refresh invalidated the closed private picker policy")
	}
	for _, id := range saved.Runtime.ModelAllowlist[t3CodeClaudeProxyID] {
		if id == "claude-opus-6" || id == "claude-sonnet-6" {
			t.Fatal("upstream manifest expanded the prepared Kilo policy")
		}
	}
	// Exercise the actual immutable private projection: newly refreshed builtins
	// stay outside Kilo while direct accounts retain their original model objects.
	asset, err := readT3CodeRuntimeAsset(saved.Runtime, saved.Runtime.ModelsAsset)
	if err != nil {
		t.Fatal(err)
	}
	if node, err := exec.LookPath("node"); err == nil {
		selected, _ := json.Marshal(saved.Runtime.ModelAllowlist[t3CodeClaudeProxyID])
		script := string(asset) + `
const selected=` + string(selected) + `;const models=[...selected.map(slug=>({slug})),{slug:"claude-opus-6"},{slug:"claude-sonnet-6"}];const result=project([{instanceId:"kilo_claude_proxy",driver:"claudeAgent",models},{instanceId:"kilo_claude_normal",driver:"claudeAgent",models}]);if(result[0].models.length!==selected.length||result[0].models.some(m=>!selected.includes(m.slug))||result[1].models!==models)throw Error("refreshed models escaped instance policy");`
		command := exec.Command(node, "--input-type=module", "--eval", script)
		command.Env = []string{"HOME=" + t.TempDir(), "PATH=" + filepath.Dir(node), "LANG=C"}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, output)
		}
	}
	data, _ := readCatalogFile(paths.ClientSettings)
	var document map[string]json.RawMessage
	var preferences map[string]json.RawMessage
	var claude map[string]json.RawMessage
	_ = json.Unmarshal(data, &document)
	_ = json.Unmarshal(document["providerModelPreferences"], &preferences)
	_ = json.Unmarshal(preferences[t3CodeClaudeProxyID], &claude)
	claude["hiddenModels"] = json.RawMessage(`[]`)
	preferences[t3CodeClaudeProxyID], _ = json.Marshal(claude)
	document["providerModelPreferences"], _ = json.Marshal(preferences)
	data, _ = json.Marshal(document)
	if err := os.WriteFile(paths.ClientSettings, data, 0600); err != nil {
		t.Fatal(err)
	}
	if a.t3CodeReady(saved, binary, *a.launcher) {
		t.Fatal("upstream refresh bypassed an alteration of owned hidden-model preferences")
	}
	prepareT3CodeFixture(t, a)
	data, _ = readCatalogFile(paths.ClientSettings)
	if !bytes.Contains(data, []byte(`"claude-opus-6"`)) || !bytes.Contains(data, []byte(`"claude-sonnet-6"`)) || bytes.Contains(data, []byte(`"vendor/not-claude"`)) {
		t.Fatal("cached manifest policy was not restricted to Claude builtins", string(data))
	}
}

func TestT3CodeRejectsModelsThatItsPickerWouldTruncate(t *testing.T) {
	a := t3CodeTestApp(t, "macos")
	library := modelLibrary{SchemaVersion: 1}
	for i := 0; i < 32; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: fmt.Sprintf("vendor/model-%d", i)})
	}
	library.DefaultModel = library.Models[31].ID
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	prepareT3CodeFixture(t, a)
	paths := t3CodePaths(a.dir)
	before, _ := os.ReadFile(paths.Selection)
	clientSettings, _ := readCatalogFile(paths.ClientSettings)
	parsed, err := readT3CodeClientSettings(clientSettings)
	if err != nil || parsed.order[0] != library.DefaultModel {
		t.Fatal("default at index 31 was not first in the picker", err)
	}
	library.Models = append(library.Models, modelLibraryItem{ID: "vendor/model-32"})
	library.DefaultModel = "vendor/model-32"
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	w := adminRequest(a, "clients/t3-code", `{}`)
	if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("32 shared models")) {
		t.Fatal("a truncated T3 library was accepted", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(paths.Selection)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected library changed the prepared selection")
	}
}
