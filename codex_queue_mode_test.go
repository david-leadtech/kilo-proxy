package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestMergeCodexQueueMode(t *testing.T) {
	original := []byte("# keep this comment\nmodel = 'vendor/model'\n\n[desktop]\nfollowUpQueueMode = 'steer' # chosen\nother = true\n")
	got, err := mergeCodexQueueMode(original, codexQueueModeQueue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("followUpQueueMode = 'queue' # chosen")) || !bytes.Contains(got, []byte("other = true")) {
		t.Fatal(string(got))
	}
	var parsed map[string]any
	if err := toml.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	value, _ := tomlAt(parsed, []string{"desktop", "followUpQueueMode"})
	if value != codexQueueModeQueue {
		t.Fatal(value)
	}
	again, err := mergeCodexQueueMode(got, codexQueueModeQueue)
	if err != nil || !bytes.Equal(got, again) {
		t.Fatal("not idempotent", err)
	}
}

func TestCodexQueueModeDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	if got := codexQueueModeFromConfig(dir); got != codexQueueModeQueue {
		t.Fatal(got)
	}
	if _, err := mergeCodexQueueMode(nil, "immediate"); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[desktop]\nfollowUpQueueMode='steer'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := codexQueueModeFromConfig(dir); got != codexQueueModeSteer {
		t.Fatal(got)
	}
}
