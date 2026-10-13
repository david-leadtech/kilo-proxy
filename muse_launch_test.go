package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMuseExecutablePreferencesPersistAndNeverFallback(t *testing.T) {
	a := launchTestApp(t)
	a.config.Port = 8877
	path := cliPreferencesTestExecutable(t, "custom Muse with spaces")
	postCLISelection(t, a, map[string]any{"version": 1, "museExecutable": path}, 200)
	postCLISelection(t, a, map[string]any{"version": 1, "claudeExecutable": ""}, 200)
	reloaded, err := newApp(a.dir, a.vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reloaded.stop)
	if got, err := reloaded.launchRuntime().resolve("muse", ""); err != nil || got != path {
		t.Fatal("lost selected Muse", got, err)
	}
	if got, err := resolveTerminalExecutable(a.dir, "muse"); err != nil || got != path {
		t.Fatal("terminal ignored selected Muse", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveTerminalExecutable(a.dir, "muse"); err == nil || got != "" {
		t.Fatal("missing custom Muse fell back", got, err)
	}
	if _, err := readSettings(a.dir); err != nil {
		t.Fatal("missing Muse prevented Settings recovery", err)
	}
	postCLISelection(t, a, map[string]any{"version": 1, "museExecutable": ""}, 200)
	if a.cliExecutable("muse") != "" {
		t.Fatal("automatic reset lost")
	}
	for _, path := range []string{"relative/muse", "/tmp/bad\npath", t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		postCLISelection(t, a, map[string]any{"version": 1, "museExecutable": path}, 400)
	}
}

func TestMuseNativeLaunchNeedsNeitherGatewayNorSharedModels(t *testing.T) {
	a := launchTestApp(t)
	a.apiKey, a.config.OrgID = "", ""
	a.modelLibrary.mu.Lock()
	a.modelLibrary.state.RecoveryRequired = true
	a.modelLibrary.mu.Unlock()
	path := filepath.Join(a.launcher.home, "synthetic", "client")
	var launched clientLaunchPlan
	a.launcher.start = func(plan clientLaunchPlan) error { launched = plan; return nil }
	request, _ := json.Marshal(clientLaunchRequest{Client: "muse", Directory: a.launcher.home, MuseExecutable: path})
	w := adminRequest(a, "clients/launch", string(request))
	if w.Code != 200 || launched.Client != "muse" || launched.Executable != path || a.proxyListener != nil {
		t.Fatal("native Muse required gateway/profile", w.Code, w.Body.String(), launched)
	}
	if !reflect.DeepEqual(launched.Env, map[string]string{"MUSE_NO_AUTO_UPDATE": "1"}) || !reflect.DeepEqual(launched.Unset, []string{"KILO_LOCAL_API_KEY"}) || len(launched.Args) != 0 {
		t.Fatal("Muse received a gateway override", launched)
	}
	for _, expected := range []string{"", "relative/muse", path + "-stale"} {
		request, _ := json.Marshal(clientLaunchRequest{Client: "muse", Directory: a.launcher.home, MuseExecutable: expected})
		if w := adminRequest(a, "clients/launch", string(request)); w.Code != 409 {
			t.Fatal("unconfirmed Muse path accepted", w.Code)
		}
	}
	request, _ = json.Marshal(terminalPrepareRequest{Client: "muse", Directory: a.launcher.home})
	w = adminRequest(a, "terminal/prepare", string(request))
	var plan clientLaunchPlan
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &plan) != nil || plan.Executable != "" || validateTerminalProfile(plan, terminalRuntime{}) != nil || a.proxyListener != nil {
		t.Fatal("native terminal preparation required gateway", w.Code, w.Body.String())
	}
	plan.Env["META_API_KEY"] = "not-allowed-from-controller"
	if validateTerminalProfile(plan, terminalRuntime{}) == nil {
		t.Fatal("controller supplied Muse credentials")
	}
}

func TestMuseTerminalRunsWithKiloClosedAndPreservesNativeArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process fixture; Windows uses the production PowerShell gate")
	}
	profile, project, home := t.TempDir(), t.TempDir(), t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom Muse ' executable")
	script := "#!/bin/sh\nexec " + helperShellQuote(self) + " -test.run='^TestTerminalClientProcessHelper$' -- \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config, err := readSettings(profile)
	if err != nil {
		t.Fatal(err)
	}
	config.MuseCodeExecutable = path
	if err := writeSettings(profile, config); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"exec", "--model", "muse-spark-1.3", "--reasoning-effort", "xhigh", "literal$(should-not-run)", "spaces ; and ' quotes"}
	args := append([]string{"-test.run=^TestTerminalAgentProcessHelper$", "--", "muse", "--config-dir", profile, "--"}, arguments...)
	binary := self
	if built := os.Getenv("KILO_TEST_TERMINAL_BINARY"); built != "" {
		binary = built
		args = append([]string{"--terminal-agent", "muse", "--config-dir", profile, "--"}, arguments...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = project
	auth := filepath.Join(home, "synthetic-muse-auth.json")
	command.Env = clientChildEnvironment(os.Environ(), map[string]string{"KILO_TERMINAL_PROCESS_TEST": "1", "HOME": home, "META_API_KEY": "synthetic-meta-key", "MUSE_AUTH_PATH": auth, "KILO_LOCAL_API_KEY": "must-not-leak", "MUSE_NO_AUTO_UPDATE": "0"}, nil, runtime.GOOS)
	command.Stdin = strings.NewReader("stdin preserved\n")
	var out, stderr bytes.Buffer
	command.Stdout, command.Stderr = &out, &stderr
	var exit *exec.ExitError
	if err := command.Run(); !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatal("native process failed", err, out.String(), stderr.String())
	}
	var got struct {
		Args                                                          []string
		Input, Cwd, LocalKey, MetaKey, MuseAuthPath, MuseNoAutoUpdate string
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err, out.String())
	}
	canonical, _ := filepath.EvalSymlinks(project)
	actual, _ := filepath.EvalSymlinks(got.Cwd)
	if !reflect.DeepEqual(got.Args, arguments) || got.Input != "stdin preserved\n" || canonical != actual || got.LocalKey != "" || got.MetaKey != "synthetic-meta-key" || got.MuseAuthPath != auth || got.MuseNoAutoUpdate != "1" {
		t.Fatal("lost native Muse behavior", got)
	}
	if _, err := os.Stat(filepath.Join(profile, terminalRuntimeFile)); !os.IsNotExist(err) {
		t.Fatal("Muse required/published a controller runtime", err)
	}
}
