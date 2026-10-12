package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSynaraOfficialChannelsUseExactPackageIdentity(t *testing.T) {
	for _, metadata := range []synaraPackageInfo{
		{Name: "synara-desktop", SynaraDesktopFlavor: "production", Version: "1.0.3"},
		{Name: "synara-desktop-beta", SynaraDesktopFlavor: "beta", Version: "1.0.4-beta.2"},
	} {
		t.Run(metadata.Name, func(t *testing.T) {
			binary := syntheticSynaraPackageExecutable(t, t.TempDir(), "linux", map[string]string{"name": metadata.Name, "synaraDesktopFlavor": metadata.SynaraDesktopFlavor, "version": metadata.Version})
			got, err := synaraPackageMetadata(binary, "linux")
			if err != nil || got.Name != metadata.Name || got.SynaraDesktopFlavor != metadata.SynaraDesktopFlavor {
				t.Fatal(got, err)
			}
			version, channel, err := managedToolInstalledVersion(context.Background(), "synara", binary, "linux", "amd64")
			if err != nil || version != metadata.Version || channel != synaraPackageChannel(metadata) {
				t.Fatal(version, channel, err)
			}
		})
	}
	for _, metadata := range []synaraPackageInfo{
		{Name: "synara-desktop", SynaraDesktopFlavor: "beta"},
		{Name: "synara-desktop-beta", SynaraDesktopFlavor: "production"},
		{Name: "synara-desktop", SynaraDesktopFlavor: "stable"},
		{Name: "synara-desktop-canary", SynaraDesktopFlavor: "canary"},
		{Name: "other", SynaraDesktopFlavor: "production"},
	} {
		if synaraPackageChannel(metadata) != "" {
			t.Fatal("nonrelease identity accepted", metadata)
		}
	}
}

func TestSynaraStableRuntimePreservesRealIdentityAndChecksBundleMetadata(t *testing.T) {
	metadata := map[string]any{"name": "synara-desktop", "synaraDesktopFlavor": "production", "version": "1.0.3", "main": "apps/desktop/main.js", "synaraCommitHash": "real-stable-source", "futureMetadata": map[string]bool{"retained": true}}
	source, _ := json.Marshal(metadata)
	updated, main, err := synaraRuntimePackageData(source)
	if err != nil || main != "apps/desktop/main.js" {
		t.Fatal(main, err)
	}
	metadata["main"] = "bootstrap.cjs"
	want, _ := json.Marshal(metadata)
	if !bytes.Equal(updated, want) {
		t.Fatal("stable runtime replaced its release identity or metadata")
	}
	old, next := strings.Repeat("a", 64), strings.Repeat("b", 64)
	plist := []byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.emanueledipietro.synara</string><key>hash</key><string>` + old + `</string></dict></plist>`)
	patched, err := synaraRuntimeInfoPlist(plist, old, next)
	if err != nil || !bytes.Contains(patched, []byte("ai.kilo.synara-private-runtime")) || !bytes.Contains(patched, []byte(next)) {
		t.Fatal("stable private bundle identity not patched", err)
	}
	ambiguous := append(append([]byte{}, plist...), []byte("<string>com.emanueledipietro.synara.beta</string>")...)
	if _, err := synaraRuntimeInfoPlist(ambiguous, old, next); err == nil {
		t.Fatal("ambiguous release identity accepted")
	}
	unknown := bytes.ReplaceAll(plist, []byte("com.emanueledipietro.synara"), []byte("another.vendor"))
	if _, err := synaraRuntimeInfoPlist(unknown, old, next); err == nil {
		t.Fatal("foreign application accepted")
	}
}

func TestSynaraChannelChangeRequiresPreparationAndKeepsChats(t *testing.T) {
	owner := synaraTestApp(t, "linux")
	prepareSynaraFixture(t, owner)
	saved, err := owner.readSynaraPrepared()
	if err != nil {
		t.Fatal(err)
	}
	paths := synaraPaths(owner.dir)
	chat := filepath.Join(paths.Data, "retained-chat.fixture")
	if err := os.MkdirAll(paths.Data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chat, []byte("existing private conversation"), 0600); err != nil {
		t.Fatal(err)
	}
	stable := syntheticSynaraPackageExecutable(t, t.TempDir(), owner.launcher.platform, map[string]string{"name": "synara-desktop", "synaraDesktopFlavor": "production", "version": "1.0.3"})
	previous := owner.launcher.resolve
	owner.launcher.resolve = func(client, custom string) (string, error) {
		if client == "synara" {
			return stable, nil
		}
		return previous(client, custom)
	}
	if owner.synaraReady(saved, stable, owner.launchRuntime()) {
		t.Fatal("channel switch reused preparation for another executable")
	}
	prepareSynaraFixture(t, owner)
	saved, err = owner.readSynaraPrepared()
	if err != nil || !owner.synaraReady(saved, stable, owner.launchRuntime()) {
		t.Fatal("stable preparation not ready", err)
	}
	if data, err := os.ReadFile(chat); err != nil || string(data) != "existing private conversation" {
		t.Fatal("channel switch changed private chats", err)
	}
	if len(saved.Providers) != 4 {
		t.Fatal("four account isolation changed")
	}
}

func TestSynaraDiscoveryIncludesStableApplications(t *testing.T) {
	for _, platform := range []string{"darwin", "windows", "linux"} {
		home := t.TempDir()
		local := filepath.Join(home, "Local")
		candidates := synaraInstallationCandidates(platform, home, local)
		want := filepath.Join(home, "Applications", "Synara.app")
		if platform == "windows" {
			want = filepath.Join(local, "Programs", "Synara", "Synara.exe")
		}
		if platform == "linux" {
			want = filepath.Join(home, ".local", "share", "synara", "synara")
		}
		found := false
		for _, candidate := range candidates {
			if candidate == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("stable application path missing", platform, want)
		}
	}
}
