package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOMPDiscoveryPrefersExecutablePATHBeforeLocalInstall(t *testing.T) {
	for _, entry := range []string{"executable", "missing", "directory", "not-executable"} {
		t.Run(entry, func(t *testing.T) {
			if entry == "not-executable" && runtime.GOOS == "windows" {
				t.Skip("Windows does not use Unix executable permission bits")
			}
			fixtureHome, pathDirectory := t.TempDir(), t.TempDir()
			// Keep discovery inside this fixture even on a developer machine with
			// an installed Oh My Pi, a custom installer path, or npm shims.
			t.Setenv("HOME", fixtureHome)
			t.Setenv("USERPROFILE", fixtureHome)
			t.Setenv("LOCALAPPDATA", filepath.Join(fixtureHome, "AppData", "Local"))
			t.Setenv("APPDATA", filepath.Join(fixtureHome, "AppData", "Roaming"))
			t.Setenv("PI_INSTALL_DIR", "")
			t.Setenv("PATH", pathDirectory)
			name := "omp"
			if runtime.GOOS == "windows" {
				name += ".exe"
				t.Setenv("PATHEXT", ".EXE;.CMD;.BAT")
			}
			fallback := filepath.Join(fixtureHome, ".local", "bin", name)
			if err := os.MkdirAll(filepath.Dir(fallback), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fallback, []byte("fallback fixture; never executed"), 0700); err != nil {
				t.Fatal(err)
			}
			pathEntry := filepath.Join(pathDirectory, name)
			want := fallback
			switch entry {
			case "executable":
				if err := os.WriteFile(pathEntry, []byte("PATH fixture; never executed"), 0700); err != nil {
					t.Fatal(err)
				}
				want = pathEntry
			case "directory":
				if err := os.Mkdir(pathEntry, 0700); err != nil {
					t.Fatal(err)
				}
			case "not-executable":
				if err := os.WriteFile(pathEntry, []byte("non-executable fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveLaunchClient("omp", "")
			if err != nil || got != want {
				t.Fatalf("Oh My Pi discovery returned %q, %v; want %q", got, err, want)
			}
		})
	}
}
