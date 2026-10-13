package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func syntheticSynaraAppImage(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 64)
	copy(header, []byte{'\x7f', 'E', 'L', 'F', 2, 1, 1, 0, 'A', 'I', 2})
	if err := os.WriteFile(path, header, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSynaraDiscoversRawAppImageWithoutExecutingOrAdoptingMetadata(t *testing.T) {
	home := t.TempDir()
	stable := syntheticSynaraAppImage(t, filepath.Join(home, "Downloads", "Synara-1.0.3-x86_64.AppImage"))
	beta := syntheticSynaraAppImage(t, filepath.Join(home, "Applications", "Synara-Beta-1.0.2-beta.1-x86_64.AppImage"))
	// Other AppImages and script impostors are not automatically called Synara.
	syntheticSynaraAppImage(t, filepath.Join(home, "Downloads", "another-app.AppImage"))
	if err := os.WriteFile(filepath.Join(home, "Downloads", "Synara-9.0.0-x86_64.AppImage"), []byte("#!/bin/sh\necho never-run\n"), 0700); err != nil {
		t.Fatal(err)
	}
	got := synaraAppImageCandidates(home)
	if len(got) != 2 || got[0] != beta || got[1] != stable {
		t.Fatal("unexpected bounded AppImage candidates", got)
	}
	resolved, err := resolveSynaraCandidates([]string{stable})
	if err != nil || resolved != stable {
		t.Fatal("raw package was not discovered", resolved, err)
	}
	metadata, err := synaraPackageMetadata(resolved, "linux")
	if err == nil || err.Error() != synaraAppImagePending || metadata != (synaraPackageInfo{}) {
		t.Fatal("unverified AppImage acquired package metadata", metadata, err)
	}
	a := launchTestApp(t)
	a.config.Language = "en"
	if err := a.validateSelectedSynaraExecutable(stable, "linux"); err != nil {
		t.Fatal("explicit AppImage candidate rejected", err)
	}
	if err := a.synaraAvailability(stable, clientLaunchRuntime{platform: "linux"}); err == nil || err.Error() != synaraAppImagePending {
		t.Fatal("raw AppImage passed launch prerequisites", err)
	}
	data, err := os.ReadFile(stable)
	if err != nil || len(data) != 64 || !bytes.Equal(data[8:11], []byte{'A', 'I', 2}) {
		t.Fatal("discovery modified original AppImage", err)
	}
	// An adjacent ASAR, renamed file or symlink must not turn a package runtime
	// into an extracted executable that Kilo would launch without verification.
	native := syntheticSynaraPackageExecutable(t, t.TempDir(), "linux", map[string]string{"name": "synara-desktop", "synaraDesktopFlavor": "production", "version": "1.0.3"})
	if err := os.WriteFile(native, data, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := synaraPackageMetadata(native, "linux"); err == nil || err.Error() != synaraAppImagePending {
		t.Fatal("renamed AppImage or adjacent metadata bypassed the launch guard", err)
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "synara")
		if err := os.Symlink(native, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := synaraPackageMetadata(alias, "linux"); err == nil || err.Error() != synaraAppImagePending {
			t.Fatal("renamed symlink bypassed the AppImage guard", err)
		}
	}
	a.launcher.platform = "linux"
	a.cliPathsMu.Lock()
	a.synaraExecutable = stable
	a.cliPathsMu.Unlock()
	w := adminRequest(a, "clients/launch", "")
	var response struct {
		Clients map[string]clientLaunchAvailability `json:"clients"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("could not inspect candidate", w.Code, w.Body.String())
	}
	status := response.Clients["synara"]
	if !status.Installed || status.Available || status.Path != stable || status.Reason != synaraAppImagePending {
		t.Fatal("AppImage candidate was confused with missing or launch-ready app", status)
	}
}

func TestSynaraSelectionRejectsEphemeralMountsAndPreservesManagedIntegrity(t *testing.T) {
	a := launchTestApp(t)
	for _, path := range []string{"relative/Synara", "~/Synara", "/tmp/bad\npath", "/proc/123/exe", "/tmp/.mount_Synara123/synara", "/home/user/app (deleted)"} {
		if validateSynaraExecutablePath(path) == nil {
			t.Fatal("unsafe or ephemeral selection accepted", path)
		}
	}
	if validateSynaraExecutablePath("") != nil {
		t.Fatal("automatic detection rejected")
	}
	ephemeral := syntheticSynaraAppImage(t, filepath.Join(t.TempDir(), ".mount_Synara123", "Synara-1.0.3-x86_64.AppImage"))
	if path, err := resolveSynaraCandidates([]string{ephemeral}); path != "" || err == nil {
		t.Fatal("temporary mount discovered as durable installation", path, err)
	}
	if runtime.GOOS == "windows" {
		return // Unix managed tool store is disabled on Windows.
	}
	alias := filepath.Join(t.TempDir(), "Synara-1.0.3-x86_64.AppImage")
	if err := os.Symlink(ephemeral, alias); err != nil {
		t.Fatal(err)
	}
	if a.validateSelectedSynaraExecutable(alias, "linux") == nil {
		t.Fatal("symlink to temporary mount accepted")
	}
	record := managedToolRegistryFixture(t, a.dir, "synara", "current")
	// Give the synthetic executable valid Synara metadata; the registry still
	// verifies its entrypoint and ASAR before an explicit path is activated.
	packagePath := syntheticSynaraPackageExecutable(t, t.TempDir(), "linux", map[string]string{"name": "synara-desktop", "synaraDesktopFlavor": "production", "version": "1.0.3"})
	metadata, err := os.ReadFile(filepath.Join(filepath.Dir(packagePath), "resources", "app.asar"))
	if err != nil {
		t.Fatal(err)
	}
	resources := filepath.Join(filepath.Dir(record.Executable), "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "app.asar"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	record.Digest, err = managedToolFingerprint(record.Executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagedTool(a.dir, record); err != nil {
		t.Fatal(err)
	}
	if err := a.validateSelectedSynaraExecutable(record.Executable, "linux"); err != nil {
		t.Fatal("verified current Synara rejected", err)
	}
	retained := managedToolRegistryFixture(t, a.dir, "synara", "retained")
	if a.validateSelectedSynaraExecutable(retained.Executable, "linux") == nil {
		t.Fatal("retained installation bypassed current pointer")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(record.Executable, alias); err != nil {
		t.Fatal(err)
	}
	if a.validateSelectedSynaraExecutable(alias, "linux") == nil {
		t.Fatal("external symlink alias bypassed current pointer")
	}
	if err := os.WriteFile(record.Executable, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.validateSelectedSynaraExecutable(record.Executable, "linux"); err == nil || !strings.Contains(err.Error(), "verified current") {
		t.Fatal("tampered managed executable accepted", err)
	}
}

func TestSynaraLinuxPolicyBlocksPreparationAndIsRecheckedAtLaunch(t *testing.T) {
	a := synaraTestApp(t, "linux")
	rt := a.launchRuntime()
	if rt.platform != "linux" {
		return // The native Windows fixture keeps its actual binary contract.
	}
	binary, err := rt.resolve("synara", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{managedToolLinuxSandboxRequirement, managedToolLinuxSandboxUnknown} {
		a.synaraCheckSystem = func(string) string { return reason }
		if err := a.synaraAvailability(binary, rt); err == nil || err.Error() != reason {
			t.Fatal("blocked host was considered ready", reason, err)
		}
		if err := a.prepareSynara(testModelLibrary(), rt, binary); err == nil || err.Error() != reason {
			t.Fatal("blocked host prepared private workspace", reason, err)
		}
		if _, err := os.Stat(synaraPaths(a.dir).Root); !os.IsNotExist(err) {
			t.Fatal("blocked preparation wrote private files", err)
		}
	}
	a.synaraCheckSystem = func(string) string { return "" }
	prepareSynaraFixture(t, a)
	saved, err := os.ReadFile(synaraPaths(a.dir).Selection)
	if err != nil {
		t.Fatal(err)
	}
	a.synaraCheckSystem = func(string) string { return managedToolLinuxSandboxRequirement }
	plan := clientLaunchPlan{Executable: binary, Env: map[string]string{}}
	if err := a.applySynaraLaunch(&plan, rt); err == nil || err.Error() != managedToolLinuxSandboxRequirement {
		t.Fatal("changed policy did not block prepared launch", err)
	}
	after, err := os.ReadFile(synaraPaths(a.dir).Selection)
	if err != nil || !bytes.Equal(saved, after) || len(plan.Args) != 0 || len(plan.Env) != 0 {
		t.Fatal("blocked launch changed selection or process plan", err)
	}
	for _, reason := range []string{managedToolLinuxSandboxRequirement, managedToolLinuxSandboxUnknown} {
		a.synaraCheckSystem = func(string) string { return reason }
		w := adminRequest(a, "clients/synara", "{}")
		if w.Code != 409 || !strings.Contains(w.Body.String(), reason) {
			t.Fatal("unchanged preparation bypassed current host policy", w.Code, w.Body.String())
		}
		current, err := os.ReadFile(synaraPaths(a.dir).Selection)
		if err != nil || !bytes.Equal(saved, current) {
			t.Fatal("rejected preparation changed the previous selection", err)
		}
	}
}

func TestSynaraPreparationAndInstallationSelectionAreOneTransaction(t *testing.T) {
	a := synaraTestApp(t, runtime.GOOS)
	next := syntheticSynaraExecutable(t, t.TempDir(), a.launcher.platform)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	previous := a.launcher.resolve
	a.launcher.resolve = func(client, custom string) (string, error) {
		if client == "synara" {
			close(entered)
			<-release
		}
		return previous(client, custom)
	}
	finished := make(chan int, 1)
	go func() { finished <- adminRequest(a, "clients/synara", "{}").Code }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not resolve Synara")
	}
	w := postCLISelection(t, a, map[string]any{"version": 1, "synaraExecutable": next}, 409)
	if !strings.Contains(w.Body.String(), "already being prepared") || a.cliExecutable("synara") != "" {
		t.Fatal("installation changed while preparation used an earlier resolver", w.Code, w.Body.String())
	}
	// Unblock before awaiting the worker, and leave the deferred close safe.
	release <- struct{}{}
	select {
	case code := <-finished:
		if code != 200 {
			t.Fatal("unchanged selected installation did not prepare", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not finish")
	}
}
