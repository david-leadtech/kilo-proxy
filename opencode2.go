package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

type openCodePreferences struct {
	Version     int    `json:"version"`
	Major       int    `json:"major"`
	Executable1 string `json:"executable1"`
	Executable2 string `json:"executable2"`
}

func validateOpenCodeExecutable(path string) error {
	if path != "" && (len(path) > 8192 || !packageUpdateAbsolute(path) || strings.ContainsAny(path, "\x00\r\n")) {
		return errors.New("Choose an absolute OpenCode executable path, or leave it empty for automatic detection.")
	}
	return nil
}

// The launcher can be resolved both with and without app.mu held. A separate
// snapshot lock keeps explicit executable choices safe in both paths.
func (a *app) openCodeExecutable(client string) string {
	a.openCodePathsMu.RLock()
	defer a.openCodePathsMu.RUnlock()
	if client == "opencode2" {
		return a.openCodeExecutables[1]
	}
	return a.openCodeExecutables[0]
}

func (a *app) openCodePreferencesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input struct {
		Version     int     `json:"version"`
		Major       int     `json:"major"`
		Executable1 *string `json:"executable1,omitempty"`
		Executable2 *string `json:"executable2,omitempty"`
	}
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &input) {
			return
		}
		if input.Version != 1 || (input.Major != 1 && input.Major != 2) {
			jsonError(w, 400, "Choose OpenCode 1 or OpenCode 2.")
			return
		}
		for _, path := range []*string{input.Executable1, input.Executable2} {
			if path != nil {
				if err := validateOpenCodeExecutable(*path); err != nil {
					jsonError(w, 400, err.Error())
					return
				}
			}
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another launch is already being prepared.")
			return
		}
		defer a.launchMu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		next := a.config
		next.OpenCodeMajor = input.Major
		if input.Executable1 != nil {
			next.OpenCode1Executable = *input.Executable1
		}
		if input.Executable2 != nil {
			next.OpenCode2Executable = *input.Executable2
		}
		if err := writeSettings(a.dir, next); err != nil {
			jsonError(w, 500, "Cannot save OpenCode preferences.")
			return
		}
		a.config = next
		a.openCodePathsMu.Lock()
		a.openCodeExecutables = [2]string{next.OpenCode1Executable, next.OpenCode2Executable}
		a.openCodePathsMu.Unlock()
	}
	major := a.config.OpenCodeMajor
	if major == 0 {
		major = 1
	}
	jsonResponse(w, 200, openCodePreferences{Version: 1, Major: major, Executable1: a.config.OpenCode1Executable, Executable2: a.config.OpenCode2Executable})
}

func (a *app) checkOpenCodeVersion(binary string, major int) error {
	var version string
	var err error
	if a.openCodeVersionProbe != nil {
		version, err = a.openCodeVersionProbe(binary)
	} else {
		version, err = managedToolProbeVersion(context.Background(), binary)
	}
	if major == 1 {
		if err != nil || !strings.HasPrefix(version, "1.") {
			return errors.New("Choose an OpenCode 1.x installation, or select OpenCode 2 in Kilo settings.")
		}
		return nil
	}
	if err != nil || !strings.HasPrefix(version, "2.") || strings.ContainsAny(version, "-+") || managedToolCompareNumericVersion(version, "2.0.18") < 0 {
		return errors.New("Install OpenCode 2.0.18 or newer within major version 2. OpenCode 1 is kept separately.")
	}
	return nil
}

func (a *app) openCode2Paths() (home, config, selection string) {
	home = filepath.Join(a.dir, "opencode2", "home")
	return home, filepath.Join(home, ".config", "opencode", "opencode.json"), filepath.Join(a.dir, "opencode2", "selection.json")
}

func (a *app) openCode2Config(library modelLibrary) ([]byte, error) {
	if err := validateModelLibrary(library); err != nil || len(library.Models) == 0 {
		return nil, errors.New("Select at least one shared model for OpenCode 2.")
	}
	models := map[string]any{}
	for _, choice := range terminalLibraryChoices(library, readNativeCatalogCache(a.dir, a.catalogScopeLocked())) {
		limits, err := contextPolicyForChoice(choice)
		if err != nil {
			return nil, err
		}
		model := map[string]any{"modelID": choice.Model.ID, "name": choice.DisplayName}
		model["package"] = "@opencode/ai/providers/openai-compatible"
		model["settings"] = map[string]any{"baseURL": "http://127.0.0.1:" + strconv.Itoa(a.config.Port) + "/v1", "apiKey": "{env:KILO_OPENCODE2_API_KEY}"}
		model["headers"] = map[string]string{"Authorization": "Bearer {env:KILO_OPENCODE2_API_KEY}"}
		if capabilities := t3CodeOpenCodeCapabilities(choice.Model); capabilities != nil {
			model["capabilities"] = capabilities
		}
		if choice.DisplayName == "" {
			model["name"] = choice.Model.ID
		}
		limit := map[string]int{}
		if limits.ContextWindow > 0 {
			limit["context"] = limits.ContextWindow
		}
		if limits.MaxOutputTokens > 0 {
			limit["output"] = limits.MaxOutputTokens
		}
		if len(limit) > 0 {
			model["limit"] = limit
		}
		body := map[string]any{"model": choice.Model.ID}
		if choice.DefaultReasoning != "" {
			body["reasoning_effort"] = choice.DefaultReasoning
		}
		model["body"] = body
		models[choice.Model.ID] = model
	}
	return json.MarshalIndent(map[string]any{
		"$schema": "https://opencode.ai/config.json", "model": "kilo-local/" + library.DefaultModel, "update": "disable",
		"providers": map[string]any{"kilo-local": map[string]any{
			"name": "Kilo Proxy", "env": []string{"KILO_OPENCODE2_API_KEY"}, "package": "@opencode/ai/providers/openai-compatible",
			"settings": map[string]any{"baseURL": "http://127.0.0.1:" + strconv.Itoa(a.config.Port) + "/v1"}, "models": models,
		}},
	}, "", "  ")
}

// openCode2LibraryError marks a model-library or configuration problem, which
// the profile API reports as a bad request rather than a conflict.
type openCode2LibraryError struct{ err error }

func (e openCode2LibraryError) Error() string { return e.err.Error() }

// Caller holds a.mu. Prepare the isolated OpenCode 2 profile without touching
// OpenCode 1 settings.
func (a *app) prepareOpenCode2Profile(library modelLibrary) error {
	_, config, selection := a.openCode2Paths()
	data, err := a.openCode2Config(library)
	if err != nil {
		return openCode2LibraryError{err}
	}
	if err := safeEditorDir(a.dir, filepath.Dir(config)); err != nil {
		return errors.New("Cannot prepare the private OpenCode 2 directory.")
	}
	old, readErr := readCatalogFile(config)
	if readErr != nil && !os.IsNotExist(readErr) {
		return errors.New("Cannot read the private OpenCode 2 configuration.")
	}
	data, err = mergeOpenCode2Config(old, data)
	if err != nil {
		return err
	}
	file, err := prepareProfileFile(config, data)
	if err != nil {
		return errors.New("Cannot prepare the private OpenCode 2 configuration.")
	}
	saved, _ := json.Marshal(library)
	record, err := prepareProfileFile(selection, saved)
	if err != nil {
		return errors.New("Cannot prepare the private OpenCode 2 selection.")
	}
	if _, err := saveEditorFiles([]profileFile{file, record}); err != nil {
		return errors.New("Cannot save the private OpenCode 2 profile.")
	}
	return nil
}

// A version-only preparation request carries no explicit library. OpenCode 2
// shares the OpenCode pack slot, so use its assigned pack when the saved catalog
// resolves it and fall back to the shared models otherwise.
func (a *app) openCode2ModelLibrary(shared modelLibrary) modelLibrary {
	packs := newModelPacksStore(a.dir)
	if packs.recovery {
		return shared
	}
	id := packs.value.Assignments[packAgentKey("opencode2")]
	if id == "" {
		return shared
	}
	catalog := readNativeCatalogCache(a.dir, a.catalogScopeLocked())
	library, err := modelPackLibrary(packs.value, id, catalog)
	if err != nil || len(library.Models) == 0 {
		return shared
	}
	known := make(map[string]bool, len(catalog))
	for _, model := range catalog {
		known[model.ID] = true
	}
	for _, item := range library.Models {
		if !known[item.ID] {
			return shared
		}
	}
	return library
}

// openCode2UnsetEnvironment removes every inherited OpenCode override so the
// isolated profile is the only configuration OpenCode 2 reads.
func openCode2UnsetEnvironment(parent []string) []string {
	names := t3CodeUnsetEnvironment(parent)
	for _, entry := range parent {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "OPENCODE_") {
			names = append(names, key)
		}
	}
	// The trusted-plan check rejects duplicates, and a caller's own T3CODE_*
	// overrides can already appear in the fixed list.
	seen := make(map[string]bool, len(names))
	unique := names[:0]
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		unique = append(unique, name)
	}
	return unique
}

func (a *app) openCode2ProfileAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input struct {
		Library *modelLibrary `json:"library"`
	}
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &input) {
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another launch is already being prepared.")
			return
		}
		defer a.launchMu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	home, config, selection := a.openCode2Paths()
	state := a.modelLibrary.snapshot()
	library := state.Library
	if input.Library != nil {
		library = *input.Library
	} else if r.Method == http.MethodPost && (state.RecoveryRequired || state.Warning != "") {
		jsonError(w, 409, "Recover and save your shared models first.")
		return
	} else if r.Method == http.MethodPost {
		library = a.openCode2ModelLibrary(library)
	}
	rt := a.launchRuntime()
	binary, resolveErr := rt.resolve("opencode2", "")
	message := "Prepare OpenCode 2 with your OpenCode models and a separate profile."
	if r.Method == http.MethodPost {
		if !a.connectionReadyLocked() {
			jsonError(w, 409, "Connect the selected provider first.")
			return
		}
		if resolveErr != nil {
			jsonError(w, 409, resolveErr.Error())
			return
		}
		if err := a.checkOpenCodeVersion(binary, 2); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
		if err := a.prepareOpenCode2Profile(library); err != nil {
			var libraryErr openCode2LibraryError
			if errors.As(err, &libraryErr) {
				jsonError(w, 400, err.Error())
			} else {
				jsonError(w, 409, err.Error())
			}
			return
		}
		message = "OpenCode 2 prepared. Its accounts and chats stay separate from OpenCode 1."
	}
	ready := false
	if data, err := readCatalogFile(selection); err == nil {
		var saved modelLibrary
		if json.Unmarshal(data, &saved) == nil {
			want, err := a.openCode2Config(saved)
			actual, readErr := readCatalogFile(config)
			merged, mergeErr := mergeOpenCode2Config(actual, want)
			ready = err == nil && readErr == nil && mergeErr == nil && bytes.Equal(actual, merged)
		}
	}
	jsonResponse(w, 200, map[string]any{"ready": ready, "prepared": ready, "requiresPreparation": !ready, "message": message, "path": config, "configPath": config, "profileDir": home, "library": library, "installURL": launchClientInstallURL("opencode2")})
}

func (a *app) applyOpenCode2Launch(plan *clientLaunchPlan, platform string) error {
	if plan.Executable != "" {
		if err := a.checkOpenCodeVersion(plan.Executable, 2); err != nil {
			return err
		}
	}
	home, config, selection := a.openCode2Paths()
	if !safeLaunchDir(filepath.Dir(config), a.dir) {
		return profileLaunchError("OpenCode 2")
	}
	data, err := readCatalogFile(selection)
	var library modelLibrary
	if err != nil || json.Unmarshal(data, &library) != nil {
		return profileLaunchError("OpenCode 2")
	}
	want, err := a.openCode2Config(library)
	actual, readErr := readCatalogFile(config)
	merged, mergeErr := mergeOpenCode2Config(actual, want)
	if err != nil || readErr != nil || mergeErr != nil || !bytes.Equal(actual, merged) {
		return profileLaunchError("OpenCode 2")
	}
	for _, entry := range t3CodeOptionalProviderEnvironment(home, nil, platform) {
		plan.Env[entry["name"].(string)] = entry["value"].(string)
	}
	plan.Env["KILO_OPENCODE2_API_KEY"] = a.config.LocalKey
	plan.Unset = append(plan.Unset, openCode2UnsetEnvironment(os.Environ())...)
	if plan.Kind == "terminal" && plan.Executable == "" {
		// The terminal wrapper inserts --standalone after the subcommand, where
		// OpenCode 2 accepts it. GUI launches set an executable and pass it
		// directly.
		plan.Args = nil
	} else {
		plan.Args = []string{"--standalone"}
	}
	return nil
}

// Update only Kilo-owned values. Keep user providers, MCP servers, comments and
// exact numeric literals in this isolated OpenCode 2 profile.
func mergeOpenCode2Config(old, desired []byte) ([]byte, error) {
	if len(bytes.TrimSpace(old)) == 0 {
		old = []byte("{}\n")
	}
	tree, err := hujson.Parse(old)
	if err != nil || tree.Value.Kind() != '{' || !uniqueEditorJSON(tree) {
		return nil, errors.New("OpenCode 2 settings must be valid JSON/JSONC with unique keys; nothing saved.")
	}
	var want map[string]json.RawMessage
	if json.Unmarshal(desired, &want) != nil {
		return nil, errors.New("Cannot build the OpenCode 2 configuration.")
	}
	var providers map[string]json.RawMessage
	if json.Unmarshal(want["providers"], &providers) != nil {
		return nil, errors.New("Cannot build the OpenCode 2 provider.")
	}
	if current := tree.Find("/providers"); current == nil {
		if err := tree.Patch([]byte(`[{"op":"add","path":"/providers","value":{}}]`)); err != nil {
			return nil, err
		}
	} else if current.Value.Kind() != '{' {
		return nil, errors.New("OpenCode 2 providers must be an object; nothing saved.")
	}
	for _, field := range []struct {
		path  string
		value json.RawMessage
	}{
		{"/$schema", want["$schema"]}, {"/model", want["model"]},
		{"/update", want["update"]}, {"/providers/kilo-local", providers["kilo-local"]},
	} {
		if current := tree.Find(field.path); current != nil {
			copy := current.Clone()
			copy.Standardize()
			if launchEqualJSON(copy.Pack(), field.value) {
				continue
			}
		}
		patch, err := json.Marshal([]map[string]any{{"op": "add", "path": field.path, "value": field.value}})
		if err != nil {
			return nil, err
		}
		if err := tree.Patch(patch); err != nil {
			return nil, err
		}
	}
	result := tree.Pack()
	if len(result) > catalogLimit {
		return nil, errors.New("OpenCode 2 settings exceed the size limit.")
	}
	return result, nil
}
