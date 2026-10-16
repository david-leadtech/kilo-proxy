package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func t3CodeLiteralSymlink(t *testing.T, target, alias string) {
	t.Helper()
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestT3CodeLiteralClaudeHomesRejectPrivateAliases(t *testing.T) {
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		for _, kind := range []string{"existing", "missing", "dangling-relative"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				options := t3ProfileTestOptions(t)
				private := filepath.Join(options.RootDir, "profiles", "claude-kilo")
				if err := os.MkdirAll(filepath.Join(private, "child"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(options.NormalHome, 0700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(options.NormalHome, "link")
				t3CodeLiteralSymlink(t, filepath.Join(private, "child"), alias)
				sep := string(filepath.Separator)
				literal := alias + sep + ".."
				expected := private
				if kind != "existing" {
					literal += sep + "missing"
					expected = filepath.Join(private, "missing")
				}
				if kind == "dangling-relative" {
					outer := filepath.Join(options.NormalHome, "dangling")
					t3CodeLiteralSymlink(t, "link"+sep+".."+sep+"missing", outer)
					literal = outer
				}
				options.NormalEnvironment[name] = literal
				canonical, err := t3CodeCanonicalPath(literal)
				if err != nil || canonical != expected {
					t.Fatal("literal alias target was lost before filesystem resolution", canonical, expected, err)
				}
				if _, err := planT3CodeProfiles(options); err == nil {
					t.Fatal("literal normal auth path overlaps private profile", name, kind)
				}
			})
		}
	}
}

func TestT3CodeLiteralClaudeHomesRetainNormalizedProtection(t *testing.T) {
	options := t3ProfileTestOptions(t)
	private := filepath.Join(options.RootDir, "profiles", "claude-kilo")
	outside := filepath.Join(options.NormalHome, "outside")
	for _, dir := range []string{private, filepath.Join(outside, "child")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(private, "link")
	t3CodeLiteralSymlink(t, filepath.Join(outside, "child"), alias)
	literal := alias + string(filepath.Separator) + ".."
	options.NormalEnvironment["CLAUDE_CONFIG_DIR"] = literal
	_, normalized, err := t3CodeNormalProviderHomes(options)
	if err != nil || normalized != private {
		t.Fatal("historical normalized-home fixture changed", normalized, private, err)
	}
	raw, err := t3CodeCanonicalPath(literal)
	if err != nil || raw != outside {
		t.Fatal("literal fixture did not leave private directory", raw, outside, err)
	}
	if _, err := planT3CodeProfiles(options); err == nil {
		t.Fatal("normalized private overlap protection was removed")
	}
}

func TestT3CodeLiteralClaudeAliasRetargetInvalidatesPreparation(t *testing.T) {
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		for _, kind := range []string{"existing", "missing"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				a := t3CodeTestApp(t, "linux")
				first := filepath.Join(a.launcher.home, "literal-first")
				second := filepath.Join(a.launcher.home, "literal-second")
				for _, dir := range []string{filepath.Join(first, "child"), filepath.Join(second, "child")} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				alias := filepath.Join(a.launcher.home, "literal-link")
				t3CodeLiteralSymlink(t, filepath.Join(first, "child"), alias)
				literal := alias + string(filepath.Separator) + ".."
				if kind == "missing" {
					literal += string(filepath.Separator) + "future"
				}
				t.Setenv(name, literal)
				prepareT3CodeFixture(t, a)
				saved := mustReadT3CodePrepared(t, a)
				binary := mustResolveT3CodeTest(t, a)
				if !a.t3CodeReady(saved, binary, a.launchRuntime()) {
					t.Fatal("valid literal auth path was not ready")
				}
				normal := saved.Providers[t3CodeClaudeNormalID].(map[string]any)
				if normal["config"].(map[string]any)["homePath"] != "" {
					t.Fatal("native descriptor would normalize literal override")
				}
				found := false
				for _, raw := range normal["environment"].([]any) {
					v := raw.(map[string]any)
					if v["name"] == name {
						if v["value"] != literal || v["sensitive"] != false {
							t.Fatal("literal native override changed")
						}
						found = true
					}
				}
				if !found {
					t.Fatal("literal native override missing")
				}
				proxy := saved.Providers[t3CodeClaudeProxyID].(map[string]any)
				if !strings.HasSuffix(proxy["config"].(map[string]any)["homePath"].(string), filepath.Join("profiles", "claude-kilo")) {
					t.Fatal("private proxy home changed")
				}
				for _, raw := range proxy["environment"].([]any) {
					v := raw.(map[string]any)
					if v["name"] == "CLAUDE_CONFIG_DIR" || v["name"] == "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
						t.Fatal("native override leaked to proxy")
					}
				}
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				t3CodeLiteralSymlink(t, filepath.Join(second, "child"), alias)
				if a.t3CodeReady(saved, binary, a.launchRuntime()) {
					t.Fatal("literal-only alias retarget retained stale preparation")
				}
				if _, err := a.planClientLaunch(clientLaunchRequest{Client: "t3-code"}, a.launchRuntime()); err == nil {
					t.Fatal("literal-only alias retarget launched")
				}
			})
		}
	}
}
