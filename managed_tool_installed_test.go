package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in acceptance downloads an actual fixed upstream package and exercises
// the production installer in a disposable Kilo profile. Desktop applications
// are never opened; only native CLI --version probes may run in private homes.
func TestManagedToolOfficialInstallation(t *testing.T) {
	selection := os.Getenv("KILO_TEST_MANAGED_TOOL")
	if selection == "" {
		t.Skip("set KILO_TEST_MANAGED_TOOL=client[:channel] for isolated official-package acceptance")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatal("managed installation acceptance requires macOS or Linux")
	}
	client, channel, _ := strings.Cut(selection, ":")
	if !managedToolClientValid(client) {
		t.Fatal("choose a tool from the fixed managed catalog")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	release, err := (&vendorManagedToolCatalog{}).Latest(ctx, client, runtime.GOOS, runtime.GOARCH, channel)
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t)
	record, err := installManagedTool(ctx, a, release, func(phase string) { t.Log(phase) })
	if err != nil {
		t.Fatal(err)
	}
	stored, err := readManagedTool(a.dir, client)
	if err != nil || stored != record || record.Version != release.Version || record.Channel != release.Channel {
		t.Fatal("verified package was not atomically published", err)
	}
	path, err := a.resolveManagedTool(client, "")
	if err != nil || path != record.Executable {
		t.Fatal("launch did not select the managed package", err)
	}
	installed, _, err := managedToolInstalledVersion(ctx, client, path, runtime.GOOS, runtime.GOARCH)
	if err != nil || installed != release.Version {
		t.Fatalf("package version %q does not match release %q: %v", installed, release.Version, err)
	}
	t.Logf("verified %s %s (%s), %d bytes, sha256 %s", client, installed, record.Channel, release.Size, release.SHA256)
}
