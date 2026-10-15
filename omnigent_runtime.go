package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const omnigentSupportedVersion = "0.18.0"

//go:embed omnigent_runtime.py
var omnigentRuntimePython string

// Source identities of the official v0.18.0 (a8676d23) Python package. A custom
// executable is accepted only when its own interpreter and package satisfy this
// contract; discovery never imports the package or runs a CLI.
var omnigentSourcePins = map[string]string{
	"runner/identity.py":                   "5eb81cada762d1b3c3832ebae6177385122b691e29f9eb94b500992608f6b609",
	"runner/_entry.py":                     "27b42bf8da201a3a141e2d3c59163932f64e9eba3085dd4d98152abe7b44e68f",
	"runner/_zygote.py":                    "9e4497927d9640f1a23f3cac76b9973b08eeabf7918f456322106b986e03648b",
	"runtime/harnesses/_runner.py":         "7852877e30c39f67ece3e70d73d47ebe141749c3f41eb77c4d2a1fdde9744b9f",
	"runtime/harnesses/process_manager.py": "e4b46dfee441e634f280f0c97e53e03bc5938c684b6b86c0a9237f0b4b6fadde",
	"runtime/harnesses/paths.py":           "00d7dfdf380d84f4bdf6e9fe16a03d47f6e53f1ecf31f39ec5dd073b3419db49",
	"host/runner_zygote.py":                "7208fabf918ac910e000914a3fe7b844f5d2d3d625f7bcabd2690f21728bb166",
	"harnesses/codex_native/main.py":       "d02c1d5686e5e1adf482813a1fd1f8be91857ce86dae75e2cc826c04362f9587",
	"onboarding/harness_readiness.py":      "df070f38d603fc0cc9c3e490fabcb543271b49561bf86093c251c1f56e69141a",
	"onboarding/harness_install.py":        "580446c0eb9e575e51302968e17e47ac661f17c24b99a458730bb401694c255a",
	"_platform.py":                         "d807d1449aa9f334081c77239c643232cbd6d75f32d61709169bba9bd164235f",
	"cli.py":                               "f2ff750da23c13bad7e2586d12dc8f21bd7e839a98a2339e3dff94f9764d49b6",
	"onboarding/provider_config.py":        "b8cea57f2ce9faf7d3c2b8b529a8580b098ffd10e25c0359abf325a25636348e",
	"inner/codex_executor.py":              "b139579edbfc290292205d1ecd763fabf22a8604d02a0494204c632db490ac68",
	"inner/claude_sdk_executor.py":         "e10ba07a9dfa17aa68b3a40e47287eca6335ad7dd8e43e33a83df240cd6e4338",
	"inner/claude_gateway_shim.py":         "d1eb0995facf229a3df58c9e1ddeb580c0011f5941220a07c253709cbe3954d9",
	"runner/native/orchestration.py":       "3ee910041e531a20b0f2318f6db948a9029e8e283911e1434ba7f8364fc4aa71",
	"inference_config.py":                  "0b906c8bc4afc8681957ddaaae4190b71dc113d53900292e1b9f0b7ad6ec1506",
	"models/model_catalog.py":              "49e9a48ee52965a4bd77c2c5461aa6d1979ab9b3ed607ff912976a051f04b065",
	"runner/native_controls.py":            "a091b2f362ac91a5355376d1800bdbdf578be40010c7637864b555d7b84687d7",
	"runner/tool_dispatch.py":              "bf6de820d724086fe4f6bf54ce70a4f9e26dcd0738c7c2eed238364e8eb9134f",
	"runner/app.py":                        "aa0fd78b52af07cb5f4ddb27e47a9ac6114b6d6e535b0534b84d419c5f841880",
	"runner/session_init_protocol.py":      "e5cef5c2e46bbea81043b9d39655fcb0f135ca2daefabfcf770cf3160309c5ea",
	"runtime/workflow.py":                  "3a791c33b21d0ea4f1de6f4bb6671a4e0f729e156cbfe104f8b733ef0f19867c",
	"host/connect.py":                      "5fadee9f233bf3b523015d2e50dcb4205f155f9d384c8069aeacc65f36a96c77",
	"host/frames.py":                       "bd1313ef027db67b2b0f333a3c8e8d0a8d5a4804d9ba3c388ea67851a4270228",
	"harness_plugins.py":                   "5bce1ba68edcc7f0135d15d71ecc83b31b57acd889544aa91e4ae324e625a5e8",
	"harness_startup_config.py":            "ebeff7cf0e03ecae84b9bd1f6ee44695378e5867ab968b492d6e0f1db28a1f1d",
	"util/reasoning_effort.py":             "16ac7a176d8131a06328e82b5df2c97cd08d7e7807efd9a82de577c30fe4557b",
	"harnesses/claude_native/main.py":      "f42c6d8742aff1b7cc3dd4e2bb0457b4fded1e9d11e6a57c284474f0c77ef7ab",
	"harnesses/codex_native/app_server.py": "de8e63e8ffe6870c84620c9306e9f07b96245251ffaff5faa91bfb5ea02c4bbf",
	"server/app.py":                        "8690ac93aea52410ac17919a25607b9334b50995cd8619972bc7019a4089c78c",
	"server/routes/_sessions/helpers.py":   "11b89efe8dd32267caceebb7cbea034c94b7d820f68308d5b260eb2ef5a6a96e",
}

type omnigentInstallation struct{ PythonPath, SourceRoot, Version, Fingerprint string }
type omnigentRuntimeOptions struct {
	CodexSelectionFingerprint                                      string
	RootDir, OmniPath, Platform, NormalHome, CodexPath, ClaudePath string
	ConfigPath, InferencePath, CodexToken, ClaudeToken             string
	Preferences                                                    omnigentPreferences
	CodexLibrary, ClaudeLibrary                                    modelLibrary
	Catalog                                                        []modelInfo
	Port                                                           int
	NormalEnvironment                                              map[string]string
}
type omnigentRuntime struct {
	PythonPath, SourceRoot, Version, Fingerprint string
	Files                                        map[string]string // relative path -> SHA256; no credential values
	Env                                          map[string]string
	Args                                         []string
}
type omnigentRuntimeModel struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ReasoningLevels  []string `json:"reasoningLevels"`
	DefaultReasoning string   `json:"defaultReasoning"`
}
type omnigentRuntimeLibrary struct {
	DefaultModel string                 `json:"defaultModel"`
	Models       []omnigentRuntimeModel `json:"models"`
}
type omnigentRuntimeDescriptor struct {
	Version          int                               `json:"version"`
	Root             string                            `json:"root"`
	OmniPath         string                            `json:"omniPath"`
	PythonPath       string                            `json:"pythonPath"`
	SourceRoot       string                            `json:"sourceRoot"`
	SourceHashes     map[string]string                 `json:"sourceHashes"`
	PackageVersion   string                            `json:"packageVersion"`
	Fingerprint      string                            `json:"fingerprint"`
	ConfigPath       string                            `json:"configPath"`
	InferencePath    string                            `json:"inferencePath"`
	ConfigHash       string                            `json:"configHash"`
	InferenceHash    string                            `json:"inferenceHash"`
	CodexPath        string                            `json:"codexPath"`
	ClaudePath       string                            `json:"claudePath"`
	NormalHome       string                            `json:"normalHome"`
	NormalCodexHome  string                            `json:"normalCodexHome"`
	NormalClaudeHome string                            `json:"normalClaudeHome"`
	Preferences      omnigentPreferences               `json:"preferences"`
	Libraries        map[string]omnigentRuntimeLibrary `json:"modelLibraries"`
	ServerPort       int                               `json:"serverPort"`
}

func omnigentHash(data []byte) string              { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func omnigentRuntimeSourceHash() string            { return omnigentHash([]byte(omnigentRuntimePython)) }
func omnigentFileHash(path string) (string, error) { return omnigentBoundedFileHash(path, 16<<20) }
func omnigentExecutableHash(path string) (string, error) {
	return omnigentBoundedFileHash(path, 512<<20)
}
func omnigentBoundedFileHash(path string, limit int64) (string, error) {
	f, err := synaraOpenCandidate(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return "", errors.New("Invalid Omnigent installation file.")
	}
	hash := sha256.New()
	if count, readErr := io.Copy(hash, io.LimitReader(f, limit+1)); readErr != nil || count > limit {
		if readErr == nil {
			readErr = errors.New("Omnigent file exceeds its limit.")
		}
		err = readErr
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func omnigentOpenRegular(path string, limit int64) (*os.File, error) {
	f, err := synaraOpenCandidate(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		f.Close()
		return nil, errors.New("Invalid Omnigent installation file.")
	}
	return f, nil
}
func omnigentReadRegular(path string, limit int64) ([]byte, error) {
	f, err := omnigentOpenRegular(path, limit)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Invalid Omnigent installation file.")
	}
	return data, nil
}
func inspectOmnigentInstallation(path, platform string) (omnigentInstallation, error) {
	return inspectOmnigentInstallationPins(path, platform, omnigentSourcePins)
}
func inspectOmnigentInstallationPins(path, platform string, pins map[string]string) (omnigentInstallation, error) {
	fail := errors.New("Choose the official Omnigent 0.18.0 Python CLI installation. Its interpreter and source contract must be available; standalone desktop launchers are not supported.")
	if platform == "windows" {
		return omnigentInstallation{}, errors.New("Omnigent integration currently requires its Python CLI on macOS or Linux. The Windows launcher is not supported yet.")
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return omnigentInstallation{}, fail
	}
	reader, err := omnigentOpenRegular(path, 64<<10)
	if err != nil {
		return omnigentInstallation{}, fail
	}
	script, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	_ = reader.Close()
	if err != nil || len(script) > 64<<10 || !strings.Contains(string(script), "omnigent.cli") {
		return omnigentInstallation{}, fail
	}
	line := strings.SplitN(string(script), "\n", 2)[0]
	if !strings.HasPrefix(line, "#!/") {
		return omnigentInstallation{}, fail
	}
	python := strings.TrimPrefix(strings.TrimSpace(line), "#!")
	if strings.ContainsAny(python, "\t \r\n") || !strings.HasPrefix(filepath.Base(python), "python") {
		return omnigentInstallation{}, fail
	}
	if st, err := os.Stat(python); err != nil || !st.Mode().IsRegular() || !launchExecutable(python) {
		return omnigentInstallation{}, fail
	}
	// Resolve a symlinked console script to its venv, but retain the interpreter
	// spelling from the shebang: Python uses that spelling to locate pyvenv.cfg.
	realScript, err := filepath.EvalSymlinks(path)
	if err != nil {
		return omnigentInstallation{}, fail
	}
	venv := filepath.Dir(filepath.Dir(realScript))
	candidates, err := filepath.Glob(filepath.Join(venv, "lib", "python*", "site-packages", "omnigent"))
	if err != nil || len(candidates) != 1 {
		return omnigentInstallation{}, fail
	}
	source := candidates[0]
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return omnigentInstallation{}, fail
	}
	metadata := filepath.Join(filepath.Dir(source), "omnigent-"+omnigentSupportedVersion+".dist-info", "METADATA")
	metadataReader, err := omnigentOpenRegular(metadata, 1<<20)
	if err != nil {
		return omnigentInstallation{}, fail
	}
	data, err := io.ReadAll(io.LimitReader(metadataReader, (1<<20)+1))
	_ = metadataReader.Close()
	if err != nil || len(data) > 1<<20 || !strings.Contains("\n"+string(data), "\nVersion: "+omnigentSupportedVersion+"\n") {
		return omnigentInstallation{}, fail
	}
	observed := map[string]string{}
	for relative, want := range pins {
		actual, err := omnigentFileHash(filepath.Join(source, filepath.FromSlash(relative)))
		if err != nil || actual != want {
			return omnigentInstallation{}, fail
		}
		observed[relative] = actual
	}
	for _, file := range []string{path, metadata} {
		hash, err := omnigentFileHash(file)
		if err != nil {
			return omnigentInstallation{}, fail
		}
		observed[file] = hash
	}
	pythonHash, err := omnigentExecutableHash(python)
	if err != nil {
		return omnigentInstallation{}, fail
	}
	observed[python] = pythonHash
	web, err := planOmnigentWebFiles(source, false)
	if err != nil {
		return omnigentInstallation{}, fail
	}
	for relative, data := range web {
		observed[relative] = omnigentHash([]byte(data))
	}
	raw, _ := json.Marshal(observed)
	return omnigentInstallation{PythonPath: python, SourceRoot: source, Version: omnigentSupportedVersion, Fingerprint: omnigentHash(raw)}, nil
}
func omnigentRuntimeDescription(o omnigentRuntimeOptions) (omnigentRuntimeDescriptor, omnigentInstallation, error) {
	var d omnigentRuntimeDescriptor
	if err := validateOmnigentPreferences(normalizeOmnigentPreferences(o.Preferences)); err != nil {
		return d, omnigentInstallation{}, err
	}
	if o.Port < 0 || o.Port > 65535 || (o.Port > 0 && o.Port < 1024) {
		return d, omnigentInstallation{}, errors.New("Choose an available unprivileged Omnigent server port.")
	}
	for _, p := range []string{o.RootDir, o.NormalHome} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n") {
			return d, omnigentInstallation{}, errors.New("Omnigent requires absolute private workspace and home paths.")
		}
	}
	installation, err := inspectOmnigentInstallation(o.OmniPath, o.Platform)
	if err != nil {
		return d, installation, err
	}
	if o.ConfigPath == "" {
		o.ConfigPath = filepath.Join(o.RootDir, "config", "config.yaml")
	}
	if o.InferencePath == "" {
		o.InferencePath = filepath.Join(o.RootDir, "inference.json")
	}
	for _, p := range []string{o.ConfigPath, o.InferencePath} {
		rel, err := filepath.Rel(o.RootDir, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return d, installation, errors.New("Omnigent configuration must stay inside its private workspace.")
		}
	}
	configHash, err := omnigentFileHash(o.ConfigPath)
	if err != nil {
		return d, installation, err
	}
	inferenceHash, err := omnigentFileHash(o.InferencePath)
	if err != nil {
		return d, installation, err
	}
	prefs := normalizeOmnigentPreferences(o.Preferences)
	d = omnigentRuntimeDescriptor{Version: 1, Root: o.RootDir, OmniPath: o.OmniPath, PythonPath: installation.PythonPath, SourceRoot: installation.SourceRoot, SourceHashes: omnigentSourcePins, PackageVersion: installation.Version, ConfigPath: o.ConfigPath, InferencePath: o.InferencePath, ConfigHash: configHash, InferenceHash: inferenceHash, CodexPath: o.CodexPath, ClaudePath: o.ClaudePath, NormalHome: o.NormalHome, NormalCodexHome: filepath.Join(o.NormalHome, ".codex"), NormalClaudeHome: filepath.Join(o.NormalHome, ".claude"), Preferences: prefs, Libraries: map[string]omnigentRuntimeLibrary{}, ServerPort: o.Port}
	if p := o.NormalEnvironment["CODEX_HOME"]; p != "" {
		d.NormalCodexHome = p
	}
	if p := o.NormalEnvironment["CLAUDE_CONFIG_DIR"]; p != "" {
		d.NormalClaudeHome = p
	}
	for _, item := range []struct {
		name, path, connection string
		enabled                bool
		library                modelLibrary
	}{{"codex", o.CodexPath, prefs.CodexConnection, prefs.Codex, o.CodexLibrary}, {"claude", o.ClaudePath, prefs.ClaudeConnection, prefs.Claude, o.ClaudeLibrary}} {
		if !item.enabled {
			continue
		}
		if !filepath.IsAbs(item.path) || validateCLIExecutable(item.path) != nil {
			return d, installation, errors.New("Choose an installed executable for each enabled Omnigent agent.")
		}
		if item.connection != "kilo" {
			continue
		}
		if err := validateModelLibrary(item.library); err != nil {
			return d, installation, err
		}
		if len(item.library.Models) == 0 {
			return d, installation, errors.New("Choose models for each enabled Omnigent Kilo connection.")
		}
		library := omnigentRuntimeLibrary{DefaultModel: item.library.DefaultModel, Models: []omnigentRuntimeModel{}}
		for _, choice := range terminalLibraryChoices(item.library, o.Catalog) {
			levels, initial := nativeReasoningFor(choice)
			if levels == nil {
				levels = []string{}
			}
			library.Models = append(library.Models, omnigentRuntimeModel{ID: choice.Model.ID, Name: choice.DisplayName, ReasoningLevels: levels, DefaultReasoning: initial})
		}
		d.Libraries[item.name] = library
	}
	identities := map[string]string{}
	for _, item := range []struct {
		enabled bool
		path    string
	}{{prefs.Codex, o.CodexPath}, {prefs.Claude, o.ClaudePath}} {
		if item.enabled {
			hash, err := omnigentExecutableHash(item.path)
			if err != nil {
				return d, installation, err
			}
			canonical, err := filepath.EvalSymlinks(item.path)
			if err != nil {
				return d, installation, err
			}
			identities[item.path] = canonical + ":" + hash
		}
	}
	for _, item := range []struct {
		enabled bool
		path    string
	}{{prefs.Codex, d.NormalCodexHome}, {prefs.Claude, d.NormalClaudeHome}} {
		if !item.enabled {
			continue
		}
		if !filepath.IsAbs(item.path) {
			return d, installation, errors.New("Omnigent normal CLI homes must be absolute.")
		}
		canonical, err := t3CodeCanonicalPath(item.path)
		if err != nil {
			return d, installation, err
		}
		identities[item.path] = canonical
		for _, private := range []string{filepath.Join(o.RootDir, "profiles", "codex-kilo"), filepath.Join(o.RootDir, "profiles", "claude-kilo"), filepath.Join(o.RootDir, "home"), filepath.Join(o.RootDir, "data"), filepath.Join(o.RootDir, "config"), filepath.Join(o.RootDir, "xdg")} {
			resolved, err := t3CodeCanonicalPath(private)
			if err != nil {
				return d, installation, err
			}
			if withinT3CodePath(canonical, resolved) || withinT3CodePath(resolved, canonical) {
				return d, installation, errors.New("Omnigent Kilo profiles must be separate from normal CLI homes.")
			}
		}
	}
	if prefs.Codex && prefs.CodexConnection == "native" || prefs.Claude && prefs.ClaudeConnection == "native" {
		for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
			if key == "CLAUDE_SECURESTORAGE_CONFIG_DIR" && !(prefs.Claude && prefs.ClaudeConnection == "native") {
				continue
			}
			if path := o.NormalEnvironment[key]; path != "" {
				if !filepath.IsAbs(path) {
					return d, installation, errors.New("Omnigent normal CLI homes must be absolute.")
				}
				canonical, err := t3CodeCanonicalPath(path)
				if err != nil {
					return d, installation, err
				}
				identities[key] = canonical
				for _, private := range []string{"profiles", "home", "data", "config", "xdg", "runtime"} {
					resolved, err := t3CodeCanonicalPath(filepath.Join(o.RootDir, private))
					if err != nil {
						return d, installation, err
					}
					if withinT3CodePath(canonical, resolved) || withinT3CodePath(resolved, canonical) {
						return d, installation, errors.New("Omnigent Kilo profiles must be separate from normal CLI homes.")
					}
				}
			}
		}
	}

	raw, _ := json.Marshal(struct {
		Descriptor                                                            omnigentRuntimeDescriptor
		Installation, Source, CodexTokenHash, ClaudeTokenHash, CodexSelection string
		Identities                                                            map[string]string
	}{d, installation.Fingerprint, omnigentRuntimeSourceHash(), omnigentHash([]byte(o.CodexToken)), omnigentHash([]byte(o.ClaudeToken)), o.CodexSelectionFingerprint, identities})
	d.Fingerprint = omnigentHash(raw)
	return d, installation, nil
}
func omnigentRuntimeIdentity(o omnigentRuntimeOptions) (string, error) {
	d, _, err := omnigentRuntimeDescription(o)
	return d.Fingerprint, err
}
func planOmnigentRuntime(o omnigentRuntimeOptions) (omnigentRuntime, map[string]string, error) {
	d, installation, err := omnigentRuntimeDescription(o)
	if err != nil {
		return omnigentRuntime{}, nil, err
	}
	runtimeDir := filepath.Join(o.RootDir, "runtime")

	descriptor, _ := json.MarshalIndent(d, "", "  ")
	descriptor = append(descriptor, '\n')
	site := "import os, sys\nif os.environ.get('KILO_OMNIGENT_DESCRIPTOR'):\n try:\n  from omnigent_runtime import install_from_environment\n  install_from_environment()\n except BaseException:\n  sys.stderr.write('The private Omnigent runtime failed its compatibility check. Prepare it again.\\n')\n  os._exit(78)\n"
	files, err := planOmnigentWebFiles(installation.SourceRoot, d.Preferences.Claude && d.Preferences.ClaudeConnection == "kilo")
	if err != nil {
		return omnigentRuntime{}, nil, err
	}
	files["runtime/omnigent_runtime.py"], files["runtime/sitecustomize.py"], files["runtime/descriptor.json"] = omnigentRuntimePython, site, string(descriptor)
	if d.Preferences.Codex && d.Preferences.CodexConnection == "kilo" {
		catalog, err := buildCodexCatalog(terminalLibraryChoices(o.CodexLibrary, o.Catalog), o.CodexLibrary.DefaultModel, false)
		if err != nil {
			return omnigentRuntime{}, nil, err
		}
		modelsPath := filepath.Join(o.RootDir, "profiles", "codex-kilo", "models.json")
		files["profiles/codex-kilo/models.json"] = string(catalog)
		quotedModel, _ := json.Marshal(o.CodexLibrary.DefaultModel)
		quotedPath, _ := json.Marshal(modelsPath)
		configuration := "model = " + string(quotedModel) + "\nmodel_catalog_json = " + string(quotedPath) + "\ncli_auth_credentials_store = \"file\"\n"
		for _, row := range d.Libraries["codex"].Models {
			if row.ID == o.CodexLibrary.DefaultModel && row.DefaultReasoning != "" {
				quoted, _ := json.Marshal(row.DefaultReasoning)
				configuration += "model_reasoning_effort = " + string(quoted) + "\n"
			}
		}
		files["profiles/codex-kilo/config.toml"] = configuration
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	result := omnigentRuntime{PythonPath: installation.PythonPath, SourceRoot: installation.SourceRoot, Version: installation.Version, Fingerprint: d.Fingerprint, Files: map[string]string{}, Env: map[string]string{}, Args: []string{"-I", filepath.Join(runtimeDir, "omnigent_runtime.py"), "--supervise", filepath.Join(runtimeDir, "descriptor.json")}}
	for _, relative := range keys {
		result.Files[relative] = omnigentHash([]byte(files[relative]))
	}
	result.Env["KILO_OMNIGENT_DESCRIPTOR"] = filepath.Join(runtimeDir, "descriptor.json")
	result.Env["KILO_OMNIGENT_CODEX_TOKEN"] = o.CodexToken
	result.Env["KILO_OMNIGENT_CLAUDE_TOKEN"] = o.ClaudeToken

	return result, files, nil
}

func prepareOmnigentRuntime(o omnigentRuntimeOptions) (omnigentRuntime, error) {
	result, files, err := planOmnigentRuntime(o)
	if err != nil {
		return omnigentRuntime{}, err
	}
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, relative := range keys {
		path := filepath.Join(o.RootDir, filepath.FromSlash(relative))
		if err = safeEditorDir(o.RootDir, filepath.Dir(path)); err != nil {
			return omnigentRuntime{}, err
		}
		if err = atomicProfileFile(path, []byte(files[relative]), 0600); err != nil {
			return omnigentRuntime{}, fmt.Errorf("Could not prepare the private Omnigent runtime: %w", err)
		}
	}
	return result, nil
}
func validateOmnigentRuntime(o omnigentRuntimeOptions, saved omnigentRuntime) bool {
	expected, files, err := planOmnigentRuntime(o)
	if err != nil || !reflect.DeepEqual(expected, saved) {
		return false
	}
	for relative, data := range files {
		path := filepath.Join(o.RootDir, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return false
		}
		hash, err := omnigentFileHash(path)
		if err != nil || hash != omnigentHash([]byte(data)) {
			return false
		}
	}
	return true
}

// A unique, correlated switch in the stable compiled frontend. Native Claude
// retains its vendor ladder because this copy is patched only for Kilo Claude.
// The unconditional branch also handles cached rows whose schema drops markers.
var omnigentEffortSwitch = regexp.MustCompile("case[\"'`]claude-code-native-ui[\"'`]:return ([A-Za-z_$][A-Za-z0-9_$]*|\\[\"low\",\"medium\",\"high\",\"xhigh\",\"max\"\\]);case[\"'`]devin-native-ui[\"'`]:return ([A-Za-z_$][A-Za-z0-9_$]*)\\(([A-Za-z_$][A-Za-z0-9_$]*),([A-Za-z_$][A-Za-z0-9_$]*)\\);case[\"'`]codex-native-ui[\"'`]:return ([A-Za-z_$][A-Za-z0-9_$]*)\\(([A-Za-z_$][A-Za-z0-9_$]*),([A-Za-z_$][A-Za-z0-9_$]*)\\)")

func patchOmnigentWebEfforts(data string) (string, error) {
	matches := omnigentEffortSwitch.FindAllStringSubmatchIndex(data, -1)
	if len(matches) != 1 {
		return "", errors.New("This Omnigent web build does not support the private per-model reasoning selector.")
	}
	m := matches[0]
	part := func(i int) string { return data[m[2*i]:m[2*i+1]] }
	if part(2) != part(5) || part(3) != part(6) || part(4) != part(7) {
		return "", errors.New("The Omnigent reasoning selector contract changed.")
	}
	clause := `case"claude-code-native-ui":return ` + part(2) + `(` + part(3) + `,` + part(4) + `);case"devin-native-ui":return ` + part(2) + `(` + part(3) + `,` + part(4) + `);case"codex-native-ui":return ` + part(5) + `(` + part(6) + `,` + part(7) + `)`
	return data[:m[0]] + clause + data[m[1]:], nil
}
func planOmnigentWebFiles(source string, kiloClaude bool) (map[string]string, error) {
	root := filepath.Join(source, "server", "static", "web-ui")
	files := map[string]string{}
	var total int64
	patches := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Omnigent web assets must not be symbolic links.")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if !info.Mode().IsRegular() || info.Size() > 16<<20 || total > 64<<20 || len(files) >= 1024 {
			return errors.New("Invalid Omnigent web assets.")
		}
		data, err := omnigentReadRegular(path, 16<<20)
		if err != nil {
			return err
		}
		text := string(data)
		if kiloClaude && strings.HasSuffix(path, ".js") && omnigentEffortSwitch.Match(data) {
			text, err = patchOmnigentWebEfforts(text)
			if err != nil {
				return err
			}
			patches++
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files["runtime/web-ui/"+filepath.ToSlash(relative)] = text
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := files["runtime/web-ui/index.html"]; !ok || kiloClaude && patches != 1 {
		return nil, errors.New("Install the compatible Omnigent 0.18.0 web UI before preparing its private workspace.")
	}
	return files, nil
}
