package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Opt-in acceptance fixture for disposable Cua guests. It uses the production
// admin UI, profile preparation, launch plan, scoped proxy and Omnigent runtime.
// Only upstream inference and the credential vault are synthetic. It must never
// discover or execute a personal CLI during normal test runs.
func TestOmnigentInstalledPrivateWorkspace(t *testing.T) {
	if os.Getenv("KILO_OMNIGENT_CUA_GUEST") != "1" {
		t.Skip("requires an explicitly provisioned disposable Cua guest")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Omnigent private runtime requires macOS or Linux")
	}
	manifest, stop := os.Getenv("KILO_OMNIGENT_ACCEPTANCE_MANIFEST"), os.Getenv("KILO_OMNIGENT_ACCEPTANCE_STOP")
	if !filepath.IsAbs(manifest) || !filepath.IsAbs(stop) {
		t.Fatal("absolute owned manifest and stop paths required")
	}
	gateway, err := url.Parse(os.Getenv("KILO_OMNIGENT_FIXTURE_GATEWAY"))
	if err != nil || gateway.Scheme != "http" || gateway.Hostname() != "127.0.0.1" || gateway.User != nil || gateway.RawQuery != "" || gateway.Fragment != "" {
		t.Fatal("this fixture accepts only a guest-local synthetic gateway")
	}
	root := t.TempDir()
	home := filepath.Join(root, "normal-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(filepath.Join(root, "kilo"), &fakeVault{values: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.stop(); a.cancelLogin(); a.quitOnce.Do(func() { close(a.quit) }) })
	a.apiKey = "synthetic-kilo-omnigent-upstream"
	a.config.OrgID = "synthetic-omnigent-acceptance"
	a.config.Language = "en"
	a.config.Omnigent = normalizeOmnigentPreferences(omnigentPreferences{})
	a.upstream = gateway
	transport := &http.Transport{Proxy: nil}
	a.transport = usageMemoryTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "http" || request.URL.Hostname() != "127.0.0.1" {
			return nil, errors.New("the isolated acceptance fixture forbids external network destinations")
		}
		return transport.RoundTrip(request)
	})
	a.accountURL = gateway.String()
	a.modelStatsURL = gateway.String() + "/fixture-model-stats"
	a.recommendedModelsURL = gateway.String() + "/fixture-recommended-models"
	a.billingAutoRefresh = false
	a.config.Port, a.listenProxy = headlessTestProxyReservation(t)
	library := modelLibrary{SchemaVersion: 1, DefaultModel: "openai/kilo-omnigent-fixture", Models: []modelLibraryItem{
		{ID: "openai/kilo-omnigent-fixture", DisplayName: "Fixture · Default", ReasoningCustom: true, ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "low"},
		{ID: "anthropic/claude-sonnet-5.5", DisplayName: "Fixture · Claude", ReasoningCustom: true, ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "high"},
		{ID: "vendor/nested/plain", DisplayName: "Fixture · No reasoning", ReasoningCustom: true, ReasoningLevels: []string{}},
	}}
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	catalog := []modelInfo{}
	for _, item := range library.Models {
		tools := true
		reasoning := len(item.ReasoningLevels) > 0
		catalog = append(catalog, modelInfo{ID: item.ID, Name: item.DisplayName, Tools: &tools, Reasoning: &reasoning, ReasoningEfforts: item.ReasoningLevels, ContextWindow: 128000, MaxOutputTokens: 8192})
	}
	cache, _ := json.Marshal(nativeCachedCatalog{SchemaVersion: 1, OrgID: a.catalogScopeLocked(), Models: catalog, FetchedAt: time.Now().UTC()})
	if err := atomicCatalogFile(filepath.Join(a.dir, "model-catalog.json"), cache); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{"omnigent": os.Getenv("KILO_OMNIGENT_OFFICIAL_CLI"), "codex-cli": os.Getenv("KILO_OMNIGENT_CODEX_CLI"), "claude": os.Getenv("KILO_OMNIGENT_CLAUDE_CLI")}
	for id, path := range paths {
		if !filepath.IsAbs(path) || !launchExecutable(path) {
			t.Fatalf("explicit installed guest CLI required: %s", id)
		}
	}
	a.config.OmnigentExecutable, a.config.CodexCLIExecutable, a.config.ClaudeCodeExecutable = paths["omnigent"], paths["codex-cli"], paths["claude"]
	a.omnigentExecutable = paths["omnigent"]
	a.cliExecutables = [2]string{paths["codex-cli"], paths["claude"]}
	if err := writeSettings(a.dir, a.config); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	commands := []*exec.Cmd{}
	var logs []*os.File
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "macos"
	}
	a.launcher = &clientLaunchRuntime{platform: platform, home: home, resolve: func(id, custom string) (string, error) {
		if custom != "" {
			return "", errors.New("no arbitrary fixture executable")
		}
		if path := paths[id]; path != "" {
			return path, nil
		}
		return "", errors.New("this isolated fixture enables only Omnigent, Codex and Claude")
	}, start: func(plan clientLaunchPlan) error {
		if plan.Client != "omnigent" {
			return errors.New("this fixture opens only Omnigent")
		}
		command, cleanup, err := clientProcessCommand(plan, clientChildEnvironment(os.Environ(), plan.Env, plan.Unset, runtime.GOOS))
		if err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(root, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			cleanup()
			return err
		}
		command.Dir = plan.Directory
		command.Stdout, command.Stderr = log, log
		if err := command.Start(); err != nil {
			_ = log.Close()
			cleanup()
			return err
		}
		mu.Lock()
		commands = append(commands, command)
		logs = append(logs, log)
		mu.Unlock()
		go func() { _ = command.Wait(); cleanup() }()
		return nil
	}}
	t.Cleanup(func() {
		// Stop only subprocess handles created by this fixture. The supervisor
		// terminates its own server/host children in its finally block.
		mu.Lock()
		defer mu.Unlock()
		for _, command := range commands {
			if command.Process != nil {
				_ = command.Process.Signal(os.Interrupt)
			}
		}
		for _, log := range logs {
			_ = log.Close()
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.adminHost = listener.Addr().String()
	admin := &http.Server{Handler: a.adminHandler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = admin.Serve(listener) }()
	t.Cleanup(func() { _ = admin.Close() })
	info := map[string]any{"version": 1, "syntheticFixture": true, "productionProfiles": true, "root": root, "configDir": a.dir, "omnigentRoot": omnigentPaths(a.dir).Root, "adminURL": "http://" + a.adminHost + "/#" + a.adminToken, "adminBaseURL": "http://" + a.adminHost, "adminToken": a.adminToken, "proxyBaseURL": "http://127.0.0.1:" + strconv.Itoa(a.config.Port), "gatewayBaseURL": gateway.String(), "models": library, "selectedExecutables": paths, "stopFile": stop, "runtimeRecord": filepath.Join(omnigentPaths(a.dir).Root, "runtime.json"), "supervisorLog": filepath.Join(root, "supervisor.log")}
	data, _ := json.MarshalIndent(info, "", "  ")
	if err := atomicCatalogFile(manifest, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := omnigentAcceptanceNativeReady(a); err != nil {
		t.Fatal(err)
	}
	t.Log("Production Kilo UI and Omnigent acceptance fixture ready; owned manifest:", manifest)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("Cua acceptance fixture deadline reached")
		case <-ticker.C:
			if _, err := os.Lstat(stop); err == nil {
				return
			}
		}
	}
}
