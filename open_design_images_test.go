package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func openDesignImageSettings(t *testing.T, data []byte) map[string]any {
	t.Helper()
	tree, err := hujson.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	tree = tree.Clone()
	tree.Standardize()
	var config map[string]any
	if err := json.Unmarshal(tree.Pack(), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func TestOpenDesignOpenCodeImagesPreserveToolsAndRotateLocalConnection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	old := []byte(`{
 // Keep the user's MCP and tool choices exactly
 "custom":9007199254740993,
 "mcp":{"design_tools":{"type":"remote","url":"http://127.0.0.1:8765/mcp","headers":{"Authorization":"Bearer design-token"}}},
 "tools":{"design_tools_*":true,"other_*":false},
 "permission":{"bash":"ask"}
}`)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	images := imageGenerationSettings{Enabled: true, Model: "vendor/image-model"}
	for _, connection := range []struct {
		port int
		key  string
	}{{8877, "old-local"}, {8899, "new-local"}} {
		if err := prepareOpenDesignEngineProfile(dir, "opencode", testModelLibrary(), nil, claudeCapabilities{}, connection.port, connection.key, images); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before, after := openDesignImageSettings(t, old), openDesignImageSettings(t, data)
	for _, section := range []string{"tools", "permission"} {
		if !reflect.DeepEqual(before[section], after[section]) {
			t.Fatalf("changed unrelated %s", section)
		}
	}
	servers := after["mcp"].(map[string]any)
	if !reflect.DeepEqual(before["mcp"].(map[string]any)["design_tools"], servers["design_tools"]) {
		t.Fatal("changed Open Design's unrelated MCP server")
	}
	want := map[string]any{"type": "remote", "url": "http://127.0.0.1:8899/mcp/images", "headers": map[string]any{"Authorization": "Bearer new-local"}, "oauth": false, "enabled": true, "timeout": float64(360000)}
	if !reflect.DeepEqual(servers["kilo_images"], want) {
		t.Fatalf("invalid image MCP configuration: %#v", servers["kilo_images"])
	}
	for _, preserved := range []string{"Keep the user's MCP", "9007199254740993"} {
		if !bytes.Contains(data, []byte(preserved)) {
			t.Fatalf("lost JSONC content: %s", preserved)
		}
	}
	if bytes.Contains(data, []byte("old-local")) || bytes.Contains(data, []byte(":8877/")) {
		t.Fatal("retained stale proxy authentication or port")
	}
	if err := prepareOpenDesignEngineProfile(dir, "opencode", testModelLibrary(), nil, claudeCapabilities{}, 8899, "new-local", imageGenerationSettings{}); err != nil {
		t.Fatal(err)
	}
	disabled, _ := os.ReadFile(path)
	remaining := openDesignImageSettings(t, disabled)["mcp"].(map[string]any)
	if len(remaining) != 1 || !reflect.DeepEqual(remaining["design_tools"], servers["design_tools"]) {
		t.Fatal("disabling images must remove only the managed image server")
	}
}

func TestOpenDesignOpenCodeImagesRejectConflictsBeforeProfileWrites(t *testing.T) {
	for name, entry := range map[string]string{
		"command":      `{"type":"local","command":["custom-mcp"]}`,
		"remote":       `{"type":"remote","url":"https://example.com/mcp","oauth":false,"headers":{"Authorization":"Bearer custom"}}`,
		"override":     `{"type":"remote","url":"http://127.0.0.1:8877/mcp/images?override=1","oauth":false,"headers":{"Authorization":"Bearer custom"}}`,
		"extra header": `{"type":"remote","url":"http://127.0.0.1:8877/mcp/images","oauth":false,"headers":{"Authorization":"Bearer custom","X-Custom":"keep"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "opencode.json")
			old := []byte(`{"mcp":{"kilo_images":` + entry + `}}`)
			if err := os.WriteFile(path, old, 0600); err != nil {
				t.Fatal(err)
			}
			err := prepareOpenDesignEngineProfile(dir, "opencode", testModelLibrary(), nil, claudeCapabilities{}, 8877, "synthetic-local", imageGenerationSettings{Enabled: true, Model: "vendor/image"})
			if err == nil || !strings.Contains(err.Error(), "already used") {
				t.Fatalf("expected conflict, got %v", err)
			}
			current, _ := os.ReadFile(path)
			if !bytes.Equal(old, current) {
				t.Fatal("conflict modified settings")
			}
			if _, err := os.Stat(filepath.Join(dir, "kilo-models.json")); !os.IsNotExist(err) {
				t.Fatal("conflict saved model selection")
			}
			disabled, err := mergeOpenDesignOpenCodeImages(old, imageGenerationSettings{}, 8877, "synthetic-local")
			if err != nil || !bytes.Equal(old, disabled) {
				t.Fatal("disabled images removed an unrelated same-name server")
			}
		})
	}
}

func TestOpenDesignOpenCodeImageConfigDiscoversExistingMCPWithoutInference(t *testing.T) {
	a := imageTestApp(t, func(http.ResponseWriter, *http.Request) { t.Error("MCP discovery must not generate an image") })
	config, err := mergeOpenDesignOpenCodeImages([]byte(`{}`), imageGenerationSettings{Enabled: true, Model: "vendor/image-model"}, 8877, "image-local-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := openDesignImageSettings(t, config)["mcp"].(map[string]any)["kilo_images"].(map[string]any)
	r := httptest.NewRequest("POST", server["url"].(string), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	for name, value := range server["headers"].(map[string]any) {
		r.Header.Set(name, value.(string))
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	a.inferenceHandler("image-upstream-secret", "image-org", "image-local-secret", "127.0.0.1:8877").ServeHTTP(w, r)
	result := imageMCPTestObject(t, w)["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "generate_image" {
		t.Fatal("configured OpenCode MCP did not expose generate_image")
	}
}

func TestOpenDesignOpenCodeImagesValidateSettingsAndJSON(t *testing.T) {
	for _, input := range []struct {
		data   string
		images imageGenerationSettings
		port   int
		key    string
	}{
		{`{}`, imageGenerationSettings{Enabled: true}, 8877, "local"},
		{`{}`, imageGenerationSettings{Enabled: true, Model: "vendor/image"}, 1, "local"},
		{`{}`, imageGenerationSettings{Enabled: true, Model: "vendor/image"}, 8877, "bad\nkey"},
		{`{"mcp":{},"mcp":{}}`, imageGenerationSettings{Enabled: true, Model: "vendor/image"}, 8877, "local"},
		{`{"mcp":[]}`, imageGenerationSettings{Enabled: true, Model: "vendor/image"}, 8877, "local"},
	} {
		if _, err := mergeOpenDesignOpenCodeImages([]byte(input.data), input.images, input.port, input.key); err == nil {
			t.Fatalf("accepted invalid image MCP input: %s", input.data)
		}
	}
}
