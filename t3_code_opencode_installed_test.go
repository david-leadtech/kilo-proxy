package main

import (
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

// Explicit official executables, fresh profiles and a loopback-only synthetic
// gateway exercise the same config/environment that T3's native driver gets.
func TestInstalledT3CodeOpenCodeDiscoversAndRoutesSharedModels(t *testing.T) {
	for _, second := range []bool{false, true} {
		t.Run(fmt.Sprint(second), func(t *testing.T) {
			variable := "KILO_TEST_OPENCODE"
			if second {
				variable = "KILO_TEST_OPENCODE2"
			}
			binary := os.Getenv(variable)
			if binary == "" {
				t.Skip("set " + variable + " to an explicit official executable")
			}
			if !filepath.IsAbs(binary) {
				t.Fatal("official executable path must be absolute")
			}
			const key = "synthetic-t3-opencode-local-key"
			ids := []string{"vendor/one", "vendor/nested/two"}
			var mu sync.Mutex
			var requests []string
			instructionsSeen := false
			highSeen := map[string]bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model           string          `json:"model"`
					Messages        json.RawMessage `json:"messages"`
					ReasoningEffort string          `json:"reasoning_effort"`
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != ids[0] && body.Model != ids[1] {
					t.Error("OpenCode did not preserve the exact local gateway model/authentication")
					http.Error(w, "unexpected synthetic request", http.StatusBadRequest)
					return
				}
				mu.Lock()
				requests = append(requests, body.Model)
				instructionsSeen = instructionsSeen || strings.Contains(string(body.Messages), "T3_PROJECT_INSTRUCTIONS_SYNTHETIC")
				if body.ReasoningEffort == "high" {
					highSeen[body.Model] = true
				}
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				for _, chunk := range []string{
					`{"id":"synthetic-t3-opencode","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"T3_OPENCODE_SYNTHETIC_OK"},"finish_reason":null}]}`,
					`{"id":"synthetic-t3-opencode","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
					`[DONE]`,
				} {
					fmt.Fprintf(w, "data: %s\n\n", chunk)
				}
			}))
			defer server.Close()
			address, _ := url.Parse(server.URL)
			port, _ := strconv.Atoi(address.Port())
			prefs := normalizeT3CodePreferences(t3CodePreferences{})
			prefs.KiloCodex, prefs.KiloClaude = false, false
			prefs.Providers, prefs.OpenCodeConnection, prefs.OpenCode2 = []string{"opencode"}, "kilo", second
			library := emptyModelLibrary()
			library.DefaultModel = ids[0]
			for _, id := range ids {
				library.Models = append(library.Models, modelLibraryItem{ID: id, ContextPreset: contextPresetCustom, ContextWindow: 64000, MaxOutputTokens: 4000, ReasoningCustom: true, ReasoningEffort: "low", ReasoningLevels: []string{"low", "high"}})
			}
			tools := true
			catalog := []modelInfo{{ID: ids[0], Tools: &tools, InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}}, {ID: ids[1], Tools: &tools, InputModalities: []string{"text"}, OutputModalities: []string{"text"}}}
			options := t3CodeProfileOptions{RootDir: t.TempDir(), NormalHome: t.TempDir(), Platform: runtime.GOOS, OpenCodeKiloSupported: true, Preferences: &prefs, Library: library, Port: port, LocalKey: key,
				Catalog:             catalog,
				AdditionalProviders: map[string]any{"opencode": map[string]any{"config": map[string]any{"binaryPath": binary, "customModels": []any{}}}}}
			provider, path, data, err := planT3CodeOpenCode(options)
			if err != nil || provider == nil || provider["enabled"] != true {
				t.Fatal("production T3 profile planning failed", err)
			}
			if err := safeEditorDir(options.RootDir, filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir(), "LANG=C.UTF-8", "HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}
			for _, entry := range provider["environment"].([]map[string]any) {
				env = append(env, entry["name"].(string)+"="+entry["value"].(string))
			}
			project := t.TempDir()
			models, section, apiID := map[string]any{}, "provider", "id"
			if second {
				section, apiID = "providers", "modelID"
			}
			for _, id := range ids {
				model := map[string]any{apiID: "unrelated/wrong-model", "headers": map[string]string{"Authorization": "Bearer unrelated-key"}}
				if second {
					model["settings"] = map[string]any{"baseURL": server.URL + "/unexpected/v1", "apiKey": "unrelated-key", "headers": map[string]string{"Authorization": "Bearer unrelated-key"}}
					model["body"] = map[string]string{"model": "unrelated/wrong-body"}
					model["variants"] = []map[string]any{{"id": "high", "settings": map[string]string{"baseURL": server.URL + "/unexpected/v1", "apiKey": "unrelated-key"}, "headers": map[string]string{"Authorization": "Bearer unrelated-key"}, "body": map[string]string{"model": "unrelated/wrong-variant", "reasoning_effort": "high"}}, {"id": "evil-extra", "body": map[string]string{"model": "unrelated/wrong-extra"}}}
				} else {
					model["provider"] = map[string]string{"api": server.URL + "/unexpected/v1", "npm": "@ai-sdk/openai-compatible"}
					model["options"] = map[string]any{"baseURL": server.URL + "/unexpected/v1", "apiKey": "unrelated-key", "headers": map[string]string{"Authorization": "Bearer unrelated-key"}, "extraBody": map[string]string{"model": "unrelated/wrong-body"}}
					model["variants"] = map[string]any{"high": map[string]any{"model": "unrelated/wrong-variant", "reasoningEffort": "high", "baseURL": server.URL + "/unexpected/v1", "apiKey": "unrelated-key", "headers": map[string]string{"Authorization": "Bearer unrelated-key"}}}
				}
				models[id] = model
			}
			projectConfig, _ := json.Marshal(map[string]any{section: map[string]any{"kilo-local": map[string]any{"models": models}}})
			if err := os.WriteFile(filepath.Join(project, "opencode.json"), projectConfig, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("T3_PROJECT_INSTRUCTIONS_SYNTHETIC\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			run := func(args ...string) []byte {
				t.Helper()
				command := exec.CommandContext(ctx, binary, args...)
				command.Dir, command.Env = project, env
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("official OpenCode %v failed: %v\n%s", args, err, output)
				}
				return output
			}
			if second {
				var listed struct {
					Data []struct{ ProviderID, ModelID string } `json:"data"`
				}
				output := run("api", "--standalone", "model.list")
				if json.Unmarshal(output, &listed) != nil || len(listed.Data) != len(ids) {
					t.Fatalf("native T3 model.list did not return the exact shared catalog: %s", output)
				}
				found := map[string]bool{}
				for _, model := range listed.Data {
					if model.ProviderID != "kilo-local" {
						t.Fatal("unrelated provider entered the Kilo catalog", model.ProviderID)
					}
					found[model.ModelID] = true
				}
				for _, id := range ids {
					if !found[id] {
						t.Fatal("native T3 discovery changed the gateway ID", id)
					}
				}
			} else {
				output := string(run("models", "--verbose"))
				for _, id := range ids {
					if !strings.Contains(output, "kilo-local/"+id) {
						t.Fatal("native T3 discovery omitted the exact shared model", id)
					}
				}
			}
			for _, id := range ids {
				mu.Lock()
				before := len(requests)
				mu.Unlock()
				mode := "--pure"
				if second {
					mode = "--standalone"
				}
				selected := "kilo-local/" + id
				args := []string{"run", mode, "--format", "json", "--model", selected}
				if second {
					args[len(args)-1] += "#high"
				} else {
					args = append(args, "--variant", "high")
				}
				output := run(append(args, "Return T3_OPENCODE_SYNTHETIC_OK")...)
				if !strings.Contains(string(output), "T3_OPENCODE_SYNTHETIC_OK") {
					t.Fatalf("synthetic completion missing: %s", output)
				}
				mu.Lock()
				seen := false
				for _, actual := range requests[before:] {
					seen = seen || actual == id
				}
				high := highSeen[id]
				mu.Unlock()
				if !seen {
					t.Fatal("the selected model never reached the local gateway", id)
				}
				if !high {
					t.Fatal("the selected reasoning level never reached the local gateway", id)
				}
			}
			mu.Lock()
			seen := instructionsSeen
			mu.Unlock()
			if !seen {
				t.Fatal("protecting the Kilo route discarded the project's AGENTS.md instructions")
			}
			t.Log("Official OpenCode discovered both shared models and routed their exact IDs using the generated T3 environment.")
		})
	}
}
