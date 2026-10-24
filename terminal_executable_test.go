package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTerminalPrepareOpenCodeKeepsExecutableDiscoveryInInvokingTerminal(t *testing.T) {
	a := terminalTestApp(t)
	a.openCodeVersionProbe = nil
	body, _ := json.Marshal(terminalPrepareRequest{Client: "opencode", Directory: a.launcher.home})
	w := adminRequest(a, "terminal/prepare", string(body))
	var plan clientLaunchPlan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.Executable != "" || plan.Client != "opencode" || len(plan.Args) != 0 {
		t.Fatal("profile-only preparation tried to probe an absent executable", w.Code, w.Body.String())
	}
}

func TestTerminalExecutableUsesSavedCLIAndOpenCodePathsWithoutFallback(t *testing.T) {
	profile := t.TempDir()
	config, err := readSettings(profile)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"codex-cli": cliPreferencesTestExecutable(t, "private-codex"), "claude": cliPreferencesTestExecutable(t, "private-claude")}
	config.CodexCLIExecutable, config.ClaudeCodeExecutable = paths["codex-cli"], paths["claude"]
	if err := writeSettings(profile, config); err != nil {
		t.Fatal(err)
	}
	for client, path := range paths {
		if got, err := resolveTerminalExecutable(profile, client); err != nil || got != path {
			t.Fatal("terminal ignored saved CLI selection", client, got, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if got, err := resolveTerminalExecutable(profile, client); err == nil || got != "" {
			t.Fatal("missing explicit CLI fell back to another installation", client, got, err)
		}
	}
}

func TestTerminalExecutableChecksSavedOpenCode2Major(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix synthetic CLI; Windows production console test supplies native clients")
	}
	profile := t.TempDir()
	config, err := readSettings(profile)
	if err != nil {
		t.Fatal(err)
	}
	makeCLI := func(path, version string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = --version ] || exit 97\nprintf '%s\\n' "+helperShellQuote(version)+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	selected := filepath.Join(t.TempDir(), "selected-opencode2")
	config.OpenCodeMajor = 2
	config.OpenCode2Executable = selected
	if err := writeSettings(profile, config); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"2.0.27", "1.19.0", "2.0.17", "unrecognized"} {
		makeCLI(selected, version)
		got, err := resolveTerminalExecutable(profile, "opencode")
		if version == "2.0.27" {
			if err != nil || got != selected {
				t.Fatal("valid selected OpenCode 2 was rejected", got, err)
			}
		} else if err == nil || got != "" || !strings.Contains(err.Error(), "OpenCode 2.0.18") {
			t.Fatal("wrong-major or unknown executable was accepted", version, got, err)
		}
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveTerminalExecutable(profile, "opencode"); err == nil || got != "" {
		t.Fatal("missing selected OpenCode 2 fell back", got, err)
	}
}

func TestTerminalPrepareOpenCode2UsesSavedMajor(t *testing.T) {
	a := terminalTestApp(t)
	config := a.config
	config.OpenCodeMajor = 2
	if err := writeSettings(a.dir, config); err != nil {
		t.Fatal(err)
	}
	a.config.OpenCodeMajor = 2
	body, _ := json.Marshal(terminalPrepareRequest{Client: "opencode2", Directory: a.launcher.home})
	w := adminRequest(a, "terminal/prepare", string(body))
	var plan clientLaunchPlan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plan) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	home, _, _ := a.openCode2Paths()
	if plan.Client != "opencode2" || plan.Executable != "" || len(plan.Args) != 0 || plan.Env["HOME"] != home || plan.Env["KILO_OPENCODE2_API_KEY"] == "" || len(plan.Unset) == 0 {
		t.Fatal("invalid OpenCode 2 terminal plan", plan)
	}
	mismatch, _ := json.Marshal(terminalPrepareRequest{Client: "opencode", Directory: a.launcher.home})
	if w := adminRequest(a, "terminal/prepare", string(mismatch)); w.Code != 409 || !strings.Contains(w.Body.String(), "The OpenCode version changed in Kilo settings. Try again.") {
		t.Fatal("client that did not match the saved major was accepted", w.Code, w.Body.String())
	}
	config.OpenCodeMajor = 1
	if err := writeSettings(a.dir, config); err != nil {
		t.Fatal(err)
	}
	a.config.OpenCodeMajor = 1
	mismatch, _ = json.Marshal(terminalPrepareRequest{Client: "opencode2", Directory: a.launcher.home})
	if w := adminRequest(a, "terminal/prepare", string(mismatch)); w.Code != 409 || !strings.Contains(w.Body.String(), "The OpenCode version changed in Kilo settings. Try again.") {
		t.Fatal("OpenCode 2 client that did not match the saved major was accepted", w.Code, w.Body.String())
	}
}

func TestTerminalExecutableChecksRealOpenCodeMajorAndManagedIntegrity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix synthetic CLI; Windows production console test supplies native clients")
	}
	profile := t.TempDir()
	config, err := readSettings(profile)
	if err != nil {
		t.Fatal(err)
	}
	makeCLI := func(path, version string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = --version ] || exit 97\nprintf '%s\\n' "+helperShellQuote(version)+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	selected := filepath.Join(t.TempDir(), "selected-opencode")
	config.OpenCodeMajor = 1
	config.OpenCode1Executable = selected
	if err := writeSettings(profile, config); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.19.0", "2.0.27", "unrecognized"} {
		makeCLI(selected, version)
		got, err := resolveTerminalExecutable(profile, "opencode")
		if version == "1.19.0" {
			if err != nil || got != selected {
				t.Fatal("valid selected OpenCode 1 was rejected", got, err)
			}
		} else if err == nil || got != "" || !strings.Contains(err.Error(), "OpenCode 1.x") {
			t.Fatal("wrong-major or unknown executable was accepted", version, got, err)
		}
	}
	if err := os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveTerminalExecutable(profile, "opencode"); err == nil || got != "" {
		t.Fatal("missing selected OpenCode fell back", got, err)
	}
	managed := managedToolRegistryFixture(t, profile, "opencode", "terminal-current")
	makeCLI(managed.Executable, "1.19.0")
	managed.Digest, err = managedToolFingerprint(managed.Executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagedTool(profile, managed); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", managed.Executable} {
		config.OpenCode1Executable = selected
		if err := writeSettings(profile, config); err != nil {
			t.Fatal(err)
		}
		if got, err := resolveTerminalExecutable(profile, "opencode"); err != nil || got != managed.Executable {
			t.Fatal("terminal did not use verified managed current", got, err)
		}
	}
	marker := filepath.Join(t.TempDir(), "unexpected-probe")
	if err := os.WriteFile(managed.Executable, []byte("#!/bin/sh\nprintf changed > "+helperShellQuote(marker)+"\nprintf '1.19.0\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveTerminalExecutable(profile, "opencode"); err == nil || got != "" {
		t.Fatal("explicit managed path bypassed integrity", got, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("modified managed executable ran during the version probe", err)
	}
}
