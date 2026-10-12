//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func managedToolWindowsProbeFixture(t *testing.T, extension, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	probes := filepath.Join(root, "private probes")
	if err := os.Mkdir(probes, 0700); err != nil {
		t.Fatal(err)
	}
	// Capture only this test's scratch directories and generated batch wrappers.
	// The executable stays outside that directory so cleanup must leave it empty.
	t.Setenv("TMP", probes)
	t.Setenv("TEMP", probes)
	directory := filepath.Join(root, "npm %PATH% !^& space")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "opencode"+extension)
	if err := os.WriteFile(binary, []byte("@echo off\r\n"+strings.ReplaceAll(body, "\n", "\r\n")+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return binary, probes
}

func managedToolWindowsProbeCleaned(t *testing.T, probes string) {
	t.Helper()
	entries, err := os.ReadDir(probes)
	if err != nil || len(entries) != 0 {
		t.Fatalf("version check left private directories or wrappers: %v, %v", entries, err)
	}
}

func TestManagedToolWindowsBatchProbeUsesPrivateStateAndFixedArguments(t *testing.T) {
	for _, extension := range []string{".cmd", ".bat"} {
		t.Run(extension, func(t *testing.T) {
			for _, name := range []string{"OPENAI_API_KEY", "ANTHROPIC_AUTH_TOKEN", "KILO_LOCAL_API_KEY", "NODE_OPTIONS"} {
				t.Setenv(name, "ambient-value-must-not-enter-probe")
			}
			t.Setenv("COMSPEC", "ambient-shell-must-not-run")
			personal := t.TempDir()
			auth := filepath.Join(personal, "auth.json")
			if err := os.WriteFile(auth, []byte("personal state must remain intact"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", personal)
			t.Setenv("CLAUDE_CONFIG_DIR", personal)
			binary, probes := managedToolWindowsProbeFixture(t, extension, `if not "%~1"=="--version" exit /b 21
if not "%~2"=="" exit /b 22
if not "%CD%"=="%HOME%" exit /b 23
if not "%USERPROFILE%"=="%HOME%" exit /b 24
if not "%CODEX_HOME%"=="%HOME%" exit /b 25
if not "%CLAUDE_CONFIG_DIR%"=="%HOME%" exit /b 26
if not "%CLAUDE_SECURESTORAGE_CONFIG_DIR%"=="%HOME%" exit /b 27
if not "%XDG_CONFIG_HOME%"=="%HOME%" exit /b 28
if not "%XDG_DATA_HOME%"=="%HOME%" exit /b 29
if not "%XDG_STATE_HOME%"=="%HOME%" exit /b 30
if not "%XDG_CACHE_HOME%"=="%HOME%" exit /b 31
if not "%TMP%"=="%HOME%" exit /b 32
if not "%TEMP%"=="%HOME%" exit /b 33
if not "%TMPDIR%"=="%HOME%" exit /b 34
if defined OPENAI_API_KEY exit /b 35
if defined ANTHROPIC_AUTH_TOKEN exit /b 36
if defined KILO_LOCAL_API_KEY exit /b 37
if defined NODE_OPTIONS exit /b 38
if "%COMSPEC%"=="ambient-shell-must-not-run" exit /b 39
echo opencode 1.14.30
echo private-home=%HOME%
echo private-wrapper=%KILO_LAUNCH_WRAPPER%`)
			output, err := managedToolProbeOutput(context.Background(), binary)
			if err != nil || !strings.HasPrefix(output, "opencode 1.14.30\r\n") {
				t.Fatalf("batch version check: output=%q err=%v", output, err)
			}
			values := map[string]string{}
			for _, line := range strings.Split(output, "\n") {
				if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					values[key] = value
				}
			}
			for _, key := range []string{"private-home", "private-wrapper"} {
				path := values[key]
				if !filepath.IsAbs(path) || strings.EqualFold(path, personal) {
					t.Fatalf("probe did not use private %s: %q", key, path)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("probe did not remove %s: %v", key, err)
				}
			}
			managedToolWindowsProbeCleaned(t, probes)
			// Exercise the exact new OpenCode 1 launch guard with a real npm-style
			// wrapper, not a synthetic version callback.
			if err := (&app{}).checkOpenCodeVersion(binary, 1); err != nil {
				t.Fatal("OpenCode 1 wrapper rejected by launch guard:", err)
			}
			managedToolWindowsProbeCleaned(t, probes)
			data, err := os.ReadFile(auth)
			if err != nil || string(data) != "personal state must remain intact" {
				t.Fatal("personal state changed", err)
			}
		})
	}
}

func TestManagedToolWindowsBatchProbeRejectsInvalidResponsesAndCleansUp(t *testing.T) {
	for name, body := range map[string]string{
		"failed":       "echo opencode 1.14.30\nexit /b 3",
		"oversized":    "for /l %%i in (1,1,2000) do @echo 0123456789\necho opencode 1.14.30",
		"unrecognized": "echo not a version",
	} {
		t.Run(name, func(t *testing.T) {
			binary, probes := managedToolWindowsProbeFixture(t, ".cmd", body)
			if _, err := managedToolProbeVersion(context.Background(), binary); err == nil {
				t.Fatal("invalid batch version response accepted")
			}
			managedToolWindowsProbeCleaned(t, probes)
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		// The fixture has no descendants: cancellation must terminate the system
		// cmd process itself before removing its working directory and wrapper.
		binary, probes := managedToolWindowsProbeFixture(t, ".bat", ":spin\ngoto spin")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		started := time.Now()
		if _, err := managedToolProbeVersion(ctx, binary); err == nil || time.Since(started) > 3*time.Second {
			t.Fatal("batch version check did not stop promptly", err)
		}
		managedToolWindowsProbeCleaned(t, probes)
	})
}
