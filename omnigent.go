package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const omnigentProfileEndpoint = "/api/clients/omnigent"
const omnigentProfileRevision = 1

type omnigentManagedPaths struct{ Root, Selection, Config, Inference string }

func omnigentPaths(dir string) omnigentManagedPaths {
	root := filepath.Join(dir, "omnigent")
	return omnigentManagedPaths{root, filepath.Join(root, "selection.json"), filepath.Join(root, "config", "config.yaml"), filepath.Join(root, "inference.json")}
}

type omnigentPrepared struct {
	Revision      int                 `json:"revision"`
	Preferences   omnigentPreferences `json:"preferences"`
	Library       modelLibrary        `json:"library"`
	CodexLibrary  modelLibrary        `json:"codexLibrary"`
	ClaudeLibrary modelLibrary        `json:"claudeLibrary"`
	Runtime       omnigentRuntime     `json:"runtime"`
	Fingerprint   string              `json:"fingerprint"`
	Files         map[string]string   `json:"files"`
}

func (a *app) omnigentOptions(library modelLibrary, rt clientLaunchRuntime) (omnigentRuntimeOptions, error) {
	paths := omnigentPaths(a.dir)
	p := normalizeOmnigentPreferences(a.config.Omnigent)
	o := omnigentRuntimeOptions{RootDir: paths.Root, Platform: rt.platform, NormalHome: rt.home, Preferences: p, ConfigPath: paths.Config, InferencePath: paths.Inference, Port: 0, NormalEnvironment: t3CodeNormalEnvironment(rt.home, os.Environ(), rt.platform)}
	if p.Claude && p.ClaudeConnection == "native" {
		if selected := os.Getenv("CLAUDE_SECURESTORAGE_CONFIG_DIR"); selected != "" {
			o.NormalEnvironment["CLAUDE_SECURESTORAGE_CONFIG_DIR"] = selected
		}
	}
	if !p.Codex && !p.Claude {
		return o, errors.New("Enable Codex or Claude before preparing Omnigent.")
	}
	var err error
	o.OmniPath, err = rt.resolve("omnigent", "")
	if err != nil {
		return o, err
	}
	if p.Codex {
		o.CodexPath, o.CodexSelectionFingerprint, err = resolveOmnigentCodexSelection(rt)
		if err != nil {
			return o, err
		}
	}
	if p.Claude {
		o.ClaudePath, err = rt.resolve("claude", "")
		if err != nil {
			return o, err
		}
	}
	o.Catalog = readNativeCatalogCache(a.dir, a.catalogScopeLocked())
	policy, err := deriveOmnigentModelPolicy(p, library, o.Catalog)
	if err != nil {
		return o, err
	}
	o.CodexLibrary, o.ClaudeLibrary = policy.CodexLibrary, policy.ClaudeLibrary
	if p.Codex && p.CodexConnection == "kilo" {
		o.CodexToken = omnigentProxyToken(a.config.LocalKey, "codex")
	}
	if p.Claude && p.ClaudeConnection == "kilo" {
		o.ClaudeToken = omnigentProxyToken(a.config.LocalKey, "claude")
	}
	return o, nil
}

func omnigentConfiguration(o omnigentRuntimeOptions, proxyPort int) ([]byte, []byte, error) {
	providers, bindings := map[string]any{}, map[string]any{}
	harness := map[string]any{"default": "codex-native"}
	if !o.Preferences.Codex {
		harness["default"] = "claude-native"
	}
	base := "http://127.0.0.1:" + strconv.Itoa(proxyPort) + "/omnigent/"
	for _, agent := range []struct {
		id, path, connection, family string
		enabled                      bool
		library                      modelLibrary
	}{
		{"codex", o.CodexPath, o.Preferences.CodexConnection, "openai", o.Preferences.Codex, o.CodexLibrary},
		{"claude", o.ClaudePath, o.Preferences.ClaudeConnection, "anthropic", o.Preferences.Claude, o.ClaudeLibrary},
	} {
		if !agent.enabled {
			continue
		}
		aliases := []string{"codex", "codex-native"}
		if agent.id == "claude" {
			aliases = []string{"claude-sdk", "claude-native"}
		}
		for _, alias := range aliases {
			harness[alias] = map[string]any{"command": agent.path}
		}
		if agent.connection != "kilo" {
			continue
		}
		ids := []string{}
		for _, item := range agent.library.Models {
			ids = append(ids, item.ID)
		}
		url := base + agent.id
		if agent.id == "codex" {
			url += "/v1"
		}
		family := map[string]any{"base_url": url, "api_key_ref": "env:KILO_OMNIGENT_" + strings.ToUpper(agent.id) + "_TOKEN", "default_model": "default", "models": map[string]string{"default": agent.library.DefaultModel}}
		if agent.id == "codex" {
			family["wire_api"] = "responses"
		}
		name := "kilo-" + agent.id
		providers[name] = map[string]any{"kind": "gateway", "default": []string{agent.family}, agent.family: family}
		for _, alias := range aliases {
			bindings[alias] = map[string]any{"provider": name, "default_model": agent.library.DefaultModel, "model_allowlist": ids}
		}
	}
	config := map[string]any{"harness": harness, "providers": providers, "inference": map[string]any{"harnesses": bindings}}
	// JSON is a YAML subset. No hand-built strings, interpolation, credentials
	// or user projects are copied into the private configuration.
	configJSON, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	inference, err := json.MarshalIndent(map[string]any{"providers": providers, "inference": config["inference"]}, "", "  ")
	return append(configJSON, '\n'), append(inference, '\n'), err
}

func (a *app) readOmnigentPrepared() (omnigentPrepared, error) {
	var saved omnigentPrepared
	paths := omnigentPaths(a.dir)
	fail := errors.New("Prepare Omnigent again in Kilo Proxy.")
	if !safeLaunchDir(paths.Root, a.dir) {
		return saved, fail
	}
	data, err := readOmnigentPrivateFile(paths.Selection, 2<<20)
	if err != nil || museManagedDecode(data, &saved) != nil || saved.Revision != omnigentProfileRevision || saved.Fingerprint == "" || validateOmnigentPreferences(saved.Preferences) != nil || validateModelLibrary(saved.Library) != nil || validateModelLibrary(saved.CodexLibrary) != nil || validateModelLibrary(saved.ClaudeLibrary) != nil || len(saved.Files) < 5 || len(saved.Files) > 8192 {
		return omnigentPrepared{}, fail
	}
	for relative, digest := range saved.Files {
		if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return omnigentPrepared{}, fail
		}
		path := filepath.Join(paths.Root, relative)
		if !safeLaunchDir(filepath.Dir(path), a.dir) {
			return omnigentPrepared{}, fail
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return omnigentPrepared{}, fail
		}
		actual, err := openDesignFileHash(path, 32<<20)
		if err != nil || actual != digest {
			return omnigentPrepared{}, fail
		}
	}
	return saved, nil
}

func (a *app) omnigentReady(saved omnigentPrepared, rt clientLaunchRuntime) bool {
	p := normalizeOmnigentPreferences(a.config.Omnigent)
	if p != saved.Preferences {
		return false
	}
	if omnigentUsesKilo(p) {
		state := a.modelLibrary.snapshot()
		if state.Warning != "" || state.RecoveryRequired || !reflect.DeepEqual(state.Library, saved.Library) || !a.connectionReadyLocked() {
			return false
		}
	}
	o, err := a.omnigentOptions(saved.Library, rt)
	if err != nil {
		return false
	}
	if !reflect.DeepEqual(o.CodexLibrary, saved.CodexLibrary) || !reflect.DeepEqual(o.ClaudeLibrary, saved.ClaudeLibrary) {
		return false
	}
	config, inference, err := omnigentConfiguration(o, a.config.Port)
	if err != nil {
		return false
	}
	if saved.Files[filepath.Join("config", "config.yaml")] != openDesignHash(config) || saved.Files["inference.json"] != openDesignHash(inference) {
		return false
	}
	identity, err := omnigentRuntimeIdentity(o)
	if err != nil || identity != saved.Fingerprint || identity != saved.Runtime.Fingerprint || !validateOmnigentRuntime(o, saved.Runtime) {
		return false
	}
	expectedFiles := map[string]string{filepath.Join("config", "config.yaml"): openDesignHash(config), "inference.json": openDesignHash(inference)}
	for path, digest := range saved.Runtime.Files {
		expectedFiles[filepath.FromSlash(path)] = digest
	}
	return reflect.DeepEqual(saved.Files, expectedFiles)
}

func (a *app) prepareOmnigent(library modelLibrary, rt clientLaunchRuntime) error {
	o, err := a.omnigentOptions(library, rt)
	if err != nil {
		return err
	}
	if _, err := inspectOmnigentInstallation(o.OmniPath, rt.platform); err != nil {
		return err
	}
	if omnigentUsesKilo(o.Preferences) && !a.connectionReadyLocked() {
		return errors.New("Connect Kilo Proxy before preparing Omnigent with Kilo models.")
	}
	paths := omnigentPaths(a.dir)
	for _, dir := range []string{paths.Root, filepath.Dir(paths.Config)} {
		if err := safeEditorDir(a.dir, dir); err != nil {
			return err
		}
	}
	config, inference, err := omnigentConfiguration(o, a.config.Port)
	if err != nil {
		return err
	}
	// Write the manifest last. Any interrupted preparation fails closed.
	if err = atomicCatalogFile(paths.Config, config); err != nil {
		return err
	}
	if err = atomicCatalogFile(paths.Inference, inference); err != nil {
		return err
	}
	runtime, err := prepareOmnigentRuntime(o)
	if err != nil {
		return err
	}
	saved := omnigentPrepared{Revision: omnigentProfileRevision, Preferences: o.Preferences, Library: cloneModelLibrary(library), CodexLibrary: o.CodexLibrary, ClaudeLibrary: o.ClaudeLibrary, Runtime: runtime, Fingerprint: runtime.Fingerprint, Files: map[string]string{filepath.Join("config", "config.yaml"): openDesignHash(config), "inference.json": openDesignHash(inference)}}
	for path, digest := range runtime.Files {
		saved.Files[path] = digest
	}
	if !a.omnigentReady(saved, rt) {
		return errors.New("Omnigent or its selected models changed while preparing. Refresh and prepare again.")
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	return atomicCatalogFile(paths.Selection, append(data, '\n'))
}

func (a *app) omnigentProfileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	state := a.modelLibrary.snapshot()
	var input struct {
		Library     *modelLibrary   `json:"library,omitempty"`
		Preferences json.RawMessage `json:"preferences,omitempty"`
	}
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &input) {
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another agent is being prepared. Try again.")
			return
		}
		defer a.launchMu.Unlock()
		if err := a.omnigentIdle(); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := normalizeOmnigentPreferences(a.config.Omnigent)
	library := state.Library
	if !omnigentUsesKilo(p) {
		library = emptyModelLibrary()
	}
	if r.Method == http.MethodPost {
		if len(input.Preferences) > 0 {
			expected, err := decodeOmnigentPreferences(input.Preferences)
			if err != nil || expected != p {
				jsonError(w, 409, "Omnigent settings changed. Reload before preparing.")
				return
			}
		}
		if omnigentUsesKilo(p) && (state.Warning != "" || state.RecoveryRequired) {
			jsonError(w, 409, "Recover and save the shared model library first.")
			return
		}
		if input.Library != nil && omnigentUsesKilo(p) && !omnigentSameModelSelection(*input.Library, library) {
			jsonError(w, 409, "The model library changed. Reload the saved models before preparing Omnigent.")
			return
		}
		if err := a.prepareOmnigent(library, a.launchRuntime()); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
	}
	saved, err := a.readOmnigentPrepared()
	ready := err == nil && a.omnigentReady(saved, a.launchRuntime())
	running, runErr := omnigentRunning(omnigentPaths(a.dir).Root)
	message := "Prepare Omnigent with Codex and Claude in its separate Kilo workspace."
	policy, policyErr := deriveOmnigentModelPolicy(p, library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()))
	if policyErr != nil {
		message = policyErr.Error()
	}
	if ready {
		message = "Omnigent is prepared with your selected connections and models."
	}
	if runErr != nil {
		message = runErr.Error()
	}
	if !running && err == nil {
		if data, statusErr := readOmnigentPrivateFile(filepath.Join(omnigentPaths(a.dir).Root, "runtime", "status.json"), 16<<10); statusErr == nil {
			var status struct {
				Version     int    `json:"version"`
				Fingerprint string `json:"fingerprint"`
				Phase       string `json:"phase"`
				Message     string `json:"message"`
			}
			if json.Unmarshal(data, &status) == nil && status.Version == 1 && status.Fingerprint == saved.Fingerprint && status.Phase == "failed" {
				message = "The private Omnigent workspace reported a problem. Check its status before opening it again."
			}
		}
	}
	ver, fingerprint := "", ""
	if ready {
		ver = saved.Runtime.Version
		fingerprint = saved.Fingerprint
	}
	jsonResponse(w, 200, map[string]any{"prepared": ready, "running": running, "preferences": p, "library": library, "version": ver, "fingerprint": fingerprint, "effectiveCodexModels": omnigentModelSummaries(policy.CodexLibrary, readNativeCatalogCache(a.dir, a.catalogScopeLocked())), "effectiveClaudeModels": omnigentModelSummaries(policy.ClaudeLibrary, readNativeCatalogCache(a.dir, a.catalogScopeLocked())), "message": message})
}

func (a *app) applyOmnigentLaunch(plan *clientLaunchPlan, rt clientLaunchRuntime) error {
	saved, err := a.readOmnigentPrepared()
	if err != nil || !a.omnigentReady(saved, rt) {
		return errors.New("Prepare Omnigent again before opening its private workspace.")
	}
	plan.Executable = saved.Runtime.PythonPath
	plan.Args = append([]string{}, saved.Runtime.Args...)
	plan.Env = map[string]string{}
	for key, value := range saved.Runtime.Env {
		plan.Env[key] = value
	}
	// Native credentials are sampled only for this explicit launch. They do
	// not enter the prepared manifest, descriptor, API response or sibling CLI.
	for _, agent := range []struct {
		name       string
		enabled    bool
		connection string
	}{
		{"codex", saved.Preferences.Codex, saved.Preferences.CodexConnection},
		{"claude", saved.Preferences.Claude, saved.Preferences.ClaudeConnection},
	} {
		if agent.enabled && agent.connection == "native" {
			encoded, _ := json.Marshal(omnigentNativeCredentialEnvironment(agent.name, os.Environ()))
			plan.Env["KILO_OMNIGENT_NATIVE_"+strings.ToUpper(agent.name)+"_ENV"] = string(encoded)
		}
	}
	plan.Unset = omnigentInheritedEnvironment(os.Environ())
	plan.Directory = omnigentPaths(a.dir).Root
	plan.skipProxy = !omnigentUsesKilo(saved.Preferences)
	return nil
}

func omnigentInheritedEnvironment(environment []string) []string {
	unset := []string{"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP", "PYTHONINSPECT", "NODE_OPTIONS", "NODE_PATH", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "KILO_LOCAL_API_KEY", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN"}
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "OMNIGENT_") || strings.HasPrefix(name, "HARNESS_") || strings.HasPrefix(name, "KILO_OMNIGENT_") || strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_") {
			if !helperContains(unset, name) {
				unset = append(unset, name)
			}
		}
	}
	return unset
}

func omnigentNativeCredentialEnvironment(agent string, parent []string) map[string]string {
	result := map[string]string{}
	names := []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_ORGANIZATION"}
	if agent == "claude" {
		names = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_SECURESTORAGE_CONFIG_DIR"}
	}
	for _, entry := range parent {
		name, value, _ := strings.Cut(entry, "=")
		if helperContains(names, name) {
			result[name] = value
		}
	}
	return result
}

// runtime.json is produced by the private supervisor, never by the API caller.
type omnigentRuntimeRecord struct {
	Version      int    `json:"version"`
	PID          int    `json:"pid"`
	StartedAt    string `json:"startedAt"`
	ServerPort   int    `json:"serverPort"`
	ControlPort  int    `json:"controlPort"`
	ControlToken string `json:"controlToken"`
	Fingerprint  string `json:"fingerprint"`
}

func readOmnigentPrivateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Omnigent runtime files must be private regular files.")
	}
	return readOpenDesignShimFile(path, limit)
}

func readOmnigentRuntime(root string) (omnigentRuntimeRecord, error) {
	var record omnigentRuntimeRecord
	if !safeLaunchDir(root, filepath.Dir(root)) {
		return record, errors.New("Unsafe private Omnigent runtime directory.")
	}
	data, err := readOmnigentPrivateFile(filepath.Join(root, "runtime.json"), 16<<10)
	if err != nil {
		return record, err
	}
	if json.Unmarshal(data, &record) != nil || record.Version != 1 || record.PID < 1 || record.PID > 1<<31-1 || record.ServerPort < 1024 || record.ServerPort > 65535 || record.ControlPort < 1024 || record.ControlPort > 65535 || len(record.ControlToken) < 32 || len(record.ControlToken) > 256 || record.Fingerprint == "" {
		return record, errors.New("Cannot verify the private Omnigent runtime.")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.StartedAt); err != nil {
		return record, errors.New("Cannot verify the private Omnigent runtime.")
	}
	return record, nil
}
func omnigentRunning(root string) (bool, error) {
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	record, err := readOmnigentRuntime(root)
	if errors.Is(err, os.ErrNotExist) {
		lock := filepath.Join(root, "runtime.lock")
		if _, err := os.Lstat(lock); errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		file, err := openProfileLock(lock)
		if err != nil {
			return false, err
		}
		defer file.Close()
		if err := lockProfileFile(file); errors.Is(err, errProfileLocked) {
			return true, nil
		} else if err != nil {
			return false, err
		}
		unlockProfileFile(file)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return openMausBotProcessAlive(record.PID)
}
func (a *app) omnigentIdle() error {
	a.mu.Lock()
	pending := time.Now().Before(a.omnigentLaunchUntil)
	a.mu.Unlock()
	active, err := omnigentRunning(omnigentPaths(a.dir).Root)
	if err != nil {
		return err
	}
	if active || pending {
		return errors.New("Stop Omnigent · Kilo before changing its connections or prepared models.")
	}
	return nil
}
func (a *app) omnigentStopAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var body struct{}
	if !decodeBody(w, r, &body) {
		return
	}
	if !a.launchMu.TryLock() {
		jsonError(w, 409, "Another launch is already being prepared.")
		return
	}
	defer a.launchMu.Unlock()
	record, err := readOmnigentRuntime(omnigentPaths(a.dir).Root)
	if errors.Is(err, os.ErrNotExist) {
		if err := a.omnigentIdle(); err != nil {
			jsonError(w, 409, "Could not stop the private Omnigent workspace. Refresh its status and try again.")
			return
		}
		if err := omnigentStoppedStatus(omnigentPaths(a.dir).Root, ""); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(record.ControlPort)+"/stop", bytes.NewReader([]byte("{}")))
	request.Header.Set("Authorization", "Bearer "+record.ControlToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		jsonError(w, 409, "Could not stop the private Omnigent workspace. Refresh its status and try again.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		jsonError(w, 409, "The private Omnigent supervisor rejected the stop request.")
		return
	}
	// The control response acknowledges shutdown; the supervisor removes its
	// record and releases its lock only after its own children have exited.
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		active, err := omnigentRunning(omnigentPaths(a.dir).Root)
		if err != nil {
			jsonError(w, 409, "Could not stop the private Omnigent workspace. Refresh its status and try again.")
			return
		}
		if !active {
			break
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			jsonError(w, 409, "Could not stop the private Omnigent workspace. Refresh its status and try again.")
			return
		case <-tick.C:
		}
	}
	a.mu.Lock()
	a.omnigentLaunchUntil = time.Time{}
	a.mu.Unlock()
	if err := omnigentStoppedStatus(omnigentPaths(a.dir).Root, record.Fingerprint); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func omnigentStoppedStatus(root, fingerprint string) error {
	data, err := readOmnigentPrivateFile(filepath.Join(root, "runtime", "status.json"), 16<<10)
	if errors.Is(err, os.ErrNotExist) && fingerprint == "" {
		return nil // An unused workspace has no shutdown receipt yet.
	}
	var status struct {
		Version     int    `json:"version"`
		Fingerprint string `json:"fingerprint"`
		Phase       string `json:"phase"`
	}
	if err != nil || json.Unmarshal(data, &status) != nil || status.Version != 1 || status.Fingerprint == "" || (fingerprint != "" && status.Fingerprint != fingerprint) || status.Phase != "stopped" {
		return errors.New("The private Omnigent workspace did not finish stopping. Review its status before opening it again.")
	}
	return nil
}
