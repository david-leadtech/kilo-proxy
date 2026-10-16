package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestT3CodeInspectionCacheSingleFlightsAndClones(t *testing.T) {
	var cache t3CodeInspectionCache
	original := t3CodeInstallation{Version: "1.2.3", SourceDigest: "owned-digest", ClaudeBuiltins: []string{"claude-owned"},
		SupportedProviders: []t3CodeProviderSupport{{ID: "opencode", Label: "OpenCode"}}}
	var calls atomic.Int64
	analyze := func() (t3CodeInstallation, error) { calls.Add(1); return original, nil }
	start := make(chan struct{})
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			<-start
			value, err := cache.get("owned-digest", analyze)
			if err != nil || !reflect.DeepEqual(value, original) {
				t.Errorf("invalid shared result: %+v %v", value, err)
				return
			}
			value.ClaudeBuiltins[0] = "mutated-return"
			value.SupportedProviders[0].ID = "mutated-return"
		})
	}
	close(start)
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("identical content analyzed %d times", calls.Load())
	}
	original.ClaudeBuiltins[0] = "mutated-analyzer"
	original.SupportedProviders[0].Label = "mutated-analyzer"
	value, err := cache.get("owned-digest", func() (t3CodeInstallation, error) { t.Error("cache hit reanalyzed"); return t3CodeInstallation{}, nil })
	if err != nil || value.ClaudeBuiltins[0] != "claude-owned" || value.SupportedProviders[0] != (t3CodeProviderSupport{ID: "opencode", Label: "OpenCode"}) {
		t.Fatal("mutable results changed cached analysis", value, err)
	}
	var failures int
	fail := func() (t3CodeInstallation, error) {
		failures++
		return t3CodeInstallation{}, errors.New("incompatible")
	}
	for range 2 {
		if _, err := cache.get("bad-digest", fail); err == nil {
			t.Fatal("incompatible content accepted")
		}
	}
	if failures != 2 || cache.digest != "owned-digest" {
		t.Fatal("analysis error cached or replaced last success")
	}
	if _, err := cache.get("other-digest", func() (t3CodeInstallation, error) { return t3CodeInstallation{SourceDigest: "other-digest"}, nil }); err != nil {
		t.Fatal(err)
	}
	if cache.digest != "other-digest" {
		t.Fatal("cache did not retain only its latest content")
	}
	if _, err := cache.get("owned-digest", analyze); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("evicted analysis was retained", calls.Load())
	}
}

func t3CodeCacheFixtureFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	archive, err := t3CodeOpenASAR(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.file.Close()
	files := map[string][]byte{}
	for _, name := range []string{"package.json", "apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs",
		"apps/server/dist/client/index.html", "apps/server/dist/client/assets/settings.providers-Fixture.js", "apps/server/dist/client/assets/utils-Fixture.js"} {
		files[name], err = archive.read(name, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestT3CodeInspectionCacheReadsSameSizeTimestampSourceChanges(t *testing.T) {
	for _, entry := range []string{"apps/desktop/dist-electron/main.cjs", "apps/server/dist/bin.mjs"} {
		t.Run(entry, func(t *testing.T) {
			binary := syntheticT3CodeExecutable(t, t.TempDir(), "linux", "0.99.0-canary.12")
			path := filepath.Join(filepath.Dir(binary), "resources", "app.asar")
			files := t3CodeCacheFixtureFiles(t, path)
			files[entry] = append(files[entry], []byte(" // own cache content A")...)
			writeT3CodeFixtureASAR(t, path, files)
			var cache t3CodeInspectionCache
			first, err := inspectT3CodeInstallationWithCache(binary, "linux", &cache)
			if err != nil {
				t.Fatal(err)
			}
			again, err := inspectT3CodeInstallationWithCache(binary, "linux", &cache)
			if err != nil || !reflect.DeepEqual(first, again) || cache.digest != first.SourceDigest {
				t.Fatal("identical source changed analysis", err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			files[entry] = bytes.Replace(files[entry], []byte("content A"), []byte("content B"), 1)
			writeT3CodeFixtureASAR(t, path, files)
			if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			afterInfo, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(before, after) || info.Size() != afterInfo.Size() || !info.ModTime().Equal(afterInfo.ModTime()) {
				t.Fatal("fixture did not retain size and timestamp")
			}
			changed, err := inspectT3CodeInstallationWithCache(binary, "linux", &cache)
			if err != nil || changed.Version != first.Version || changed.SourceDigest == first.SourceDigest || cache.digest != changed.SourceDigest {
				t.Fatal("same-version, size and timestamp bypassed full content validation", changed, err)
			}
		})
	}
}

func TestT3CodeInspectionCacheCannotBypassPackageReadOrContractFailure(t *testing.T) {
	for _, kind := range []string{"removed", "truncated", "invalid header", "invalid metadata", "missing contract"} {
		t.Run(kind, func(t *testing.T) {
			binary := syntheticT3CodeExecutable(t, t.TempDir(), "linux")
			path := filepath.Join(filepath.Dir(binary), "resources", "app.asar")
			var cache t3CodeInspectionCache
			first, err := inspectT3CodeInstallationWithCache(binary, "linux", &cache)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "removed":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(path, 16); err != nil {
					t.Fatal(err)
				}
			case "invalid header":
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data[0] = 0
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid metadata":
				files := t3CodeCacheFixtureFiles(t, path)
				files["package.json"] = bytes.Replace(files["package.json"], []byte("t3code"), []byte("x3code"), 1)
				writeT3CodeFixtureASAR(t, path, files)
			case "missing contract":
				files := t3CodeCacheFixtureFiles(t, path)
				token := "providerInstances"
				files["apps/server/dist/bin.mjs"] = bytes.Replace(files["apps/server/dist/bin.mjs"], []byte(token), []byte(strings.Repeat("x", len(token))), 1)
				writeT3CodeFixtureASAR(t, path, files)
			}
			if _, err := inspectT3CodeInstallationWithCache(binary, "linux", &cache); err == nil {
				t.Fatal("cached success bypassed current invalid package")
			}
			if !cache.valid || cache.digest != first.SourceDigest {
				t.Fatal("invalid package replaced successful cached content")
			}
		})
	}
}
