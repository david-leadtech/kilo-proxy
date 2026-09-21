package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const openDesignManagedImagesFixture = `[mcp_servers.kilo_images]
url = 'http://127.0.0.1:8877/mcp/images'
bearer_token_env_var = 'KILO_LOCAL_API_KEY'
`

func openDesignReadCodexConfig(t *testing.T, dir string) ([]byte, map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return data, imagesTOML(t, data)
}

func openDesignPrepareCodexImages(t *testing.T, dir string, port int, enabled bool) {
	t.Helper()
	if err := prepareOpenDesignEngineProfile(dir, "codex-cli", testModelLibrary(), nil, claudeCapabilities{}, port, "synthetic-local-key", imageGenerationSettings{Enabled: enabled, Model: "vendor/image"}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDesignCodexImageApprovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	original := []byte(`# Keep private permissions
approval_policy = 'on-request'
sandbox_mode = 'workspace-write'
[mcp_servers.docs]
command = 'private-docs-server'
default_tools_approval_mode = 'prompt'
[mcp_servers.docs.tools.lookup]
approval_mode = 'writes'
`)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), original, 0600); err != nil {
		t.Fatal(err)
	}
	openDesignPrepareCodexImages(t, dir, 8877, true)
	first, config := openDesignReadCodexConfig(t, dir)
	servers := config["mcp_servers"].(map[string]any)
	images := servers[codexImagesServer].(map[string]any)
	wantTools := map[string]any{"generate_image": map[string]any{"approval_mode": "approve"}}
	if !reflect.DeepEqual(images["tools"], wantTools) || images["default_tools_approval_mode"] != nil {
		t.Fatal("Open Design must approve only the managed generate_image tool")
	}
	if config["approval_policy"] != "on-request" || config["sandbox_mode"] != "workspace-write" || !reflect.DeepEqual(servers["docs"], imagesTOML(t, original)["mcp_servers"].(map[string]any)["docs"]) {
		t.Fatal("image approval changed global permissions or an unrelated MCP server")
	}
	if !bytes.Contains(first, []byte("# Keep private permissions")) || bytes.Contains(first, []byte("synthetic-local-key")) {
		t.Fatal("profile lost comments or embedded the local credential")
	}
	backup, err := os.ReadFile(filepath.Join(dir, "config.toml.bak"))
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("approval migration must back up the original profile")
	}
	openDesignPrepareCodexImages(t, dir, 8877, true)
	again, _ := openDesignReadCodexConfig(t, dir)
	if !bytes.Equal(first, again) {
		t.Fatal("repeated preparation must not rewrite the same image policy")
	}
	openDesignPrepareCodexImages(t, dir, 8899, true)
	_, config = openDesignReadCodexConfig(t, dir)
	images = config["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)
	if images["url"] != "http://127.0.0.1:8899/mcp/images" || !reflect.DeepEqual(images["tools"], wantTools) {
		t.Fatal("port rotation lost the scoped image approval")
	}
	openDesignPrepareCodexImages(t, dir, 8899, false)
	_, config = openDesignReadCodexConfig(t, dir)
	servers = config["mcp_servers"].(map[string]any)
	if servers[codexImagesServer] != nil || servers["docs"] == nil || config["approval_policy"] != "on-request" || config["sandbox_mode"] != "workspace-write" {
		t.Fatal("disabling images must remove only the managed image server and its approval")
	}
}

func TestOpenDesignCodexImageApprovalPreservesExplicitPolicies(t *testing.T) {
	for _, scope := range []string{"server", "tool"} {
		for _, mode := range []string{"auto", "prompt", "writes", "approve"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				dir := t.TempDir()
				custom := "default_tools_approval_mode = '" + mode + "'\n"
				if scope == "tool" {
					custom = "[mcp_servers.kilo_images.tools.generate_image]\napproval_mode = '" + mode + "'\noutput_token_limit = 1234\n"
				}
				original := []byte(openDesignManagedImagesFixture + custom)
				if err := os.WriteFile(filepath.Join(dir, "config.toml"), original, 0600); err != nil {
					t.Fatal(err)
				}
				openDesignPrepareCodexImages(t, dir, 8899, true)
				_, config := openDesignReadCodexConfig(t, dir)
				images := config["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)
				if scope == "server" {
					if images["default_tools_approval_mode"] != mode || images["tools"] != nil {
						t.Fatal("explicit server policy must not be overridden by a new tool policy")
					}
				} else {
					want := imagesTOML(t, original)["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)["tools"]
					if !reflect.DeepEqual(images["tools"], want) {
						t.Fatal("explicit tool policy or output budget was replaced")
					}
				}
			})
		}
	}
}

func TestOpenDesignCodexImageApprovalPreservesToolRestrictions(t *testing.T) {
	for _, restriction := range []string{
		"enabled = false\n",
		"enabled_tools = ['other_tool']\n",
		"enabled_tools = []\n",
		"disabled_tools = ['generate_image']\n",
		"enabled_tools = ['generate_image']\ndisabled_tools = ['other_tool']\n",
	} {
		t.Run(restriction, func(t *testing.T) {
			dir := t.TempDir()
			original := []byte(openDesignManagedImagesFixture + restriction + `[mcp_servers.kilo_images.tools.other_tool]
approval_mode = 'prompt'
output_token_limit = 7654
[mcp_servers.kilo_images.tools.generate_image]
output_token_limit = 4321
`)
			if err := os.WriteFile(filepath.Join(dir, "config.toml"), original, 0600); err != nil {
				t.Fatal(err)
			}
			before := imagesTOML(t, original)["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)
			openDesignPrepareCodexImages(t, dir, 8899, true)
			_, config := openDesignReadCodexConfig(t, dir)
			after := config["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)
			for _, key := range []string{"enabled", "enabled_tools", "disabled_tools"} {
				if value, exists := before[key]; exists && !reflect.DeepEqual(after[key], value) {
					t.Fatalf("explicit %s restriction was changed", key)
				}
			}
			tools := after["tools"].(map[string]any)
			if !reflect.DeepEqual(tools["other_tool"], before["tools"].(map[string]any)["other_tool"]) || tools["generate_image"].(map[string]any)["output_token_limit"] != int64(4321) {
				t.Fatal("adding approval discarded another tool or custom output budget")
			}
		})
	}
}

func TestOpenDesignCodexImageApprovalDoesNotApplyToOrdinaryProfiles(t *testing.T) {
	for _, name := range []string{".codex-kilo-desktop", ".codex-kilo-cli"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			images := imageGenerationSettings{Enabled: true, Model: "vendor/image"}
			if _, _, err := saveCodexProfileOptions(dir, []byte(testCatalog), 8877, "", &images, nil); err != nil {
				t.Fatal(err)
			}
			_, config := openDesignReadCodexConfig(t, dir)
			server := config["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)
			if server["tools"] != nil || server["default_tools_approval_mode"] != nil || config["approval_policy"] != nil || config["sandbox_mode"] != nil {
				t.Fatal("Open Design-specific image approval leaked into an ordinary Codex profile")
			}
		})
	}
}

func TestOpenDesignCodexImageApprovalFailureChangesNoFiles(t *testing.T) {
	for _, malformed := range []string{"tools = 'invalid'\n", "[mcp_servers.kilo_images.tools]\ngenerate_image = false\n"} {
		t.Run(malformed, func(t *testing.T) {
			dir := t.TempDir()
			originals := map[string][]byte{
				"config.toml":     []byte(openDesignManagedImagesFixture + malformed),
				"models.json":     []byte(testCatalog),
				"config.toml.bak": []byte("# Previous backup\n"),
				"models.json.bak": []byte("{}\n"),
			}
			for name, data := range originals {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareOpenDesignEngineProfile(dir, "codex-cli", testModelLibrary(), nil, claudeCapabilities{}, 8899, "synthetic-local-key", imageGenerationSettings{Enabled: true, Model: "vendor/image"}); err == nil {
				t.Fatal("invalid MCP tool settings must fail before any profile writes")
			}
			for name, expected := range originals {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(got, expected) {
					t.Fatalf("failed approval preparation changed %s", name)
				}
			}
		})
	}
}

func TestOpenDesignCodexImageApprovalMigratesLegacyPreparedProfile(t *testing.T) {
	a := openDesignTestApp(t, "macos")
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image"}
	openDesignPrepareFixture(t, a, "codex-cli")
	paths := openDesignProfilePaths(a.dir)
	dir := filepath.Join(paths.Profiles, "codex-cli")
	saved := mustOpenDesignPrepared(t, a)
	binary, err := resolveOpenDesignCLI("codex-cli", *a.launcher)
	if err != nil {
		t.Fatal(err)
	}
	// v0.25.0 saved a valid image endpoint and file hashes, but no tool approval
	// and no generation revision in its fingerprint. Files alone cannot detect
	// that an unchanged selection now needs a configuration migration.
	data, before := openDesignReadCodexConfig(t, dir)
	after := imagesTOML(t, data)
	delete(after["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any), "tools")
	legacy, err := editCodexTOML(data, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	saved.Files["config.toml"], err = openDesignManagedFileHash(dir, "config.toml")
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint, _ := json.Marshal([]any{saved.Engine, binary, saved.Library, a.config.Port, a.config.LocalKey, a.config.OrgID, a.config.ImageGeneration, ""})
	saved.Fingerprint = openDesignHash(oldFingerprint)
	manifest, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(paths.Root, "selection.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if !a.openDesignFilesMatch(saved) || a.openDesignReady(saved, binary) {
		t.Fatal("a valid legacy manifest must require image approval migration")
	}
	beforeFiles := openDesignFixtureFiles(t, paths.Root)
	a.openDesignCheckRunning = func(string) (bool, error) { return true, nil }
	if response := adminRequest(a, "open-design/profile", `{"engine":"codex-cli"}`); response.Code != http.StatusConflict {
		t.Fatal("migration must wait until the managed Open Design instance is closed")
	}
	if !reflect.DeepEqual(beforeFiles, openDesignFixtureFiles(t, paths.Root)) {
		t.Fatal("running-instance rejection changed the private profile")
	}
	a.openDesignCheckRunning = func(string) (bool, error) { return false, nil }
	openDesignPrepareFixture(t, a, "codex-cli")
	current := mustOpenDesignPrepared(t, a)
	if current.Fingerprint == saved.Fingerprint || !a.openDesignReady(current, binary) {
		t.Fatal("repreparing the closed instance did not migrate its manifest")
	}
	_, config := openDesignReadCodexConfig(t, dir)
	tool := config["mcp_servers"].(map[string]any)[codexImagesServer].(map[string]any)["tools"].(map[string]any)["generate_image"].(map[string]any)
	if tool["approval_mode"] != "approve" {
		t.Fatal("legacy image profile did not receive the scoped approval")
	}
}
