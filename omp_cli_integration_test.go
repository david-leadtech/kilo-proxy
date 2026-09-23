package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in check runs the released CLI, not a launch stub. All model and
// image inference is answered by a synthetic local gateway; no Kilo credits,
// personal credentials, installed client profiles, or global installs are used.
func TestInstalledOMP(t *testing.T) {
	binary := os.Getenv("KILO_TEST_OMP_BINARY")
	if binary == "" {
		t.Skip("set KILO_TEST_OMP_BINARY to an Oh My Pi executable")
	}
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(project, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("SYNTHETIC_OMP_READ_RESULT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := testApp(t)
	a.ompProfileDir = filepath.Join(dir, "omp-profile")
	a.apiKey = "synthetic-omp-upstream"
	a.config.OrgID = "synthetic-omp-team"
	a.config.ImageGeneration = imageGenerationSettings{Enabled: true, Model: "vendor/image-model"}
	pngData := imageTestPNG(t)
	var reads, imageCalls, completed, switches, mcpLists atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-omp-upstream" || r.Header.Get("X-KiloCode-OrganizationId") != "synthetic-omp-team" {
			t.Error("OMP lost Kilo Proxy's upstream credential or team header")
		}
		switch r.URL.Path {
		case "/api/gateway/models":
			jsonResponse(w, 200, map[string]any{"data": []any{map[string]any{"id": "vendor/image-model", "name": "Synthetic image model", "architecture": map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"image", "text"}}}}})
		case "/synthetic-images":
			imageCalls.Add(1)
			jsonResponse(w, 200, imageTestResponse(pngData, map[string]any{"cost": 0.04}))
		case "/api/gateway/responses":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				http.Error(w, "read error", 400)
				return
			}
			doc, err := decodeObject(data)
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid JSON", 400)
				return
			}
			if doc["store"] == true || doc["previous_response_id"] != nil || doc["stream"] != true {
				t.Error("OMP used unsupported stored, chained, or non-streaming Responses")
			}
			model := stringValue(doc["model"])
			if model == "vendor/two" {
				switches.Add(1)
				ompFixtureText(w, "SYNTHETIC_OMP_SWITCH_OK")
				return
			}
			if model != "vendor/one" {
				t.Errorf("OMP did not use the configured default model: %q", model)
				http.Error(w, "unexpected model", 400)
				return
			}
			if stringValue(object(doc["reasoning"])["effort"]) != "high" {
				t.Errorf("OMP lost the configured reasoning level: %v", doc["reasoning"])
			}
			var readResult, imageResult bool
			for _, raw := range ompFixtureArray(doc["input"]) {
				item := object(raw)
				if stringValue(item["type"]) != "function_call_output" {
					continue
				}
				encoded, _ := json.Marshal(item["output"])
				readResult = readResult || strings.Contains(string(encoded), "SYNTHETIC_OMP_READ_RESULT")
				imageResult = imageResult || strings.Contains(string(encoded), "input_image")
			}
			if imageResult {
				completed.Add(1)
				ompFixtureText(w, "SYNTHETIC_OMP_TOOLS_AND_IMAGES_OK")
				return
			}
			if readResult {
				reads.Add(1)
				for _, raw := range ompFixtureArray(doc["tools"]) {
					name := stringValue(object(raw)["name"])
					if strings.Contains(name, "generate_image") {
						ompFixtureTool(w, "image", name, map[string]any{"prompt": "Draw a synthetic blue square"})
						return
					}
				}
				// OMP 18 defaults to deferred tools: its prompt advertises MCP
				// tools as xd:// devices, invoked through the standard write tool.
				device := "xd://mcp__kilo_images_generate_image"
				if strings.Contains(stringValue(doc["instructions"]), device) {
					ompFixtureTool(w, "image", "write", map[string]any{"path": device, "content": `{"prompt":"Draw a synthetic blue square"}`})
					return
				}
				t.Errorf("OMP did not expose the configured Kilo image MCP tool (tools/list calls=%d)", mcpLists.Load())
				http.Error(w, "missing image MCP", 400)
				return
			}
			ompFixtureTool(w, "read", "read", map[string]any{"path": fixturePath})
		default:
			t.Errorf("unexpected synthetic upstream route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	setUpstream(a, upstream.URL)
	a.imageGenerationURL = upstream.URL + "/synthetic-images"
	proxy := httptest.NewUnstartedServer(nil)
	_, port, err := net.SplitHostPort(proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	a.config.Port, _ = strconv.Atoi(port)
	handler := a.inferenceHandler(a.apiKey, a.config.OrgID, a.config.LocalKey, proxy.Listener.Addr().String())
	proxy.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp/images" && r.Method == "POST" {
			// Count discovery without inspecting or persisting client credentials.
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(data)))
			if strings.Contains(string(data), `"tools/list"`) {
				mcpLists.Add(1)
			}
		}
		handler.ServeHTTP(w, r)
	})
	proxy.Start()
	defer proxy.Close()
	selection := `{"models":[{"id":"vendor/one","name":"Friendly reasoning model","contextWindow":64000,"maxOutputTokens":4000,"reasoning":true,"effort":"high","reasoningEfforts":["low","medium","high"],"inputModalities":["text","image"],"inputPrice":1,"outputPrice":5},{"id":"vendor/two","name":"Friendly fast model","contextWindow":32000,"maxOutputTokens":2000,"reasoning":false,"inputModalities":["text"]}],"initial":"vendor/one"}`
	if result := adminRequest(a, "omp/profile", selection); result.Code != http.StatusOK {
		t.Fatalf("OMP profile preparation failed: %d %s", result.Code, result.Body)
	}
	if result := adminRequest(a, "omp/profile", ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Friendly reasoning model") {
		t.Fatalf("OMP profile reload failed: %d %s", result.Code, result.Body)
	}
	env := []string{}
	for _, entry := range os.Environ() {
		name := strings.SplitN(entry, "=", 2)[0]
		if strings.HasPrefix(name, "OMP_") || strings.HasPrefix(name, "PI_") || strings.HasPrefix(name, "XDG_") || strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "API_KEY") || name == "HOME" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "HOME="+dir, "OMP_PROFILE=", "PI_PROFILE=", "PI_CODING_AGENT_DIR="+a.ompProfileDir, "PI_OPENAI_STATEFUL=0", "NO_COLOR=1")
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = project, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("released OMP failed (%v): %s", err, out)
		}
		return string(out)
	}
	models := run("models", "kilo-local", "--json", "--no-extensions")
	for _, want := range []string{"vendor/one", "vendor/two", "Friendly reasoning model", "Friendly fast model"} {
		if !strings.Contains(models, want) {
			t.Fatalf("released OMP did not load %q: %s", want, models)
		}
	}
	var catalog struct {
		Models []struct {
			ID            string   `json:"id"`
			ContextWindow int      `json:"contextWindow"`
			MaxTokens     int      `json:"maxTokens"`
			Reasoning     bool     `json:"reasoning"`
			Thinking      []string `json:"thinking"`
			Input         []string `json:"input"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(models), &catalog); err != nil || len(catalog.Models) != 2 {
		t.Fatalf("OMP returned an invalid generated catalog: %v %s", err, models)
	}
	for _, model := range catalog.Models {
		if model.ID == "vendor/one" && (!model.Reasoning || strings.Join(model.Thinking, ",") != "low,medium,high" || strings.Join(model.Input, ",") != "text,image" || model.ContextWindow != 64000 || model.MaxTokens != 4000) {
			t.Fatalf("OMP did not honor reasoning, vision or token limits: %+v", model)
		}
		if model.ID == "vendor/two" && model.Reasoning {
			t.Fatal("OMP invented reasoning for a non-reasoning model")
		}
	}
	args := []string{"--print", "--mode", "json", "--no-session", "--no-lsp", "--no-pty", "--no-extensions", "--no-skills", "--no-rules", "--no-title"}
	result := run(append(append([]string{}, args...), "Read fixture.txt and generate the synthetic image.")...)
	if reads.Load() != 1 || imageCalls.Load() != 1 || completed.Load() != 1 || mcpLists.Load() < 1 || !strings.Contains(result, "SYNTHETIC_OMP_TOOLS_AND_IMAGES_OK") {
		t.Fatalf("default-model tool and image round-trip failed: reads=%d images=%d completed=%d MCP=%d output=%s", reads.Load(), imageCalls.Load(), completed.Load(), mcpLists.Load(), result)
	}
	result = run(append(append([]string{}, args...), "--model", "kilo-local/vendor/two", "Reply with the synthetic marker.")...)
	if switches.Load() != 1 || !strings.Contains(result, "SYNTHETIC_OMP_SWITCH_OK") {
		t.Fatalf("explicit model switch failed: requests=%d output=%s", switches.Load(), result)
	}
	t.Log("Released OMP verified generated profile, both model names, default/reasoning, explicit model switch, Responses streaming, local file tool execution, and image MCP generation with image replay. Only synthetic loopback inference was used.")
}

func ompFixtureEvent(w http.ResponseWriter, kind string, payload map[string]any) {
	payload["type"] = kind
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
}

func ompFixtureArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func ompFixtureComplete(w http.ResponseWriter, output any) {
	ompFixtureEvent(w, "response.completed", map[string]any{"response": map[string]any{"id": "resp_omp", "object": "response", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "cost": 0.001}}})
}

func ompFixtureTool(w http.ResponseWriter, id, name string, args map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	arguments, _ := json.Marshal(args)
	item := map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_" + id, "name": name, "arguments": "", "status": "in_progress"}
	ompFixtureEvent(w, "response.output_item.added", map[string]any{"output_index": 0, "item": item})
	ompFixtureEvent(w, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "item_id": item["id"], "delta": string(arguments)})
	item["arguments"], item["status"] = string(arguments), "completed"
	ompFixtureEvent(w, "response.function_call_arguments.done", map[string]any{"output_index": 0, "item_id": item["id"], "arguments": string(arguments)})
	ompFixtureEvent(w, "response.output_item.done", map[string]any{"output_index": 0, "item": item})
	ompFixtureComplete(w, []any{item})
}

func ompFixtureText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	item := map[string]any{"type": "message", "id": "msg_omp", "role": "assistant", "status": "in_progress", "content": []any{}}
	part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
	ompFixtureEvent(w, "response.output_item.added", map[string]any{"output_index": 0, "item": item})
	ompFixtureEvent(w, "response.content_part.added", map[string]any{"output_index": 0, "item_id": item["id"], "content_index": 0, "part": part})
	ompFixtureEvent(w, "response.output_text.delta", map[string]any{"output_index": 0, "item_id": item["id"], "content_index": 0, "delta": text})
	part["text"], item["status"] = text, "completed"
	item["content"] = []any{part}
	ompFixtureEvent(w, "response.output_item.done", map[string]any{"output_index": 0, "item": item})
	ompFixtureComplete(w, []any{item})
}
