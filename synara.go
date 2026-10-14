package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const synaraProfileEndpoint = "/api/clients/synara"
const synaraProfileRevision = 5

type synaraManagedPaths struct{ Root, Data, UIHome, Electron, Settings, Secrets, Selection string }

func synaraPaths(appDir string) synaraManagedPaths {
	root := filepath.Join(appDir, "synara")
	data := filepath.Join(root, "data")
	return synaraManagedPaths{Root: root, Data: data, UIHome: filepath.Join(root, "ui-home"), Settings: filepath.Join(data, "userdata", "settings.json"), Electron: filepath.Join(root, "electron"), Secrets: filepath.Join(data, "userdata", "secrets"), Selection: filepath.Join(root, "selection.json")}
}

type synaraPrepared struct {
	SupportedProviders    []t3CodeProviderSupport `json:"supportedProviders"`
	OpenCodeLibrary       modelLibrary            `json:"openCodeLibrary"`
	OpenCode2Supported    bool                    `json:"openCode2Supported"`
	OpenCodeKiloSupported bool                    `json:"openCodeKiloSupported"`
	Preferences           *synaraPreferences      `json:"preferences,omitempty"`
	CodexLibrary          modelLibrary            `json:"codexLibrary"`
	ClaudeLibrary         modelLibrary            `json:"claudeLibrary"`
	ClaudeRoutes          map[string]string       `json:"claudeRoutes"`
	Library               modelLibrary            `json:"library"`
	Version               string                  `json:"version"`
	Fingerprint           string                  `json:"fingerprint"`
	Providers             map[string]any          `json:"providers"`
	Files                 map[string]string       `json:"files"`
	Runtime               *synaraPrivateRuntime   `json:"runtime,omitempty"`
}

func (a *app) synaraUsesPrivateRuntime(rt clientLaunchRuntime) bool {
	return !a.synaraRuntimeDisabled && (rt.platform == "macos" || rt.platform == "darwin")
}

type synaraModelSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

func (a *app) synaraLegacyFingerprint(binary string, library modelLibrary, rt clientLaunchRuntime) string {
	codex, _ := resolveOpenDesignCLI("codex-cli", rt)
	claude, _ := rt.resolve("claude", "")
	normalEnvironment := synaraNormalEnvironment(rt.home, os.Environ(), rt.platform)
	normalCodex, normalClaude, err := t3CodeNormalProviderHomes(t3CodeProfileOptions{NormalHome: rt.home, NormalEnvironment: normalEnvironment})
	if err != nil {
		return ""
	}
	canonicalCodex, err := t3CodeCanonicalPath(normalCodex)
	if err != nil {
		return ""
	}
	canonicalClaude, err := t3CodeCanonicalPath(normalClaude)
	if err != nil {
		return ""
	}
	secureStorage, err := synaraNormalSecureStorage(t3CodeProfileOptions{RootDir: synaraPaths(a.dir).Root, NormalHome: rt.home, NormalEnvironment: normalEnvironment})
	if err != nil {
		return ""
	}
	if secureStorage == "" {
		secureStorage = filepath.Join(rt.home, ".claude")
	}
	canonicalSecure, err := t3CodeCanonicalPath(secureStorage)
	if err != nil {
		return ""
	}
	adapterSourceHash, err := a.synaraAdapterSourceHash()
	if err != nil {
		return ""
	}
	// A retained symlink spelling can resolve to a different provider home
	// after preparation. Record its resolved directory identity as well.
	data, _ := json.Marshal([]any{binary, codex, claude, library, a.config.Port, a.config.LocalKey, a.config.OrgID, a.catalogScopeLocked(), a.clientImageSettingsLocked(), rt.home, normalEnvironment, canonicalCodex, canonicalClaude, canonicalSecure, synaraProfileRevision, adapterSourceHash})
	return openDesignHash(data)
}

func (a *app) readSynaraPrepared() (synaraPrepared, error) {
	var saved synaraPrepared
	if !safeLaunchDir(synaraPaths(a.dir).Root, a.dir) {
		return saved, errors.New("Prepare the private Synara profiles first.")
	}
	data, err := readCatalogFile(synaraPaths(a.dir).Selection)
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Fingerprint == "" || validateModelLibrary(saved.Library) != nil || (len(saved.Providers) < 4 || len(saved.Providers) > 10) || len(saved.Files) > 32 {
		return synaraPrepared{}, errors.New("Prepare the private Synara profiles first.")
	}
	if saved.Preferences == nil {
		if len(saved.Library.Models) == 0 || validateSynaraLibrary(saved.Library) != nil || len(saved.Files) < 4 {
			return synaraPrepared{}, errors.New("Prepare the private Synara profiles first.")
		}
	} else {
		if validateSynaraPreferences(*saved.Preferences) != nil || validateT3CodeLibrary(saved.CodexLibrary) != nil || validateT3CodeClaudeRoutes(saved.ClaudeLibrary, saved.ClaudeRoutes) != nil || len(saved.Files) == 0 {
			return synaraPrepared{}, errors.New("Invalid private Synara agent selection.")
		}
		if synaraUsesKilo(*saved.Preferences) && len(saved.Library.Models) == 0 {
			return synaraPrepared{}, errors.New("Invalid private Synara agent selection.")
		}
		for id, enabled := range map[string]bool{synaraCodexProxyID: saved.Preferences.KiloCodex, synaraClaudeProxyID: saved.Preferences.KiloClaude} {
			prefix := "profiles/codex-kilo/"
			names := []string{"config.toml", "models.json"}
			library := saved.CodexLibrary
			key := "KILO_LOCAL_API_KEY"
			if id == synaraClaudeProxyID {
				prefix = "profiles/claude-kilo/"
				names = []string{"settings.json", "kilo-models.json"}
				library = saved.ClaudeLibrary
				key = "ANTHROPIC_AUTH_TOKEN"
			}
			if enabled != (len(library.Models) > 0) {
				return synaraPrepared{}, errors.New("Invalid private Synara model selection.")
			}
			for _, name := range names {
				_, exists := saved.Files[filepath.FromSlash(prefix+name)]
				if exists != enabled {
					return synaraPrepared{}, errors.New("Invalid private Synara active-profile manifest.")
				}
			}
			secret := filepath.Join("data", "userdata", "secrets", t3CodeSecretName(id, key))
			_, exists := saved.Files[secret]
			if exists != enabled {
				return synaraPrepared{}, errors.New("Invalid private Synara active-profile manifest.")
			}
		}
	}
	if err := validateSynaraCoreProviders(saved); err != nil {
		return synaraPrepared{}, err
	}
	if err := validateSynaraAdditionalManifest(saved); err != nil {
		return synaraPrepared{}, err
	}
	if err := validateSynaraPreparedFiles(saved); err != nil {
		return synaraPrepared{}, err
	}
	for _, id := range []string{synaraCodexNormalID, synaraCodexProxyID, synaraClaudeNormalID, synaraClaudeProxyID} {
		if saved.Providers[id] == nil {
			return synaraPrepared{}, errors.New("Invalid private Synara agent selection.")
		}
	}
	return saved, nil
}

func (a *app) synaraReady(saved synaraPrepared, binary string, rt clientLaunchRuntime) bool {
	_, err := synaraPackageMetadata(binary, rt.platform)
	if err != nil || (saved.Preferences == nil) != (a.config.Synara.Version == 0) || saved.Preferences != nil && !reflect.DeepEqual(*saved.Preferences, normalizeSynaraPreferences(a.config.Synara)) || saved.Fingerprint != a.synaraFingerprint(binary, saved.Library, rt) {
		return false
	}
	options, optionsErr := a.synaraOptionsChecked(rt, saved.Library)
	if optionsErr != nil {
		return false
	}
	if saved.Preferences != nil {
		policy := synaraOptionsPolicy(options)
		if !reflect.DeepEqual(saved.CodexLibrary, policy.CodexLibrary) || !reflect.DeepEqual(saved.ClaudeLibrary, policy.ClaudeLibrary) || !reflect.DeepEqual(saved.OpenCodeLibrary, policy.OpenCodeLibrary) || !reflect.DeepEqual(saved.ClaudeRoutes, policy.ClaudeRoutes) || !reflect.DeepEqual(saved.SupportedProviders, options.SupportedProviders) || saved.OpenCode2Supported != options.OpenCode2Supported || saved.OpenCodeKiloSupported != options.OpenCodeKiloSupported {
			return false
		}
	}
	if saved.Preferences != nil && (!synaraCoreProvidersReady(options, saved.Providers) || !synaraAdditionalProvidersReady(options, saved.Providers)) {
		return false
	}
	paths := synaraPaths(a.dir)
	if !safeLaunchDir(paths.Root, a.dir) {
		return false
	}
	if a.synaraUsesPrivateRuntime(rt) {
		if saved.Runtime == nil || !synaraPrivateRuntimeReady(options, binary, rt.platform, *saved.Runtime) {
			return false
		}
	} else if saved.Runtime != nil {
		return false
	}
	if saved.Preferences == nil || saved.Preferences.Codex {
		shim := synaraCodexNormalShimPath(paths.Root, rt.platform)
		shimRelative, _ := filepath.Rel(paths.Root, shim)
		descriptorRelative := filepath.Join("adapters", synaraCodexNormalDescriptorName)
		sourceHash, sourceErr := a.synaraAdapterSourceHash()
		if sourceErr != nil || sourceHash == "" || saved.Files[shimRelative] != sourceHash || saved.Files[descriptorRelative] == "" {
			return false
		}
		normal, ok := saved.Providers[synaraCodexNormalID].(map[string]any)
		if !ok {
			return false
		}
		normalConfig, ok := normal["config"].(map[string]any)
		if !ok || normalConfig["binaryPath"] != shim {
			return false
		}
		target, err := synaraCodexNormalShimTarget(shim, rt.platform)
		codex, resolveErr := resolveOpenDesignCLI("codex-cli", rt)
		canonicalCodex, canonicalErr := filepath.EvalSymlinks(codex)
		if err != nil || resolveErr != nil || canonicalErr != nil || target != canonicalCodex {
			return false
		}
		if info, err := os.Lstat(shim); err != nil || rt.platform != "windows" && info.Mode().Perm() != 0700 {
			return false
		}
	}
	for relative, expected := range saved.Files {
		if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return false
		}
		path := filepath.Join(paths.Root, relative)
		if !safeLaunchDir(filepath.Dir(path), a.dir) {
			return false
		}
		digest, err := openDesignFileHash(path, synaraCodexNormalFileLimit(relative, rt.platform))
		if err != nil || digest != expected {
			return false
		}
	}
	return synaraProfilesReady(options, saved.Providers)
}

func (a *app) prepareSynara(library modelLibrary, rt clientLaunchRuntime, binary string) error {
	if a.config.Synara.Version != 0 && a.config.Synara.KiloClaude && !a.synaraRuntimeDisabled && rt.platform != "macos" && rt.platform != "darwin" {
		return errors.New("This Synara installation cannot restrict the Kilo Claude picker to its prepared models. Choose the native Claude account or a compatible macOS build.")
	}
	if err := a.synaraAvailabilityFor(binary, rt, normalizeSynaraPreferences(a.config.Synara)); err != nil {
		return err
	}
	if err := a.synaraAdditionalAvailability(binary, rt, normalizeSynaraPreferences(a.config.Synara), true); err != nil {
		return err
	}
	version, _ := synaraVersion(binary, rt.platform)

	paths := synaraPaths(a.dir)
	for _, dir := range []string{paths.Root, paths.Secrets, paths.UIHome} {
		if err := safeEditorDir(a.dir, dir); err != nil {
			return err
		}
	}
	options, err := a.synaraOptionsChecked(rt, library)
	if err != nil {
		return err
	}
	if options.Preferences != nil {
		policy, err := deriveSynaraModelPolicy(*options.Preferences, library, options.Catalog)
		if err != nil {
			return err
		}
		options.Policy = &policy
	}
	if options.Preferences == nil || options.Preferences.KiloClaude {
		options.ClaudeCaps = a.synaraClaudeCapabilities(options.ClaudeBinary, rt)
	}
	planned, err := planSynaraProfiles(options)
	if err != nil {
		return err
	}
	planned.Providers = normalizeT3CodeProviders(planned.Providers)
	files := planned.Files
	if options.Preferences == nil || options.Preferences.Codex {
		adapterSource := a.synaraAdapterSource
		if adapterSource == nil {
			adapterSource = os.Executable
		}
		source, err := adapterSource()
		if err != nil {
			return errors.New("Cannot locate Kilo Proxy's native Synara Codex adapter.")
		}
		adapterFiles, err := planSynaraCodexNormalShim(paths.Root, options.CodexBinary, source, rt.platform)
		if err != nil {
			return err
		}
		files = append(files, adapterFiles...)

		adapterHash, err := a.synaraAdapterSourceHash()
		if err != nil || adapterHash != openDesignHash(adapterFiles[0].new) {
			return errors.New("Kilo Proxy's native Synara adapter changed during preparation. Restart Kilo Proxy and prepare again.")
		}
	}
	fingerprint := a.synaraFingerprint(binary, library, rt)
	if fingerprint == "" {
		return errors.New("Cannot safely identify the private Synara configuration; no profile changes saved.")
	}
	saved := synaraPrepared{SupportedProviders: options.SupportedProviders, Library: library, Version: version, Fingerprint: fingerprint, Providers: planned.Providers, Files: map[string]string{}, Preferences: options.Preferences}
	if options.Policy != nil {
		saved.OpenCodeLibrary = options.Policy.OpenCodeLibrary
		saved.OpenCode2Supported = options.OpenCode2Supported
		saved.OpenCodeKiloSupported = options.OpenCodeKiloSupported
		saved.CodexLibrary = options.Policy.CodexLibrary
		saved.ClaudeLibrary = options.Policy.ClaudeLibrary
		saved.ClaudeRoutes = options.Policy.ClaudeRoutes
	}
	if a.synaraUsesPrivateRuntime(rt) {
		privateRuntime, err := prepareSynaraPrivateRuntime(options, binary, rt.platform)
		if err != nil {
			return err
		}
		saved.Runtime = &privateRuntime
	}
	for _, file := range files {
		if file.path == paths.Settings || file.path == filepath.Join(paths.Electron, "synara-storage-origin-v1.json") {
			continue
		}
		relative, err := filepath.Rel(paths.Root, file.path)
		if err != nil {
			return err
		}
		saved.Files[relative] = openDesignHash(file.new)
	}
	data, _ := json.MarshalIndent(saved, "", "  ")
	selection, err := prepareProfileFile(paths.Selection, append(data, '\n'))
	if err != nil {
		return err
	}
	_, err = saveEditorFiles(append(files, selection))
	return err
}

func (a *app) synaraProfileAPI(w http.ResponseWriter, r *http.Request) {
	rt := a.launchRuntime()
	if r.Method == http.MethodGet {
		binary, resolveErr := rt.resolve("synara", "")
		paths := synaraPaths(a.dir)
		state := a.modelLibrary.snapshot()
		library := state.Library
		version, _ := synaraVersion(binary, rt.platform)
		message := "Prepare Synara with your selected accounts and models in a separate window."
		if resolveErr == nil {
			resolveErr = a.synaraAvailability(binary, rt)
		}
		if resolveErr != nil {
			message = resolveErr.Error()
		}
		a.mu.Lock()
		saved, err := a.readSynaraPrepared()
		ready := resolveErr == nil && err == nil && a.synaraReady(saved, binary, rt)
		prefs, prefsErr := a.synaraPreferencesSnapshotLocked()
		if ready {
			library = saved.Library
		}
		policy, _ := deriveSynaraModelPolicy(prefs, library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()))
		a.mu.Unlock()
		if prefsErr != nil {
			ready = false
			message = prefsErr.Error()
		}
		models := []synaraModelSummary{}
		if ready {
			message = "Synara prepared with your selected accounts and models."
			library = saved.Library
			for _, model := range library.Models {
				models = append(models, synaraModelSummary{ID: model.ID, Name: model.DisplayName, ReasoningEffort: model.ReasoningEffort})
			}
		}
		jsonResponse(w, 200, synaraResponseCapabilities(map[string]any{"prepared": ready, "library": library, "profileDir": paths.Root, "version": version, "models": models, "message": message, "preferences": prefs, "effectiveCodexModels": synaraModelSummaries(policy.CodexLibrary), "effectiveClaudeModels": synaraModelSummaries(policy.ClaudeLibrary), "effectiveOpenCodeModels": synaraModelSummaries(policy.OpenCodeLibrary)}, binary, rt.platform))
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input struct {
		Library     *modelLibrary   `json:"library,omitempty"`
		Preferences json.RawMessage `json:"preferences,omitempty"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if !a.launchMu.TryLock() {
		jsonError(w, 409, "Another agent is being prepared. Try again.")
		return
	}
	defer a.launchMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(input.Preferences) > 0 {
		prefs, err := decodeSynaraPreferences(input.Preferences)
		if err != nil {
			jsonError(w, 400, err.Error())
			return
		}
		current, currentErr := a.synaraPreferencesSnapshotLocked()
		if currentErr != nil {
			jsonError(w, 409, currentErr.Error())
			return
		}
		prefs = preserveSynaraConnection(prefs, input.Preferences, current)
		if !reflect.DeepEqual(prefs, current) {
			jsonError(w, 409, "Synara settings changed. Reload its settings before preparing again.")
			return
		}
		if err = a.saveSynaraPreferencesLocked(prefs); err != nil {
			jsonError(w, 500, err.Error())
			return
		}
	}
	prefs := normalizeSynaraPreferences(a.config.Synara)
	// Resolve and validate the current choice under the preparation transaction.
	binary, resolveErr := rt.resolve("synara", "")
	if resolveErr != nil {
		jsonError(w, 409, resolveErr.Error())
		return
	}
	if err := a.synaraAvailabilityFor(binary, rt, prefs); err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	state := a.modelLibrary.snapshot()
	library := state.Library
	if !synaraUsesKilo(prefs) {
		library = emptyModelLibrary()
	} else if input.Library != nil {
		library = *input.Library
	} else if state.RecoveryRequired || state.Warning != "" {
		jsonError(w, 409, "Recover and save your shared models first.")
		return
	}
	if err := validateModelLibrary(library); err != nil {
		jsonError(w, 400, err.Error())
		return
	}
	if synaraUsesKilo(prefs) && len(library.Models) == 0 {
		jsonError(w, 400, "Choose a valid shared model library before preparing Synara.")
		return
	}
	if synaraUsesKilo(prefs) && !a.connectionReadyLocked() {
		jsonError(w, 409, "Connect the selected provider first.")
		return
	}
	paths := synaraPaths(a.dir)
	saved, readErr := a.readSynaraPrepared()
	unchanged := readErr == nil && reflect.DeepEqual(saved.Library, library) && a.synaraReady(saved, binary, rt)
	check := a.synaraCheckRunning
	if check == nil {
		check = synaraRunning
	}
	running, err := check(paths.Root)
	if err != nil {
		jsonError(w, 409, "Cannot verify whether Synara Kilo is running. Close its window and try again.")
		return
	}
	if (running || time.Now().Before(a.synaraLaunchUntil)) && !unchanged {
		jsonError(w, 409, "Quit the Synara Kilo window before changing its models or connection. Your regular Synara window can stay open.")
		return
	}
	if !unchanged {
		if err = a.prepareSynara(library, rt, binary); err != nil {
			jsonError(w, 409, err.Error())
			return
		}
	}
	models := []synaraModelSummary{}
	for _, model := range library.Models {
		models = append(models, synaraModelSummary{ID: model.ID, Name: model.DisplayName, ReasoningEffort: model.ReasoningEffort})
	}
	version, _ := synaraVersion(binary, rt.platform)
	policy, _ := deriveSynaraModelPolicy(prefs, library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()))
	jsonResponse(w, 200, synaraResponseCapabilities(map[string]any{"prepared": true, "library": library, "profileDir": paths.Root, "version": version, "models": models, "message": "Synara prepared with your selected accounts and models.", "preferences": prefs, "effectiveCodexModels": synaraModelSummaries(policy.CodexLibrary), "effectiveClaudeModels": synaraModelSummaries(policy.ClaudeLibrary), "effectiveOpenCodeModels": synaraModelSummaries(policy.OpenCodeLibrary)}, binary, rt.platform))
}
