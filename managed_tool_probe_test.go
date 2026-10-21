package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManagedToolCLIProbeUsesPrivateStateAndFixedArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixture")
	}
	root := t.TempDir()
	normal := filepath.Join(root, "personal")
	if err := os.Mkdir(normal, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(normal, "auth.json")
	if err := os.WriteFile(secret, []byte("must not be read or changed"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "ambient-secret")
	t.Setenv("NODE_OPTIONS", "--require=ambient-hook")
	t.Setenv("CODEX_HOME", normal)
	t.Setenv("CLAUDE_CONFIG_DIR", normal)
	executable := filepath.Join(root, "codex")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 2\n[ \"$HOME\" != " + helperShellQuote(normal) + " ] || exit 3\n[ \"$PWD\" = \"$HOME\" ] && [ \"$CODEX_HOME\" = \"$HOME\" ] && [ \"$CLAUDE_CONFIG_DIR\" = \"$HOME\" ] && [ \"$XDG_CONFIG_HOME\" = \"$HOME\" ] || exit 4\n[ -z \"${OPENAI_API_KEY-}${NODE_OPTIONS-}${KILO_LOCAL_API_KEY-}\" ] || exit 5\nprintf 'codex-cli 0.162.1\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	version, channel, err := managedToolInstalledVersion(context.Background(), "codex-cli", executable, runtime.GOOS, runtime.GOARCH)
	if err != nil || version != "0.162.1" || channel != "stable" {
		t.Fatal(version, channel, err)
	}
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "must not be read or changed" {
		t.Fatal("personal state changed", err)
	}
}

func TestManagedToolCLIProbeRejectsUnboundedFailedAndCancelledResponses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixture")
	}
	for name, body := range map[string]string{
		"failed":       "printf 'codex-cli 9.9.9\\n'; exit 3",
		"oversized":    "i=0; while [ \"$i\" -lt 2000 ]; do printf 0123456789; i=$((i+1)); done; printf ' 9.9.9\\n'",
		"unrecognized": "printf 'not a version\\n'",
	} {
		t.Run(name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "codex")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, _, err := managedToolInstalledVersion(context.Background(), "codex-cli", binary, runtime.GOOS, runtime.GOARCH); err == nil {
				t.Fatal("invalid version response accepted")
			}
		})
	}
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := managedToolInstalledVersion(ctx, "claude", binary, runtime.GOOS, runtime.GOARCH); err == nil || time.Since(started) > 3*time.Second {
		t.Fatal("version check did not stop promptly", err)
	}
}

func TestManagedToolExternalClaudeVersionDoesNotInventUpdateTrack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixture")
	}
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '2.1.288 (Claude Code)\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	version, channel, err := managedToolInstalledVersion(context.Background(), "claude", binary, runtime.GOOS, runtime.GOARCH)
	if err != nil || version != "2.1.288" || channel != "" {
		t.Fatalf("unproven external update track: version=%q channel=%q err=%v", version, channel, err)
	}
}

func managedToolBinaryPlistFixture(t *testing.T, bundle, executable string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(bundle, "Contents", "MacOS", executable)
	if err := os.WriteFile(binary, []byte("synthetic executable; must never run"), 0700); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	metadata := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>` + executable + `</string><key>CFBundleShortVersionString</key><string>1.2.3</string></dict></plist>`
	if err := os.WriteFile(plist, []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/usr/bin/plutil", "-convert", "binary1", "--", plist)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("binary metadata fixture: %v %s", err, output)
	}
	return binary
}

func TestManagedToolBinaryPlistMetadataAndDesktopLaunch(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS binary plist conversion")
	}
	for _, client := range []string{"codex", "claude-desktop"} {
		t.Run(client, func(t *testing.T) {
			a := launchTestApp(t)
			bundle := filepath.Join(t.TempDir(), "Managed.app")
			wantExecutable := managedToolBinaryPlistFixture(t, bundle, "Native Agent")
			a.launcher.resolve = func(string, string) (string, error) { return bundle, nil }
			a.claudeDesktopCheckRunning = func(string) (bool, error) { return false, nil }
			if client == "claude-desktop" {
				prepareDesktopLaunchTest(t, a)
			} else {
				launchPrepareFixture(t, a, client)
			}
			plan, err := a.planClientLaunch(clientLaunchRequest{Client: client}, a.launchRuntime())
			if err != nil || plan.Executable != wantExecutable {
				t.Fatalf("binary plist launch: executable=%q err=%v", plan.Executable, err)
			}
			if got := t3CodeBundleExecutable(bundle); got != wantExecutable {
				t.Fatalf("T3/Synara bundle resolution: %q", got)
			}
			version, _, err := managedToolInstalledVersion(context.Background(), client, bundle, "darwin", "arm64")
			if err != nil || version != "1.2.3" {
				t.Fatal(version, err)
			}
		})
	}
}

func TestManagedToolDesktopVersionNeverStartsApplication(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			if runtime.GOOS == "windows" && platform == "darwin" {
				t.Skip("POSIX bundle executable-mode fixture")
			}
			binary := syntheticT3CodeExecutable(t, t.TempDir(), platform, t3CodeNightlyVersion)
			version, channel, err := managedToolInstalledVersion(context.Background(), "t3-code", binary, platform, "arm64")
			if err != nil || version != t3CodeNightlyVersion || channel != "nightly" {
				t.Fatal(version, channel, err)
			}
		})
	}
	bundle := filepath.Join(t.TempDir(), "Codex.app")
	if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0700); err != nil {
		t.Fatal(err)
	}
	metadata := `<plist><dict><key>CFBundleShortVersionString</key><string>26.1007.21159</string><key>CFBundleVersion</key><string>20052</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	version, channel, err := managedToolInstalledVersion(context.Background(), "codex", bundle, "darwin", "arm64")
	if err != nil || version != "26.1007.21159" || channel != "stable" {
		t.Fatal(version, channel, err)
	}
}

func TestManagedT3CodeChannelUsesPackagedVersionNotBranding(t *testing.T) {
	tests := []struct {
		name, version, bundleName, releaseChannel string
		valid                                     bool
	}{
		{"stable release branded Alpha", "0.0.45", "T3 Code (Alpha).app", "stable", true},
		{"stable release branded Beta", "0.1.0", "T3 Code (Beta).app", "stable", true},
		{"nightly release", t3CodeNightlyVersion, "T3 Code (Nightly).app", "nightly", true},
		{"stable release cannot masquerade as nightly", "0.1.0", "T3 Code (Beta).app", "nightly", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			original := syntheticT3CodeVersionExecutable(t, t.TempDir(), "macos", tc.version)
			bundle := filepath.Join(filepath.Dir(original), tc.bundleName)
			if original != bundle {
				if err := os.Rename(original, bundle); err != nil {
					t.Fatal("rename fixture to release branding:", err)
				}
			}

			version, channel, err := managedToolInstalledVersion(context.Background(), "t3-code", bundle, "macos", runtime.GOARCH)
			if err != nil || version != tc.version || channel != managedToolReleaseChannel(tc.version) {
				t.Fatalf("wrong packaged release identity: version=%q channel=%q err=%v", version, channel, err)
			}

			err = verifyManagedToolVersion(context.Background(), managedToolRelease{
				Client: "t3-code", Version: tc.version, Channel: tc.releaseChannel,
			}, bundle, "macos", runtime.GOARCH)
			if (err == nil) != tc.valid {
				t.Fatalf("verification validity = %t, want %t (err=%v)", err == nil, tc.valid, err)
			}
		})
	}
}

func TestManagedToolVersionsRetainPrereleaseLabels(t *testing.T) {
	for _, label := range []string{"1.0.4-beta.19", "0.0.46-nightly.20261003.2610", "2.0.0+build.12"} {
		if got, err := managedToolVersionLabel(label); err != nil || got != label {
			t.Fatal(got, err)
		}
	}
	for _, label := range []string{"", "2.0.0\nheader", "../2.0.0", "2.0.0<unsafe>", strings.Repeat("x", 129)} {
		if _, err := managedToolVersionLabel(label); err == nil {
			t.Fatal("invalid label accepted", label)
		}
	}
}

func TestManagedToolZedPreviewMatchesPublishedVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixture")
	}
	// The v1.24.1-pre source has Cargo version 1.24.1 and RELEASE_CHANNEL
	// preview. Its Linux CLI prints "Zed preview <RELEASE_VERSION> ...";
	// the macOS bundle carries the numeric version and Preview identity.
	for _, test := range []struct{ name, output, wantVersion, wantChannel string }{
		{"preview", "Zed preview 1.24.1 1234567 – /synthetic/zed-preview.app/libexec/zed-editor", "1.24.1-pre", "preview"},
		{"preview already suffixed", "Zed preview 1.24.1-pre 1234567 – /synthetic/zed-preview.app/libexec/zed-editor", "1.24.1-pre", "preview"},
		{"stable in preview parent", "Zed 1.23.2 1234567 – /synthetic/preview/zed.app/libexec/zed-editor", "1.23.2", "stable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary := filepath.Join(t.TempDir(), "zed")
			script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 2\nprintf '%s\\n' " + helperShellQuote(test.output) + "\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			version, channel, err := managedToolInstalledVersion(context.Background(), "zed", binary, "linux", runtime.GOARCH)
			if err != nil || version != test.wantVersion || channel != test.wantChannel {
				t.Fatalf("Zed version=%q channel=%q err=%v", version, channel, err)
			}
		})
	}
	bundle := filepath.Join(t.TempDir(), "Zed Preview.app")
	if err := os.MkdirAll(filepath.Join(bundle, "Contents"), 0700); err != nil {
		t.Fatal(err)
	}
	plist := `<plist><dict><key>CFBundleShortVersionString</key><string>1.24.1</string><key>CFBundleIdentifier</key><string>dev.zed.Zed-Preview</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	version, channel, err := managedToolInstalledVersion(context.Background(), "zed", bundle, "darwin", runtime.GOARCH)
	if err != nil || version != "1.24.1-pre" || channel != "preview" {
		t.Fatalf("Zed Preview bundle version=%q channel=%q err=%v", version, channel, err)
	}
}
