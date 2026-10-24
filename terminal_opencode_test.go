package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestTerminalOpenCodeRefreshesSharedModelsAndPreservesMCP(t *testing.T) {
	a := terminalTestApp(t)
	a.config.Port = 8899
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image"}
	root, path, savedPath, err := a.editorPaths("opencode")
	if err != nil || safeEditorDir(root, filepath.Dir(path)) != nil {
		t.Fatal("cannot prepare temporary OpenCode profile", err)
	}
	original := []byte(`{
 // Keep this comment and other providers.
 "permission":{"bash":"ask"},
 "mcp":{"docs":{"type":"local","command":["my-mcp","with spaces"]}},
 "provider":{"personal":{"npm":"@ai-sdk/openai-compatible","name":"Personal"}}
}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	prepare := func() clientLaunchPlan {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.prepareTerminalProfile("opencode", a.launcher.home, a.modelLibrary.snapshot().Library, claudeCapabilities{}); err != nil {
			t.Fatal(err)
		}
		plan := clientLaunchPlan{Client: "opencode", Env: map[string]string{}}
		if err := a.launchProfile(&plan, a.launcher.home); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first := prepare()
	if !reflect.DeepEqual(first.Args, []string{"--model", "kilo-local/vendor/two"}) || !reflect.DeepEqual(first.Env, map[string]string{"OPENCODE_CONFIG": path}) || !reflect.DeepEqual(first.Unset, []string{"OPENCODE_CONFIG_CONTENT"}) {
		t.Fatal("OpenCode terminal plan differs from the scoped launcher", first)
	}
	if !bytes.Equal(original, terminalInstallerRead(t, path+".bak")) {
		t.Fatal("OpenCode settings did not receive an exact backup")
	}
	config := terminalInstallerRead(t, path)
	for _, expected := range []string{"Keep this comment", "my-mcp", "Personal", "permission", "My First", "My Second", "kilo_images", "http://127.0.0.1:8899/mcp/images", a.config.LocalKey} {
		if !bytes.Contains(config, []byte(expected)) {
			t.Fatalf("OpenCode profile lost %q", expected)
		}
	}
	if bytes.Contains(config, []byte(a.apiKey)) {
		t.Fatal("upstream credential written to OpenCode profile")
	}
	var selection editorSelection
	if json.Unmarshal(terminalInstallerRead(t, savedPath), &selection) != nil || len(selection.Models) != 2 || selection.Models[0].ID != "vendor/one" || selection.Models[1].ID != "vendor/two" || selection.Initial != "vendor/two" {
		t.Fatal("shared model IDs, order or default lost")
	}
	state := a.modelLibrary.snapshot()
	state.Library.DefaultModel = "vendor/one"
	state.Library.Models[0].DisplayName = "Updated from Models"
	state.Library.Models[0].ContextWindow = 0
	if _, err := a.modelLibrary.save(state.Library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	a.config.ImageGeneration.Enabled = false
	a.config.LocalKey = "changed-synthetic-local-key"
	second := prepare()
	config = terminalInstallerRead(t, path)
	if !reflect.DeepEqual(second.Args, []string{"--model", "kilo-local/vendor/one"}) || !bytes.Contains(config, []byte("Updated from Models")) || !bytes.Contains(config, []byte(a.config.LocalKey)) || bytes.Contains(config, []byte("kilo_images")) || !bytes.Contains(config, []byte("my-mcp")) {
		t.Fatal("OpenCode reused stale models, credentials or image MCP settings")
	}
	if json.Unmarshal(terminalInstallerRead(t, savedPath), &selection) != nil || selection.Models[0].Context != 272000 {
		t.Fatal("OpenCode did not apply the shared recommended context")
	}
	for _, name := range []string{path, savedPath} {
		info, err := os.Stat(name)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatal("OpenCode profile is not private", err)
		}
	}
}

func TestTerminalOpenCodeRejectsUntrustedExecutionParameters(t *testing.T) {
	a := terminalTestApp(t)
	cleanup, err := a.publishTerminalRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := readTerminalRuntime(a.dir)
	_, config, _, _ := a.editorPaths("opencode")
	if err != nil || info.OpenCodeConfig != config {
		t.Fatal("missing trusted OpenCode profile location", err)
	}
	valid := func() clientLaunchPlan {
		return clientLaunchPlan{Client: "opencode", Name: "OpenCode", Kind: "terminal", Env: map[string]string{"OPENCODE_CONFIG": config}, Unset: []string{"OPENCODE_CONFIG_CONTENT"}}
	}
	if err := validateTerminalProfile(valid(), info); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*clientLaunchPlan){
		"executable":        func(p *clientLaunchPlan) { p.Executable = "/bin/sh" },
		"extra argument":    func(p *clientLaunchPlan) { p.Args = append(p.Args, "--port", "9999") },
		"injected model":    func(p *clientLaunchPlan) { p.Args = []string{"--model", "kilo-local/vendor/two"} },
		"injected command":  func(p *clientLaunchPlan) { p.Args = []string{"models", "kilo-local"} },
		"wrong profile":     func(p *clientLaunchPlan) { p.Env["OPENCODE_CONFIG"] = "/other" },
		"inline override":   func(p *clientLaunchPlan) { p.Env["OPENCODE_CONFIG_CONTENT"] = "{}" },
		"extra environment": func(p *clientLaunchPlan) { p.Env["LD_PRELOAD"] = "/tmp/injected.so" },
		"missing reset":     func(p *clientLaunchPlan) { p.Unset = nil },
		"extra reset":       func(p *clientLaunchPlan) { p.Unset = append(p.Unset, "PATH") },
	} {
		t.Run(name, func(t *testing.T) {
			plan := valid()
			mutate(&plan)
			if validateTerminalProfile(plan, info) == nil {
				t.Fatal("untrusted response accepted")
			}
		})
	}
	older := info
	older.OpenCodeConfig = ""
	if validateTerminalRuntime(older) != nil || validateTerminalProfile(valid(), older) == nil {
		t.Fatal("older descriptors must remain valid but cannot authorize OpenCode")
	}
	for _, path := range []string{"relative", "/tmp/../other", "/tmp/config\n"} {
		invalid := info
		invalid.OpenCodeConfig = path
		if validateTerminalRuntime(invalid) == nil {
			t.Fatal("invalid trusted path accepted", path)
		}
	}
}

func TestTerminalOpenCode2UsesOwnProfileAndOpenCodePackSlot(t *testing.T) {
	a := openCode2TestApp(t)
	a.config.Port = 8899
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image"}
	root, v1path, _, err := a.editorPaths("opencode")
	if err != nil || safeEditorDir(root, filepath.Dir(v1path)) != nil {
		t.Fatal("cannot prepare temporary OpenCode 1 profile", err)
	}
	v1 := []byte("OpenCode 1 retained\n")
	if err := os.WriteFile(v1path, v1, 0600); err != nil {
		t.Fatal(err)
	}
	_, config, selection := a.openCode2Paths()
	if err := safeEditorDir(a.dir, filepath.Dir(config)); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{
 // Keep this comment and the user's own servers.
 "mcp":{"docs":{"type":"local","command":["my-mcp","with spaces"]}},
 "providers":{"personal":{"name":"Personal"}}
}`)
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	catalog, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.config.OrgID, Models: []modelInfo{
		{ID: "vendor/pack", Name: "Pack pick", ContextWindow: 128000, MaxOutputTokens: 4096},
		{ID: "vendor/one", Name: "Shared pick", ContextWindow: 200000, MaxOutputTokens: 32000},
	}})
	if err := atomicCatalogFile(filepath.Join(a.dir, "model-catalog.json"), catalog); err != nil {
		t.Fatal(err)
	}
	file := emptyModelPacksFile()
	file.Personal = []personalModelPack{{ID: "mine-opencode", Name: "OpenCode pick", Library: modelLibrary{
		SchemaVersion: 1, DefaultModel: "vendor/pack", Models: []modelLibraryItem{{ID: "vendor/pack", ContextPreset: contextPresetRecommended}},
	}}}
	file.Assignments["opencode"] = "mine-opencode"
	if err := newModelPacksStore(a.dir).save(file, false); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	library, libraryErr := a.terminalModelLibrary("opencode2", a.modelLibrary.snapshot().Library)
	if libraryErr == nil {
		libraryErr = a.prepareTerminalProfile("opencode2", a.launcher.home, library, claudeCapabilities{})
	}
	a.mu.Unlock()
	if libraryErr != nil {
		t.Fatal(libraryErr)
	}
	prepared := terminalInstallerRead(t, config)
	for _, expected := range []string{"Keep this comment", "my-mcp", "Personal", "vendor/pack", "kilo-local/vendor/pack"} {
		if !bytes.Contains(prepared, []byte(expected)) {
			t.Fatalf("OpenCode 2 profile lost %q", expected)
		}
	}
	if bytes.Contains(prepared, []byte(`"vendor/one"`)) {
		t.Fatal("OpenCode 2 ignored the pack assigned to the OpenCode slot")
	}
	saved := terminalInstallerRead(t, selection)
	if !bytes.Contains(saved, []byte("vendor/pack")) || bytes.Contains(saved, []byte("vendor/one")) {
		t.Fatal("OpenCode 2 selection did not use the assigned pack", string(saved))
	}
	if data := terminalInstallerRead(t, v1path); !bytes.Equal(data, v1) {
		t.Fatal("OpenCode 2 preparation changed the OpenCode 1 profile")
	}
}

func TestTerminalOpenCode2RejectsUntrustedExecutionParameters(t *testing.T) {
	a := terminalTestApp(t)
	cleanup, err := a.publishTerminalRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := readTerminalRuntime(a.dir)
	home, _, _ := a.openCode2Paths()
	if err != nil || info.OpenCode2Home != home {
		t.Fatal("missing trusted OpenCode 2 home", err)
	}
	valid := func() clientLaunchPlan {
		env := map[string]string{"HOME": home, "KILO_OPENCODE2_API_KEY": "synthetic-opencode2-key"}
		for _, entry := range t3CodeOptionalProviderEnvironment(home, nil, "linux") {
			env[entry["name"].(string)] = entry["value"].(string)
		}
		return clientLaunchPlan{Client: "opencode2", Name: "OpenCode 2", Kind: "terminal", Env: env, Unset: openCode2UnsetEnvironment(nil)}
	}
	if err := validateTerminalProfile(valid(), info); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*clientLaunchPlan){
		"executable":           func(p *clientLaunchPlan) { p.Executable = "/bin/sh" },
		"extra argument":       func(p *clientLaunchPlan) { p.Args = append(p.Args, "models") },
		"standalone argument":  func(p *clientLaunchPlan) { p.Args = []string{"--standalone"} },
		"wrong home":           func(p *clientLaunchPlan) { p.Env["HOME"] = "/other" },
		"home outside profile": func(p *clientLaunchPlan) { p.Env["HOME"] = filepath.Join(home, "..", "outside") },
		"extra environment":    func(p *clientLaunchPlan) { p.Env["LD_PRELOAD"] = "/tmp/injected.so" },
		"missing key":          func(p *clientLaunchPlan) { delete(p.Env, "KILO_OPENCODE2_API_KEY") },
		"duplicate reset":      func(p *clientLaunchPlan) { p.Unset = append(p.Unset, p.Unset[0]) },
		"disallowed reset":     func(p *clientLaunchPlan) { p.Unset = append(p.Unset, "PATH") },
	} {
		t.Run(name, func(t *testing.T) {
			plan := valid()
			mutate(&plan)
			if validateTerminalProfile(plan, info) == nil {
				t.Fatal("untrusted response accepted")
			}
		})
	}
	older := info
	older.OpenCode2Home = ""
	if validateTerminalRuntime(older) != nil || validateTerminalProfile(valid(), older) == nil {
		t.Fatal("older descriptors must remain valid but cannot authorize OpenCode 2")
	}
	for _, path := range []string{"relative", home + "/../other", home + "/\n"} {
		invalid := info
		invalid.OpenCode2Home = path
		if validateTerminalRuntime(invalid) == nil {
			t.Fatal("invalid trusted OpenCode 2 home accepted", path)
		}
	}
}

func TestTerminalOpenCodeProtectsExistingFiles(t *testing.T) {
	for _, conflict := range []string{"settings", "selection", "directory", "malformed", "mcp conflict", "backup"} {
		t.Run(conflict, func(t *testing.T) {
			a := terminalTestApp(t)
			a.config.Port = 8899
			a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image"}
			root, path, selection, _ := a.editorPaths("opencode")
			outside := t.TempDir()
			target := filepath.Join(outside, "untouched")
			original := []byte("private external file\n")
			if err := os.WriteFile(target, original, 0600); err != nil {
				t.Fatal(err)
			}
			if conflict == "directory" {
				if err := os.Symlink(outside, filepath.Dir(path)); err != nil {
					t.Skip(err)
				}
			} else {
				if err := safeEditorDir(root, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
				switch conflict {
				case "settings", "selection", "backup":
					if conflict == "backup" {
						if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					link := map[string]string{"settings": path, "selection": selection, "backup": path + ".bak"}[conflict]
					if err := os.Symlink(target, link); err != nil {
						t.Skip(err)
					}
				case "malformed":
					if err := os.WriteFile(path, []byte(`{"provider":`), 0600); err != nil {
						t.Fatal(err)
					}
				case "mcp conflict":
					if err := os.WriteFile(path, []byte(`{"mcp":{"kilo_images":{"type":"local","command":["custom-mcp"]}}}`), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			a.mu.Lock()
			err := a.prepareTerminalProfile("opencode", a.launcher.home, a.modelLibrary.snapshot().Library, claudeCapabilities{})
			a.mu.Unlock()
			if err == nil || !bytes.Equal(original, terminalInstallerRead(t, target)) {
				t.Fatal("unsafe profile preparation succeeded or modified an external file", err)
			}
			if conflict != "selection" {
				if _, err := os.Lstat(selection); !os.IsNotExist(err) {
					t.Fatal("failed preflight changed the model selection")
				}
			}
		})
	}
}

func TestTerminalCommandsUpgradeAddsOpenCodeWithoutRewritingExistingCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command installation")
	}
	home, configDir, binary := terminalInstallerFixture(t)
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]string{"kilo-codex": "codex-cli", "kilo-claude": "claude", "kilo-omp": "omp"} {
		if err := os.WriteFile(filepath.Join(bin, name), terminalCommandScript(binary, configDir, client), 0700); err != nil {
			t.Fatal(err)
		}
	}
	startup := filepath.Join(home, ".zshrc")
	original := append([]byte("# Existing user preferences\n"), terminalPathBlock("zsh")...)
	if err := os.WriteFile(startup, original, 0600); err != nil {
		t.Fatal(err)
	}
	status, err := terminalCommandsStatus(home, configDir, binary, "zsh", "darwin")
	if err != nil || status.Installed || !status.PathConfigured {
		t.Fatal("old three-command install did not report a needed update", err)
	}
	result, err := installTerminalCommands(home, configDir, binary, "zsh", "darwin")
	if err != nil || !result.Installed || len(result.Commands) != 5 || len(result.Backups) != 0 || !bytes.Equal(original, terminalInstallerRead(t, startup)) {
		t.Fatal("upgrade changed existing commands or shell preferences", err)
	}
	if !bytes.Contains(terminalInstallerRead(t, result.Commands["kilo-opencode"]), []byte("--terminal-agent opencode")) {
		t.Fatal("kilo-opencode wrapper was not installed")
	}
}
