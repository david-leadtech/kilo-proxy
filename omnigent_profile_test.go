package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func omnigentTestApp(t *testing.T) *app {
	t.Helper()
	omni := syntheticOmnigentInstallation(t)
	a := testApp(t)
	home := t.TempDir()
	paths := map[string]string{"omnigent": omni}
	for _, id := range []string{"codex-cli", "claude"} {
		path := filepath.Join(home, id)
		data := []byte("synthetic native executable; never executed")
		if id == "codex-cli" {
			data = openDesignCodexNativeFixture("macos", runtime.GOARCH)
		}
		if os.WriteFile(path, data, 0700) != nil {
			t.Fatal("cannot write fixture CLI")
		}
		paths[id] = path
	}
	a.launcher = &clientLaunchRuntime{platform: "macos", home: home, resolve: func(id, custom string) (string, error) {
		if p := paths[id]; p != "" && custom == "" {
			return p, nil
		}
		return "", errors.New("missing synthetic executable")
	}, start: func(clientLaunchPlan) error { return errors.New("fixture never starts a vendor CLI") }}
	a.apiKey = "synthetic-omnigent-gateway"
	a.config.OrgID = "synthetic-org"
	a.config.Port = 9988
	library := museTestLibrary()
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	if w := adminRequest(a, "clients/omnigent", `{}`); w.Code != 200 {
		t.Fatalf("fixture preparation: %d %s", w.Code, w.Body.String())
	}
	return a
}

func TestOmnigentProfilePreparationAndTamperRevocation(t *testing.T) {
	a := omnigentTestApp(t)
	assertReady := func(want bool) {
		t.Helper()
		w := adminRequest(a, "clients/omnigent", "")
		var state struct{ Prepared bool }
		if json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Prepared != want {
			t.Fatalf("prepared=%v wanted %v: %s", state.Prepared, want, w.Body.String())
		}
	}
	assertReady(true)
	paths := omnigentPaths(a.dir)
	original, err := os.ReadFile(paths.Selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*omnigentPrepared){
		func(saved *omnigentPrepared) { saved.Runtime.PythonPath = "/unrelated/python" },
		func(saved *omnigentPrepared) { saved.Runtime.Args = []string{"-c", "print('unrelated')"} },
		func(saved *omnigentPrepared) { saved.Runtime.Env["NODE_OPTIONS"] = "--require /unrelated/hook" },
		func(saved *omnigentPrepared) { saved.Runtime.Files = map[string]string{} },
		func(saved *omnigentPrepared) { delete(saved.Files, "inference.json") },
	} {
		var saved omnigentPrepared
		if json.Unmarshal(original, &saved) != nil {
			t.Fatal("invalid original fixture")
		}
		mutate(&saved)
		raw, _ := json.Marshal(saved)
		if atomicCatalogFile(paths.Selection, raw) != nil {
			t.Fatal("cannot mutate fixture manifest")
		}
		assertReady(false)
		if w := adminRequest(a, "clients/launch", `{"client":"omnigent"}`); w.Code != 409 {
			t.Fatalf("tampered execution became launchable: %d", w.Code)
		}
		if atomicCatalogFile(paths.Selection, original) != nil {
			t.Fatal("cannot restore fixture manifest")
		}
	}
	key := a.config.LocalKey
	a.config.LocalKey = key + "-rotated"
	assertReady(false)
	a.config.LocalKey = key
	assertReady(true)
	if err := os.Chmod(paths.Selection, 0644); err != nil {
		t.Fatal(err)
	}
	assertReady(false)
	if err := os.Chmod(paths.Selection, 0600); err != nil {
		t.Fatal(err)
	}
	assertReady(true)
	state := a.modelLibrary.snapshot()
	state.Library.DefaultModel = state.Library.Models[1].ID
	if _, err := a.modelLibrary.save(state.Library, state.Revision, false); err != nil {
		t.Fatal(err)
	}
	assertReady(false)
	if w := adminRequest(a, "clients/omnigent", `{}`); w.Code != 200 {
		t.Fatalf("updated library did not prepare: %s", w.Body.String())
	}
	assertReady(true)
}

func TestOmnigentProxyCredentialBoundariesAndSessionEffort(t *testing.T) {
	a := omnigentTestApp(t)
	handler := a.inferenceHandler("synthetic-upstream", a.config.OrgID, a.config.LocalKey, "127.0.0.1:9988")
	call := func(method, path, scope, body string, xkey bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:9988"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if xkey {
			r.Header.Set("X-Api-Key", omnigentProxyToken(a.config.LocalKey, scope))
		} else {
			r.Header.Set("Authorization", "Bearer "+omnigentProxyToken(a.config.LocalKey, scope))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, scope := range []string{"codex", "claude"} {
		if w := call("GET", "/omnigent/"+scope+"/v1/models", scope, "", scope == "claude"); w.Code != 200 || !strings.Contains(w.Body.String(), "vendor/nested/reasoning") || strings.Contains(w.Body.String(), a.config.LocalKey) {
			t.Fatalf("exact scoped catalog: %d %s", w.Code, w.Body.String())
		}
		for _, path := range []string{"/v1/models", "/omnigent/other/v1/models", "/muse/v1/responses"} {
			if w := call("GET", path, scope, "", false); w.Code != 404 {
				t.Fatalf("out-of-scope accepted: %s %d", path, w.Code)
			}
		}
	}
	for _, scope := range []string{"codex", "claude"} {
		w := call("POST", "/omnigent/claude/v1/session-effort", scope, `{"sessionId":"conv_fixture","model":"vendor/nested/reasoning","effort":"high"}`, false)
		if scope == "codex" && w.Code != 404 || scope == "claude" && w.Code != 200 {
			t.Fatalf("effort scope boundary: %s %d %s", scope, w.Code, w.Body.String())
		}
	}
	w := call("POST", "/omnigent/claude/v1/session-effort", "claude", `{"sessionId":"conv_fixture","model":"outside/model","effort":"high"}`, true)
	if w.Code != 400 {
		t.Fatal("unselected effort model accepted", w.Code)
	}
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"vendor/nested/reasoning","output_config":{"effort":"low"}}`))
	r.Header.Set("X-Kilo-Omnigent-Session", "conv_fixture")
	confirmed, err := a.omnigentSessionEffortRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Context().Value(omnigentEffortContextKey{}) != "high" {
		t.Fatal("confirmed effort did not follow exact session/model")
	}
	a.config.LocalKey += "-rotated"
	handler = a.inferenceHandler("synthetic-upstream", a.config.OrgID, a.config.LocalKey, "127.0.0.1:9988")
	if w := call("GET", "/omnigent/claude/v1/models", "claude", "", true); w.Code != 409 {
		t.Fatal("rotated key left stale profile usable", w.Code)
	}
}

func TestOmnigentNativeCredentialsAreTransientAndPerAgent(t *testing.T) {
	parent := []string{"OPENAI_API_KEY=synthetic-openai", "ANTHROPIC_API_KEY=synthetic-anthropic", "ANTHROPIC_CUSTOM_HEADERS=X-Own: synthetic", "KILO_OMNIGENT_NATIVE_CLAUDE_ENV=forged", "NODE_OPTIONS=forged"}
	codex, claude := omnigentNativeCredentialEnvironment("codex", parent), omnigentNativeCredentialEnvironment("claude", parent)
	if len(codex) != 1 || codex["OPENAI_API_KEY"] != "synthetic-openai" || len(claude) != 2 || claude["ANTHROPIC_API_KEY"] != "synthetic-anthropic" {
		t.Fatal("native credential crossed provider boundary")
	}
	unset := omnigentInheritedEnvironment(parent)
	for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_CUSTOM_HEADERS", "KILO_OMNIGENT_NATIVE_CLAUDE_ENV", "NODE_OPTIONS"} {
		if !helperContains(unset, name) {
			t.Fatalf("ambient %s entered private runtime", name)
		}
	}
}

func TestOmnigentNativeSecureStorageSelectionIsBoundAndSeparate(t *testing.T) {
	a := omnigentTestApp(t)
	a.config.Omnigent.ClaudeConnection = "native"
	library := a.modelLibrary.snapshot().Library
	nativeStore := filepath.Join(t.TempDir(), "native-secure-storage")
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", nativeStore)
	o, err := a.omnigentOptions(library, *a.launcher)
	if err != nil || o.NormalEnvironment["CLAUDE_SECURESTORAGE_CONFIG_DIR"] != nativeStore {
		t.Fatalf("native secure-storage selector was not bound: %v", err)
	}
	if err := a.prepareOmnigent(library, *a.launcher); err != nil {
		t.Fatal(err)
	}
	saved, err := a.readOmnigentPrepared()
	if err != nil || !a.omnigentReady(saved, *a.launcher) {
		t.Fatal("selected native secure-storage home was not ready", err)
	}
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", filepath.Join(t.TempDir(), "changed-store"))
	if a.omnigentReady(saved, *a.launcher) {
		t.Fatal("changed native secure-storage home did not revoke preparation")
	}
	t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", filepath.Join(omnigentPaths(a.dir).Root, "profiles", "claude-kilo"))
	if err := a.prepareOmnigent(library, *a.launcher); err == nil {
		t.Fatal("native secure-storage home overlapped the private Kilo profile")
	}
}
