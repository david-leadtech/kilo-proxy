package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func openCode2TestApp(t *testing.T) *app {
	t.Helper()
	a := launchTestApp(t)
	a.config.Port = 8877
	a.openCodeVersionProbe = func(string) (string, error) { return "2.0.27", nil }
	if _, err := a.modelLibrary.save(testModelLibrary(), 0, false); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestOpenCodeVersionPreferencePersistsWithoutChangingProfiles(t *testing.T) {
	a := openCode2TestApp(t)
	marker := filepath.Join(a.dir, "retained-chat")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"version":1,"major":0}`, `{"version":2,"major":2}`, `{"version":1,"major":3}`, `{"version":1,"major":2,"path":"/tmp"}`} {
		if w := adminRequest(a, "clients/opencode/preferences", body); w.Code < 400 {
			t.Fatal(body, w.Body.String())
		}
	}
	if w := adminRequest(a, "clients/opencode/preferences", `{"version":1,"major":2}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	stored, err := readSettings(a.dir)
	if err != nil || stored.OpenCodeMajor != 2 {
		t.Fatal(stored.OpenCodeMajor, err)
	}
	if data, _ := os.ReadFile(marker); string(data) != "keep" {
		t.Fatal("changed existing chat")
	}
	a.launchMu.Lock()
	if w := adminRequest(a, "clients/opencode/preferences", `{"version":1,"major":1}`); w.Code != 409 {
		t.Fatal(w.Code)
	}
	a.launchMu.Unlock()
}

func TestOpenCodeExecutablePreferencesSelectExactMajorAndPreserveChoices(t *testing.T) {
	a := openCode2TestApp(t)
	paths := []string{filepath.Join(t.TempDir(), "OpenCode 1 custom"), filepath.Join(t.TempDir(), "OpenCode 2 custom")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("synthetic CLI"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	post := func(value any, code int) {
		t.Helper()
		data, _ := json.Marshal(value)
		w := adminRequest(a, "clients/opencode/preferences", string(data))
		if w.Code != code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	post(openCodePreferences{Version: 1, Major: 2, Executable1: paths[0], Executable2: paths[1]}, 200)
	for i, client := range []string{"opencode", "opencode2"} {
		if got, err := a.launchRuntime().resolve(client, ""); err != nil || got != paths[i] {
			t.Fatal("explicit executable replaced by automatic discovery", client, got, err)
		}
	}
	var probed []string
	a.openCodeVersionProbe = func(path string) (string, error) {
		probed = append(probed, path)
		if path == paths[0] {
			return "1.19.0", nil
		}
		return "2.0.27", nil
	}
	for i, client := range []string{"opencode", "opencode2"} {
		if client == "opencode2" {
			if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		} else {
			launchPrepareFixture(t, a, client)
		}
		plan, err := a.planClientLaunch(clientLaunchRequest{Client: client}, a.launchRuntime())
		if err != nil || plan.Executable != paths[i] {
			t.Fatal("launch did not use the selected major's executable", client, plan.Executable, err)
		}
	}
	if len(probed) == 0 {
		t.Fatal("chosen executable version was not checked")
	}
	for _, path := range probed {
		if path != paths[0] && path != paths[1] {
			t.Fatal("version check used an automatic executable", path)
		}
	}
	a.openCodeVersionProbe = func(string) (string, error) { return "1.19.0", nil }
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime()); err == nil {
		t.Fatal("explicit path with wrong major accepted")
	}
	// Older version-only clients must not erase either executable choice.
	post(map[string]any{"version": 1, "major": 1}, 200)
	stored, err := readSettings(a.dir)
	if err != nil || stored.OpenCodeMajor != 1 || stored.OpenCode1Executable != paths[0] || stored.OpenCode2Executable != paths[1] {
		t.Fatal("version-only request lost paths", err)
	}
	reloaded, err := newApp(a.dir, a.vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.stop)
	for i, client := range []string{"opencode", "opencode2"} {
		if got, err := reloaded.launchRuntime().resolve(client, ""); err != nil || got != paths[i] {
			t.Fatal("restart lost executable choice", client, got, err)
		}
	}
	post(map[string]any{"version": 1, "major": 2, "executable1": ""}, 200)
	if got, err := a.launchRuntime().resolve("opencode", ""); err != nil || got == paths[0] {
		t.Fatal("automatic detection was not restored", got, err)
	}
	if got, err := a.launchRuntime().resolve("opencode2", ""); err != nil || got != paths[1] {
		t.Fatal("resetting v1 changed v2", got, err)
	}
	if err := os.Remove(paths[1]); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("opencode2", ""); err == nil || got != "" {
		t.Fatal("missing chosen executable silently fell back", got, err)
	}
}

func TestOpenCodeExecutablePreferencesRejectInvalidAndFailedSave(t *testing.T) {
	a := openCode2TestApp(t)
	for _, path := range []string{"relative/opencode", "~/opencode", "/tmp/bad\npath", "/tmp/bad\x00path", "/" + strings.Repeat("x", 8192)} {
		data, _ := json.Marshal(openCodePreferences{Version: 1, Major: 1, Executable2: path})
		if w := adminRequest(a, "clients/opencode/preferences", string(data)); w.Code != 400 {
			t.Fatal("invalid path accepted", w.Code)
		}
	}
	previous := a.config
	path := filepath.Join(a.dir, "settings.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(openCodePreferences{Version: 1, Major: 2, Executable2: filepath.Join(t.TempDir(), "opencode2")})
	if w := adminRequest(a, "clients/opencode/preferences", string(data)); w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	if a.config.OpenCodeMajor != previous.OpenCodeMajor || a.openCodeExecutable("opencode2") != "" {
		t.Fatal("failed save changed the effective executable")
	}
}

func TestOpenCodeExplicitManagedPathCannotBypassIntegrity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installations remain manual")
	}
	a := openCode2TestApp(t)
	record := managedToolRegistryFixture(t, a.dir, "opencode", "release-1")
	if err := writeManagedTool(a.dir, record); err != nil {
		t.Fatal(err)
	}
	setPath := func(path string) {
		t.Helper()
		data, _ := json.Marshal(openCodePreferences{Version: 1, Major: 1, Executable1: path})
		if w := adminRequest(a, "clients/opencode/preferences", string(data)); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	setPath(record.Executable)
	if got, err := a.launchRuntime().resolve("opencode", ""); err != nil || got != record.Executable {
		t.Fatal("valid managed entrypoint rejected", got, err)
	}
	if err := os.WriteFile(record.Executable, []byte("tampered managed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("opencode", ""); err == nil || got != "" {
		t.Fatal("explicit path bypassed the current record digest", got, err)
	}
	alias := filepath.Join(t.TempDir(), "opencode-alias")
	if err := os.Symlink(record.Executable, alias); err != nil {
		t.Fatal(err)
	}
	setPath(alias)
	if got, err := a.launchRuntime().resolve("opencode", ""); err == nil || got != "" {
		t.Fatal("external alias bypassed the managed record digest", got, err)
	}
}

func TestOpenCode2UsesSeparateVerifiedProfileAndExactGatewayModels(t *testing.T) {
	a := openCode2TestApp(t)
	if err := safeEditorDir(a.dir, filepath.Join(a.dir, "tools", "opencode")); err != nil {
		t.Fatal(err)
	}
	v1 := filepath.Join(a.dir, "tools", "opencode", "marker")
	if err := os.WriteFile(v1, []byte("OpenCode 1 retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	home, config, _ := a.openCode2Paths()
	data, _ := os.ReadFile(config)
	var parsed struct {
		Providers map[string]struct {
			Models map[string]struct {
				ModelID string `json:"modelID"`
			} `json:"models"`
		} `json:"providers"`
	}
	if bytes.Contains(data, []byte(a.config.LocalKey)) || bytes.Contains(data, []byte(`"provider":`)) || json.Unmarshal(data, &parsed) != nil || parsed.Providers["kilo-local"].Models["vendor/one"].ModelID != "vendor/one" {
		t.Fatal("invalid v2 configuration")
	}
	plan, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["HOME"] != home || plan.Env["KILO_OPENCODE2_API_KEY"] != a.config.LocalKey || len(plan.Args) != 1 || plan.Args[0] != "--standalone" {
		t.Fatal(plan.Args, "invalid private environment")
	}
	if data, _ := os.ReadFile(v1); string(data) != "OpenCode 1 retained" {
		t.Fatal("changed OpenCode 1")
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode"}, a.launchRuntime()); err == nil || !strings.Contains(err.Error(), "OpenCode 1.x") {
		t.Fatal("v2 accepted by v1 adapter", err)
	}
	a.openCodeVersionProbe = func(string) (string, error) { return "1.19.0", nil }
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime()); err == nil {
		t.Fatal("v1 accepted by v2 adapter")
	}
	a.openCodeVersionProbe = func(string) (string, error) { return "2.0.27", nil }
	changed := bytes.Replace(data, []byte("127.0.0.1"), []byte("evil.test"), 1)
	if err := os.WriteFile(config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime()); err == nil {
		t.Fatal("changed endpoint accepted")
	}
}

func TestInstalledOpenCode2UsesV2ConfigAndExactLocalGateway(t *testing.T) {
	binary := os.Getenv("KILO_TEST_OPENCODE2")
	if binary == "" {
		t.Skip("set KILO_TEST_OPENCODE2 to an official OpenCode 2 executable")
	}
	a := openCode2TestApp(t)
	a.openCodeVersionProbe = nil
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+a.config.LocalKey {
			http.Error(w, "unexpected synthetic request", 400)
			return
		}
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		model, _ := request["model"].(string)
		mu.Lock()
		models = append(models, model)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"synthetic-opencode2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"OPEN_CODE_2_SYNTHETIC_OK"},"finish_reason":null}]}`,
			`{"id":"synthetic-opencode2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
			`[DONE]`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	a.config.Port, _ = strconv.Atoi(u.Port())
	previousResolve := a.launcher.resolve
	a.launcher.resolve = func(id, custom string) (string, error) {
		if id == "opencode2" {
			return binary, nil
		}
		return previousResolve(id, custom)
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	plan, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2", Directory: t.TempDir()}, a.launchRuntime())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "--standalone", "--format", "json", "--model", "kilo-local/vendor/one", "Return OPEN_CODE_2_SYNTHETIC_OK")
	command.Dir = plan.Directory
	command.Env = clientChildEnvironment(os.Environ(), plan.Env, plan.Unset, a.launchRuntime().platform)
	output, err := command.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("OPEN_CODE_2_SYNTHETIC_OK")) {
		t.Fatalf("official v2 failed: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) == 0 {
		t.Fatal("no request reached the synthetic gateway")
	}
	for _, model := range models {
		if model != "vendor/one" {
			t.Fatal("wrong model", model)
		}
	}
}

func TestOpenCode2ProfileUsesOpenCodePackSlotAndSharedFallback(t *testing.T) {
	a := openCode2TestApp(t)
	catalog, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.config.OrgID, Models: []modelInfo{
		{ID: "vendor/pack", Name: "Pack pick", ContextWindow: 128000, MaxOutputTokens: 4096},
		{ID: "vendor/one", Name: "Shared pick", ContextWindow: 200000, MaxOutputTokens: 32000},
		{ID: "vendor/two", Name: "Shared fast", ContextWindow: 128000, MaxOutputTokens: 4096},
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
	_, config, _ := a.openCode2Paths()
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	prepared, _ := os.ReadFile(config)
	if !bytes.Contains(prepared, []byte(`"modelID":"vendor/pack"`)) || !bytes.Contains(prepared, []byte(`"model":"kilo-local/vendor/pack"`)) || bytes.Contains(prepared, []byte(`"vendor/one"`)) {
		t.Fatal("OpenCode 2 version-only preparation ignored the OpenCode pack slot", string(prepared))
	}
	delete(file.Assignments, "opencode")
	if err := newModelPacksStore(a.dir).save(file, false); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	prepared, _ = os.ReadFile(config)
	if !bytes.Contains(prepared, []byte(`"vendor/one"`)) || !bytes.Contains(prepared, []byte(`"vendor/two"`)) || bytes.Contains(prepared, []byte(`"vendor/pack"`)) {
		t.Fatal("OpenCode 2 did not fall back to the shared library", string(prepared))
	}
	// An assigned pack that no longer resolves is reported instead of silently
	// preparing other models.
	file.Personal[0].Library.Models[0].ID = "vendor/missing"
	file.Personal[0].Library.DefaultModel = "vendor/missing"
	file.Assignments["opencode"] = "mine-opencode"
	if err := newModelPacksStore(a.dir).save(file, false); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "vendor/missing") {
		t.Fatal("unresolvable OpenCode pack was not reported", w.Code, w.Body.String())
	}
}

func TestOpenCode2PreservesUserConfigurationAndRejectsOwnedChanges(t *testing.T) {
	a := openCode2TestApp(t)
	_, config, _ := a.openCode2Paths()
	if err := safeEditorDir(a.dir, filepath.Dir(config)); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{
		// user MCP and provider settings
		"mcp": {"example": {"type":"local", "command":["user-command"]}},
		"large":9007199254740993,
		"providers":{"personal":{"name":"Personal"}},
	}`)
	if err := os.WriteFile(config, original, 0600); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	prepared, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, retained := range []string{"// user MCP and provider settings", `"large":9007199254740993`, `"personal":{"name":"Personal"}`, `"command":["user-command"]`} {
		if !bytes.Contains(prepared, []byte(retained)) {
			t.Fatal("lost unowned setting", retained)
		}
	}
	if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	again, _ := os.ReadFile(config)
	if !bytes.Equal(prepared, again) {
		t.Fatal("repeat preparation changed configuration")
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime()); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(prepared, []byte(`"modelID":"vendor/one"`), []byte(`"modelID":"wrong/model"`), 1)
	if bytes.Equal(changed, prepared) {
		t.Fatal("fixture did not change owned model ID")
	}
	if err := os.WriteFile(config, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.planClientLaunch(clientLaunchRequest{Client: "opencode2"}, a.launchRuntime()); err == nil {
		t.Fatal("changed owned model mapping accepted")
	}
	for _, invalid := range []string{`{"providers":[]}`, `{"providers":{},"providers":{}}`} {
		if err := os.WriteFile(config, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if w := adminRequest(a, "clients/opencode2", `{}`); w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
		if actual, _ := os.ReadFile(config); string(actual) != invalid {
			t.Fatal("invalid configuration overwritten")
		}
	}
}

func TestOpenCode2DoesNotInventToolCapabilities(t *testing.T) {
	a := openCode2TestApp(t)
	no := false
	cache, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.catalogScopeLocked(), Models: []modelInfo{{ID: "vendor/one", Tools: &no, InputModalities: []string{"text"}, OutputModalities: []string{"text"}}}})
	if err := atomicCatalogFile(filepath.Join(a.dir, "model-catalog.json"), cache); err != nil {
		t.Fatal(err)
	}
	data, err := a.openCode2Config(testModelLibrary())
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("invalid configuration")
	}
	provider := config["providers"].(map[string]any)["kilo-local"].(map[string]any)
	models := provider["models"].(map[string]any)
	if models["vendor/one"].(map[string]any)["capabilities"].(map[string]any)["tools"] != false {
		t.Fatal("advertised tools against explicit catalog metadata")
	}
	cache, _ = json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.catalogScopeLocked(), Models: []modelInfo{{ID: "vendor/one", Tools: &no}}})
	if err := atomicCatalogFile(filepath.Join(a.dir, "model-catalog.json"), cache); err != nil {
		t.Fatal(err)
	}
	data, err = a.openCode2Config(testModelLibrary())
	if err != nil || bytes.Contains(data, []byte(`"capabilities"`)) {
		t.Fatal("partial catalog metadata produced invalid OpenCode 2 capabilities", err)
	}
	if err := os.Remove(filepath.Join(a.dir, "model-catalog.json")); err != nil {
		t.Fatal(err)
	}
	data, err = a.openCode2Config(testModelLibrary())
	if err != nil || bytes.Contains(data, []byte(`"tools"`)) {
		t.Fatal("invented tool support for an unknown model", err)
	}
}

func TestOpenCode2DiscoveryDoesNotAdvertiseVersionOneFallback(t *testing.T) {
	a := openCode2TestApp(t)
	a.openCodeVersionProbe = func(string) (string, error) { return "1.19.0", nil }
	read := func() clientLaunchAvailability {
		w := adminRequest(a, "clients/launch", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Clients map[string]clientLaunchAvailability `json:"clients"`
		}
		if json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatal("invalid discovery")
		}
		return response.Clients["opencode2"]
	}
	if info := read(); info.Installed || info.Available || !strings.Contains(info.Reason, "2.0.18") {
		t.Fatal("v1 command advertised as OpenCode 2", info)
	}
	a.openCodeVersionProbe = func(string) (string, error) { return "2.0.27", nil }
	if info := read(); !info.Installed || !info.Available {
		t.Fatal("verified OpenCode 2 unavailable", info)
	}
}
