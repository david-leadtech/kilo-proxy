package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func museWindowsLauncherFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Muse ' installation café 日本語")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "muse.cmd")
	binary := filepath.Join(dir, "muse-bin-1.4.6-R6000.1.exe")
	for path, data := range map[string]string{shim: museWindowsOfficialShim, filepath.Join(dir, ".muse-version"): "1.4.6-R6000.1\r\n", binary: "synthetic native Muse"} {
		if err := os.WriteFile(path, []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return shim, binary
}

func TestMuseWindowsOfficialLauncherResolvesExactNativeBinary(t *testing.T) {
	for _, style := range []string{"official-crlf", "lf", "utf8-bom"} {
		t.Run(style, func(t *testing.T) {
			shim, binary := museWindowsLauncherFixture(t)
			data := museWindowsOfficialShim
			if style == "lf" {
				data = strings.ReplaceAll(data, "\r\n", "\n")
			} else if style == "utf8-bom" {
				data = "\xef\xbb\xbf" + data
			}
			if err := os.WriteFile(shim, []byte(data), 0700); err != nil {
				t.Fatal(err)
			}
			if got, err := resolveMuseWindowsLauncher(shim, "windows"); err != nil || got != binary {
				t.Fatal("official launcher did not resolve its exact recorded native binary", got, err)
			}
			for _, platform := range []string{"darwin", "macos", "linux"} {
				if got, err := resolveMuseWindowsLauncher(shim, platform); err != nil || got != shim {
					t.Fatal("Windows layout changed another platform", platform, got, err)
				}
			}
		})
	}
}

func TestMuseWindowsOfficialLauncherFailsClosedForInvalidRecordedBinary(t *testing.T) {
	for _, failure := range []string{"missing-state", "empty-state", "numeric-version", "traversal", "newline-version", "nul-version", "oversized-state", "missing-binary", "binary-directory", "state-directory", "state-link", "binary-link"} {
		t.Run(failure, func(t *testing.T) {
			shim, binary := museWindowsLauncherFixture(t)
			state := filepath.Join(filepath.Dir(shim), ".muse-version")
			var err error
			switch failure {
			case "missing-state":
				err = os.Remove(state)
			case "empty-state", "numeric-version", "traversal", "newline-version", "nul-version", "oversized-state":
				value := map[string]string{"empty-state": "", "numeric-version": "1.4.6", "traversal": "../../other", "newline-version": "1.4.6-R6000.1\n1.4.6-R6001.1", "nul-version": "1.4.6-R6000.1\x00", "oversized-state": strings.Repeat("1", 129)}[failure]
				err = os.WriteFile(state, []byte(value), 0700)
			case "missing-binary":
				err = os.Remove(binary)
			case "binary-directory", "state-directory":
				path := binary
				if failure == "state-directory" {
					path = state
				}
				if err = os.Remove(path); err == nil {
					err = os.Mkdir(path, 0700)
				}
			case "state-link", "binary-link":
				path, contents := binary, "synthetic native Muse"
				if failure == "state-link" {
					path, contents = state, "1.4.6-R6000.1"
				}
				target := filepath.Join(t.TempDir(), "unrelated")
				if err = os.WriteFile(target, []byte(contents), 0700); err == nil {
					if err = os.Remove(path); err == nil {
						err = os.Symlink(target, path)
						if err != nil && runtime.GOOS == "windows" {
							t.Skip("Windows symlink creation unavailable:", err)
						}
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			// Another installed version may exist, but never substitutes for the
			// exact recorded one when this installation is incomplete or invalid.
			if err := os.WriteFile(filepath.Join(filepath.Dir(shim), "muse-bin-1.4.6-R6002.1.exe"), []byte("other installed version"), 0700); err != nil {
				t.Fatal(err)
			}
			if got, err := resolveMuseWindowsLauncher(shim, "windows"); err == nil || got != "" {
				t.Fatal("invalid official installation fell back or accepted altered metadata", got, err)
			}
		})
	}
}

func TestMuseWindowsLauncherNeverInterpretsAnUnrecognizedCustomScript(t *testing.T) {
	for _, source := range []string{"@echo off\r\necho custom\r\n", museWindowsOfficialShim + "echo unrelated\r\n", strings.ReplaceAll(museWindowsOfficialShim, "%~dp0.muse-launcher.ps1", "%~dp0.other.ps1"), strings.Repeat("x", 4097)} {
		shim, _ := museWindowsLauncherFixture(t)
		if err := os.WriteFile(shim, []byte(source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(filepath.Dir(shim), ".muse-version")); err != nil {
			t.Fatal(err)
		}
		if got, err := resolveMuseWindowsLauncher(shim, "windows"); err != nil || got != shim {
			t.Fatal("unrecognized script was reinterpreted using sibling metadata", got, err)
		}
	}
	shim, binary := museWindowsLauncherFixture(t)
	for _, path := range []string{binary, filepath.Join(filepath.Dir(shim), "custom-muse.cmd"), filepath.Join(filepath.Dir(shim), "muse.bat")} {
		if got, err := resolveMuseWindowsLauncher(path, "windows"); err != nil || got != path {
			t.Fatal("another explicit executable was replaced", got, err)
		}
	}
}

func TestMuseWindowsDiscoveryResolvesOfficialLauncherFromSameInstallation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PATH and LocalAppData discovery")
	}
	shim, binary := museWindowsLauncherFixture(t)
	t.Setenv("PATH", filepath.Dir(shim))
	if got, err := resolveLaunchClient("muse", ""); err != nil || got != binary {
		t.Fatal("PATH discovery did not resolve the official native binary", got, err)
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLaunchClient("muse", ""); err == nil || got != "" {
		t.Fatal("broken first installation fell back to another Muse installation", got, err)
	}
}

func TestMuseWindowsSelectedOfficialLauncherUsesItsNativeBinaryWithoutFallback(t *testing.T) {
	shim, binary := museWindowsLauncherFixture(t)
	a := launchTestApp(t)
	a.launcher.platform = "windows"
	postCLISelection(t, a, map[string]any{"version": 1, "museExecutable": shim}, 200)
	if got, err := a.launchRuntime().resolve("muse", ""); err != nil || got != binary {
		t.Fatal("shared resolver ignored the explicit official Muse installation", got, err)
	}
	if a.cliExecutable("muse") != shim {
		t.Fatal("resolution rewrote the user's explicit installation choice")
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if got, err := a.launchRuntime().resolve("muse", ""); err == nil || got != "" {
		t.Fatal("missing exact binary fell back to another installation", got, err)
	}
}
