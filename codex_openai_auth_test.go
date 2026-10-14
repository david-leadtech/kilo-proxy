package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func assertOpenAIAuthPreference(t *testing.T, data []byte, want bool) {
	t.Helper()
	var config map[string]any
	if err := toml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	value, ok := tomlAt(config, []string{"model_providers", "kilo-local", "requires_openai_auth"})
	if !ok || value != want {
		t.Fatalf("requires_openai_auth = %v; want %v", value, want)
	}
	key, _ := tomlAt(config, []string{"model_providers", "kilo-local", "env_key"})
	if key != "KILO_LOCAL_API_KEY" {
		t.Fatalf("provider key changed: %v", key)
	}
	if config["model_provider"] != "kilo-local" {
		t.Fatal("inference provider changed")
	}
}

func TestMergeCodexConfigPreservesOpenAIAuthPreference(t *testing.T) {
	for _, tt := range []struct {
		name, config string
		want         bool
	}{
		{"new profile remains optional", "", false},
		{"explicit opt out", "[model_providers.kilo-local]\nrequires_openai_auth = false\n", false},
		{"desktop tools enabled", "[model_providers.kilo-local]\nrequires_openai_auth = true # desktop tools\n", true},
		{"quoted key", "[model_providers.'kilo-local']\nrequires_openai_auth = true\n", true},
		{"dotted key", "model_providers.'kilo-local'.requires_openai_auth = true\n", true},
		{"inline provider", "model_providers = { 'kilo-local' = { requires_openai_auth = true } }\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(tt.config)
			for _, port := range []int{8877, 9988, 8877} {
				merged, err := mergeCodexConfig(data, []byte(testCatalog), port)
				if err != nil {
					t.Fatal(err)
				}
				assertOpenAIAuthPreference(t, merged, tt.want)
				if strings.Contains(tt.config, "# desktop tools") && !bytes.Contains(merged, []byte("true # desktop tools")) {
					t.Fatal("authentication comment lost")
				}
				again, err := mergeCodexConfig(merged, []byte(testCatalog), port)
				if err != nil || !bytes.Equal(merged, again) {
					t.Fatal("profile preparation is not idempotent", err)
				}
				data = merged
			}
		})
	}
}

func TestSaveCodexProfileKeepsOpenAIAuthSession(t *testing.T) {
	dir := t.TempDir()
	auth := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"test-access","refresh_token":"test-refresh"}}`)
	authPath := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(authPath, auth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[model_providers.kilo-local]\nrequires_openai_auth = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{8877, 9988, 8877} {
		if _, _, err := saveCodexProfile(dir, []byte(testCatalog), port); err != nil {
			t.Fatal(err)
		}
		config, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		assertOpenAIAuthPreference(t, config, true)
		got, err := os.ReadFile(authPath)
		if err != nil || !bytes.Equal(got, auth) {
			t.Fatal("preparing the profile changed the saved ChatGPT session", err)
		}
	}
}

func TestCodexProfileRejectsInvalidOpenAIAuthPreference(t *testing.T) {
	dir := t.TempDir()
	config := []byte("[model_providers.kilo-local]\nrequires_openai_auth = 'private-invalid-setting'\n")
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saveCodexProfile(dir, []byte(testCatalog), 8877); err == nil || strings.Contains(err.Error(), "private-invalid-setting") {
		t.Fatal("invalid authentication preference must fail without exposing its value")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, config) {
		t.Fatal("invalid profile was overwritten", err)
	}
}

func TestCodexXcodeAuthUsesOnlyLocalProxyCredential(t *testing.T) {
	data := []byte("[model_providers.kilo-local]\nrequires_openai_auth = true\nenv_key = 'KILO_LOCAL_API_KEY'\n")
	merged, err := codexXcodeAuth(data, "test-local-key")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := toml.Unmarshal(merged, &config); err != nil {
		t.Fatal(err)
	}
	provider := config["model_providers"].(map[string]any)["kilo-local"].(map[string]any)
	if provider["requires_openai_auth"] != false || provider["experimental_bearer_token"] != "test-local-key" {
		t.Fatal("Xcode must remain local-key-only")
	}
	if _, ok := provider["env_key"]; ok {
		t.Fatal("Xcode cannot inherit the terminal key environment")
	}
}
