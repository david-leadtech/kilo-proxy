package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func ompTestSelection() ompSelection {
	return ompSelection{Initial: "vendor/reasoning", Models: []ompModel{
		{editorModel: editorModel{ID: "vendor/reasoning", Name: "My reasoning model", Context: 64000, Output: 4000}, Reasoning: true, Effort: "high", ReasoningEfforts: []string{"none", "low", "high", "ultra"}, InputModalities: []string{"text", "image"}},
		{editorModel: editorModel{ID: "vendor/fast", Name: "My fast model", Context: 32000}},
	}}
}

func prepareOMPTest(t *testing.T, a *app, s ompSelection) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return adminRequest(a, "omp/profile", string(data))
}

func TestOMPProfilePreservesPreferencesAndUpdatesManagedConnection(t *testing.T) {
	a := launchTestApp(t)
	dir := a.ompProfileDir
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	originalSettings := "# Keep my preferences\ntheme: catppuccin\nmodelRoles:\n  smol: another/tiny\nstartup:\n  quiet: true\n"
	originalModels := "# Keep my provider\nproviders:\n  another:\n    api: openai-completions\n    baseUrl: http://127.0.0.1:1234/v1\n    auth: none\n"
	for name, data := range map[string]string{"config.yml": originalSettings, "models.yml": originalModels, "mcp.json": `{"mcpServers":{"custom":{"command":"my-tool"}}}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := ompTestSelection()
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image"}
	result := prepareOMPTest(t, a, s)
	if result.Code != 200 || strings.Contains(result.Body.String(), a.config.LocalKey) {
		t.Fatal(result.Code, result.Body.String())
	}
	contents := map[string][]byte{}
	for _, name := range []string{"config.yml", "models.yml", "mcp.json", "kilo-models.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		contents[name] = data
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(filepath.Join(dir, name))
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s is not private", name)
			}
		}
	}
	if !bytes.Contains(contents["config.yml"], []byte("theme: catppuccin")) || !bytes.Contains(contents["config.yml"], []byte("smol: another/tiny")) ||
		!bytes.Contains(contents["models.yml"], []byte("# Keep my provider")) || !bytes.Contains(contents["models.yml"], []byte("another:")) {
		t.Fatal("preparation erased unrelated YAML preferences")
	}
	backup, _ := os.ReadFile(filepath.Join(dir, "config.yml.bak"))
	if string(backup) != originalSettings {
		t.Fatal("original settings backup missing")
	}
	var mcp map[string]any
	if json.Unmarshal(contents["mcp.json"], &mcp) != nil {
		t.Fatal("invalid MCP settings")
	}
	servers := object(mcp["mcpServers"])
	if servers["custom"] == nil || stringValue(object(object(servers["kilo-images"])["headers"])["Authorization"]) != "Bearer "+a.config.LocalKey {
		t.Fatal("MCP preservation or local credential incorrect")
	}
	for name, data := range contents {
		if name == "models.yml" {
			if !bytes.Contains(data, []byte(a.config.LocalKey)) || bytes.Contains(data, []byte(a.apiKey)) {
				t.Fatal("wrong credential in managed provider")
			}
		}
	}
	result = prepareOMPTest(t, a, s)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"changed":false`) {
		t.Fatal("unchanged preparation is not idempotent", result.Body.String())
	}
	plan, err := a.planClientLaunch(clientLaunchRequest{Client: "omp"}, a.launchRuntime())
	if err != nil || plan.Env["PI_CODING_AGENT_DIR"] != dir || plan.Env["OMP_PROFILE"] != "" ||
		plan.Env["PI_PROFILE"] != "" || plan.Env["PI_OPENAI_STATEFUL"] != "0" ||
		strings.Join(plan.Args, " ") != "--model kilo-local/vendor/reasoning" {
		t.Fatal("invalid isolated launch", err, plan)
	}
	a.config.LocalKey = "kl_local_rotated_for_test"
	a.config.Port = 43219
	a.config.ImageGeneration.Enabled = false
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "omp"}, a.launchRuntime()); err == nil {
		t.Fatal("stale key/port/image setup accepted")
	}
	s.Initial = "vendor/fast"
	result = prepareOMPTest(t, a, s)
	if result.Code != 200 {
		t.Fatal(result.Body.String())
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "omp"}, a.launchRuntime()); err != nil {
		t.Fatal("updated profile cannot launch", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if bytes.Contains(data, []byte("kilo-images")) || !bytes.Contains(data, []byte("custom")) {
		t.Fatal("disabling images erased other MCP servers or kept stale auth")
	}
	reloaded := adminRequest(a, "omp/profile", "")
	if reloaded.Code != 200 || !strings.Contains(reloaded.Body.String(), `"initial":"vendor/fast"`) {
		t.Fatal("updated model selection did not reload")
	}
}

func TestOMPProfileRejectsUnsafeOrAmbiguousSettingsWithoutPartialWrites(t *testing.T) {
	for _, invalid := range []string{
		"theme: [broken",
		"theme: light\ntheme: dark\n",
		"theme: light\n---\ntheme: dark\n",
		"modelRoles: not-a-map\n",
		"startup: &prefs {quiet: true}\nmodelRoles: *prefs\n",
		"modelRoles:\n  default: &main kilo-local/vendor/reasoning\n  smol: *main\n",
		"defaults: &d {startup: {quiet: true}}\n<<: *d\n",
	} {
		t.Run(invalid, func(t *testing.T) {
			a := launchTestApp(t)
			s := ompTestSelection()
			if w := prepareOMPTest(t, a, s); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			modelsBefore, _ := os.ReadFile(filepath.Join(a.ompProfileDir, "models.yml"))
			choicesBefore, _ := os.ReadFile(filepath.Join(a.ompProfileDir, "kilo-models.json"))
			if err := os.WriteFile(filepath.Join(a.ompProfileDir, "config.yml"), []byte(invalid), 0600); err != nil {
				t.Fatal(err)
			}
			s.Initial = "vendor/fast"
			result := prepareOMPTest(t, a, s)
			if result.Code != 409 {
				t.Fatal("invalid profile accepted", result.Code)
			}
			modelsAfter, _ := os.ReadFile(filepath.Join(a.ompProfileDir, "models.yml"))
			choicesAfter, _ := os.ReadFile(filepath.Join(a.ompProfileDir, "kilo-models.json"))
			if !bytes.Equal(modelsBefore, modelsAfter) || !bytes.Equal(choicesBefore, choicesAfter) {
				t.Fatal("failed preparation partially changed profile")
			}
		})
	}
	if runtime.GOOS != "windows" {
		a := launchTestApp(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, a.ompProfileDir); err != nil {
			t.Fatal(err)
		}
		if w := prepareOMPTest(t, a, ompTestSelection()); w.Code != 409 {
			t.Fatal("symlink profile accepted")
		}
		entries, _ := os.ReadDir(outside)
		if len(entries) != 0 {
			t.Fatal("wrote through profile symlink")
		}
	}
}

func TestOMPProfileAuthenticationAndCapabilityBounds(t *testing.T) {
	a := launchTestApp(t)
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "http://"+a.adminHost+"/api/omp/profile", strings.NewReader("{}"))
		w := httptest.NewRecorder()
		a.adminHandler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatal("profile endpoint allowed unauthenticated access")
		}
	}
	s := ompTestSelection()
	data, err := buildOMPModels(s, "http://127.0.0.1:8877/v1", "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	node, err := readOMPYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := node.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	provider := object(object(decoded["providers"])["kilo-local"])
	models := provider["models"].([]any)
	thinking := object(object(models[0])["thinking"])
	levels, ok := thinking["efforts"].([]any)
	if !ok || len(levels) != 2 || levels[0] != "low" || levels[1] != "high" || thinking["defaultLevel"] != "high" {
		t.Fatal("unsupported reasoning levels advertised")
	}
	if object(models[1])["reasoning"] != false || object(models[1])["thinking"] != nil || object(models[1])["maxTokens"] != 8192 {
		t.Fatal("unknown capabilities invented")
	}
	if object(models[0])["cost"] != nil {
		t.Fatal("incomplete cache prices invented")
	}
	s.Models[0].Effort = "none"
	data, _ = buildOMPModels(s, "http://127.0.0.1:8877/v1", "local-fixture")
	if bytes.Contains(data, []byte("thinking:")) {
		t.Fatal("explicitly disabled reasoning was re-enabled")
	}
	s.Models[1].ID = s.Models[0].ID
	if validateOMPSelection(s) == nil {
		t.Fatal("duplicate models accepted")
	}
}

func TestOMPLaunchCommandQuotesPathsAndScopesInheritedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix command exercised on macOS and Linux")
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile ' and $(not-a-command)")
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "models.yml"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\nprintf '%s\\n' \"$PI_CODING_AGENT_DIR\" \"$OMP_PROFILE/$PI_PROFILE\" \"$PI_OPENAI_STATEFUL\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "omp"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", ompLaunchCommand(profile, "vendor/model", "unix"))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "OMP_PROFILE=personal", "PI_PROFILE=work", "PI_OPENAI_STATEFUL=1")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != profile+"\n/\n0\n--model\nkilo-local/vendor/model\n" {
		t.Fatal("profile/path isolation failed", err, string(out))
	}
}
