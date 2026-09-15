package main

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeOpenDesignCodexFixture(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func openDesignCodexNativeFixture(platform, arch string) []byte {
	header := make([]byte, 128)
	switch platform {
	case "macos":
		binary.LittleEndian.PutUint32(header[:4], 0xfeedfacf)
		cpu := uint32(0x01000007)
		if arch == "arm64" {
			cpu = 0x0100000c
		}
		binary.LittleEndian.PutUint32(header[4:8], cpu)
	case "windows":
		copy(header, "MZ")
		binary.LittleEndian.PutUint32(header[60:64], 64)
		copy(header[64:], "PE\x00\x00")
		machine := uint16(0x8664)
		if arch == "arm64" {
			machine = 0xaa64
		}
		binary.LittleEndian.PutUint16(header[68:70], machine)
	}
	return header
}

func TestOpenDesignCodexWrapperUsesVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	wrapper := writeOpenDesignCodexFixture(t, filepath.Join(root, "bin", "codex"), []byte("#!/usr/bin/env node\n// package has no optional binary"))
	apps := filepath.Join(root, "Applications")
	// Skip another wrapper and the wrong architecture, even when executable.
	writeOpenDesignCodexFixture(t, filepath.Join(apps, "Codex.app", "Contents", "Resources", "codex"), []byte("#!/bin/sh\nexit 1"))
	wrong := filepath.Join(root, "wrong")
	writeOpenDesignCodexFixture(t, filepath.Join(wrong, "ChatGPT.app", "Contents", "Resources", "codex"), openDesignCodexNativeFixture("macos", "amd64"))
	want := writeOpenDesignCodexFixture(t, filepath.Join(apps, "ChatGPT.app", "Contents", "Resources", "codex"), openDesignCodexNativeFixture("macos", "arm64"))
	got, err := resolveOpenDesignCodex(wrapper, "macos", "arm64", []string{wrong, apps})
	if err != nil || got != want {
		t.Fatalf("resolved %q, %v; want native bundle %q", got, err, want)
	}
	if got, err := resolveOpenDesignCodex(want, "macos", "arm64", nil); err != nil || got != want {
		t.Fatalf("already native executable changed: %q, %v", got, err)
	}
}

func TestOpenDesignNativeCodexHonorsHostFilePermissions(t *testing.T) {
	path := writeOpenDesignCodexFixture(t, filepath.Join(t.TempDir(), "codex"), openDesignCodexNativeFixture("macos", "arm64"))
	if !openDesignNativeCodex(path, "macos", "arm64") {
		t.Fatal("valid executable header was rejected")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if got, want := openDesignNativeCodex(path, "macos", "arm64"), runtime.GOOS == "windows"; got != want {
		t.Fatalf("non-executable Unix mode on %s: accepted=%v, want %v", runtime.GOOS, got, want)
	}
	if openDesignNativeCodex(filepath.Dir(path), "macos", "arm64") {
		t.Fatal("directory accepted as an executable")
	}
}

func TestOpenDesignCodexWrapperUsesNativePackageWithoutDesktop(t *testing.T) {
	for _, platform := range []string{"macos", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(platform+"/"+arch, func(t *testing.T) {
				root := t.TempDir()
				wrapper := writeOpenDesignCodexFixture(t, filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js"), []byte("#!/usr/bin/env node\n"))
				if platform == "windows" {
					wrapper = writeOpenDesignCodexFixture(t, filepath.Join(root, "codex.cmd"), []byte("@echo off\r\nnode codex.js %*\r\n"))
				}
				suffix, target := openDesignCodexTarget(platform, arch)
				name := "codex"
				if platform == "windows" {
					name += ".exe"
				}
				want := writeOpenDesignCodexFixture(t, filepath.Join(root, "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-"+suffix, "vendor", target, "codex", name), openDesignCodexNativeFixture(platform, arch))
				got, err := resolveOpenDesignCodex(wrapper, platform, arch, nil)
				if err != nil || got != want {
					t.Fatalf("resolved %q, %v; want package executable %q", got, err, want)
				}
			})
		}
	}
}

func TestOpenDesignCodexBrokenWrapperFailsBeforePreparation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"codex", "codex.js", "codex.cmd", "codex.bat"} {
		wrapper := writeOpenDesignCodexFixture(t, filepath.Join(root, name), []byte("#!/usr/bin/env node\n"))
		if got, err := resolveOpenDesignCodex(wrapper, "windows", "amd64", nil); got != "" || err == nil || !strings.Contains(err.Error(), "native Codex") {
			t.Fatalf("broken wrapper accepted: %q, %v", got, err)
		}
	}
}

func TestOpenDesignCLIResolutionPreservesOtherEnginesAndErrors(t *testing.T) {
	for _, engine := range []string{"claude", "opencode"} {
		rt := clientLaunchRuntime{resolve: func(id, custom string) (string, error) {
			if id != engine || custom != "" {
				t.Fatal("changed the engine discovery contract")
			}
			return "unchanged-other-engine", nil
		}}
		if got, err := resolveOpenDesignCLI(engine, rt); err != nil || got != "unchanged-other-engine" {
			t.Fatalf("other engine changed: %q, %v", got, err)
		}
	}
	want := errors.New("not installed")
	rt := clientLaunchRuntime{resolve: func(string, string) (string, error) { return "", want }}
	if _, err := resolveOpenDesignCLI("codex-cli", rt); !errors.Is(err, want) {
		t.Fatalf("discovery error changed: %v", err)
	}
}
