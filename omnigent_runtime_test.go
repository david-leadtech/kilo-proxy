package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// This is a package-layout fixture, not an installed Omnigent or Python. Its
// bytes are never executed. Pin replacement is scoped to one nonparallel test;
// the production inspector uses the official source hashes exclusively.
func syntheticOmnigentInstallation(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("The new Omnigent Python runtime does not support Windows launchers.")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	source := filepath.Join(root, "lib", "python3.12", "site-packages", "omnigent")
	for _, dir := range []string{bin, source, filepath.Join(filepath.Dir(source), "omnigent-0.18.0.dist-info"), filepath.Join(source, "server", "static", "web-ui", "assets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	python := filepath.Join(bin, "python3")
	omni := filepath.Join(bin, "omni")
	write := func(path, data string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(python, "synthetic interpreter; never execute\n", 0700)
	write(omni, "#!"+python+"\nfrom omnigent.cli import main\n", 0700)
	write(filepath.Join(filepath.Dir(source), "omnigent-0.18.0.dist-info", "METADATA"), "Name: omnigent\nVersion: 0.18.0\n", 0600)
	old := omnigentSourcePins
	omnigentSourcePins = map[string]string{"inference_config.py": omnigentHash([]byte("synthetic source contract\n"))}
	t.Cleanup(func() { omnigentSourcePins = old })
	write(filepath.Join(source, "inference_config.py"), "synthetic source contract\n", 0600)
	write(filepath.Join(source, "server", "static", "web-ui", "index.html"), "<html><script src='/assets/test.js'></script></html>\n", 0600)
	write(filepath.Join(source, "server", "static", "web-ui", "assets", "test.js"), "function UEe(e,t=[],n=null){switch(HEe(e)){case`claude-code-native-ui`:return zEe;case`devin-native-ui`:return Vi(t,n);case`codex-native-ui`:return Vi(t,n);case`pi-native-ui`:return BEe;default:return REe}}", 0600)
	return omni
}

func TestOmnigentRuntimeInspectorDoesNotExecuteAndRejectsChangedSource(t *testing.T) {
	binary := syntheticOmnigentInstallation(t)
	installation, err := inspectOmnigentInstallation(binary, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if installation.Version != "0.18.0" || installation.Fingerprint == "" || installation.PythonPath != filepath.Join(filepath.Dir(binary), "python3") {
		t.Fatalf("unexpected installation: %+v", installation)
	}
	if err := os.WriteFile(filepath.Join(installation.SourceRoot, "inference_config.py"), []byte("different source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectOmnigentInstallation(binary, runtime.GOOS); err == nil {
		t.Fatal("accepted changed package source")
	}
}

func TestOmnigentRuntimeInspectorAcceptsBoundedPythonExecutable(t *testing.T) {
	o := omnigentRuntimeTestOptions(t)
	python := filepath.Join(filepath.Dir(o.OmniPath), "python3")
	f, err := os.OpenFile(python, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(17 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	// The official standalone macOS Python is larger than the source-file
	// bound. This sparse executable fixture is inspected, never executed.
	installed, err := inspectOmnigentInstallation(o.OmniPath, o.Platform)
	if err != nil {
		t.Fatalf("legitimate interpreter over 16 MiB was rejected: %v", err)
	}
	prepared, err := prepareOmnigentRuntime(o)
	if err != nil || !validateOmnigentRuntime(o, prepared) {
		t.Fatalf("fresh runtime with bounded interpreter was invalid: %v", err)
	}
	f, err = os.OpenFile(python, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("changed interpreter contents"), 32)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("change fixture: %v / %v", err, closeErr)
	}
	changed, err := inspectOmnigentInstallation(o.OmniPath, o.Platform)
	if err != nil || changed.Fingerprint == installed.Fingerprint {
		t.Fatalf("interpreter contents were not bound to installation identity: %v", err)
	}
	if validateOmnigentRuntime(o, prepared) {
		t.Fatal("replacement interpreter left the old prepared runtime ready")
	}
	// Retain the executable limit and the smaller source/script/metadata
	// limits. Truncation does not allocate or execute a 512 MiB fixture.
	if err = os.Truncate(python, (512<<20)+1); err != nil {
		t.Fatal(err)
	}
	if _, err = inspectOmnigentInstallation(o.OmniPath, o.Platform); err == nil {
		t.Fatal("accepted an interpreter beyond the executable bound")
	}
	if err = os.Truncate(python, 17<<20); err != nil {
		t.Fatal(err)
	}
	for name, limit := range map[string]int64{o.OmniPath: 64 << 10, filepath.Join(filepath.Dir(installed.SourceRoot), "omnigent-0.18.0.dist-info", "METADATA"): 1 << 20, filepath.Join(installed.SourceRoot, "inference_config.py"): 16 << 20} {
		original, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Truncate(name, limit+1); err != nil {
			t.Fatal(err)
		}
		if _, err = inspectOmnigentInstallation(o.OmniPath, o.Platform); err == nil {
			t.Fatalf("accepted oversized non-executable installation file: %s", name)
		}
		if err = os.WriteFile(name, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOmnigentRuntimeWebPatchScopesClaudeAndRequiresCorrelatedContract(t *testing.T) {
	source := "function UEe(e,t=[],n=null){switch(HEe(e)){case`claude-code-native-ui`:return zEe;case`devin-native-ui`:return Vi(t,n);case`codex-native-ui`:return Vi(t,n);default:return REe}}"
	patched, err := patchOmnigentWebEfforts(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patched, `case"claude-code-native-ui":return Vi(t,n)`) || !strings.Contains(patched, `case"codex-native-ui":return Vi(t,n)`) {
		t.Fatal(patched)
	}
	for _, bad := range []string{source + source, strings.Replace(source, "return Vi(t,n);default", "return Different(t,n);default", 1), strings.Replace(source, "return Vi(t,n);default", "return Vi(t,other);default", 1), "/* unrelated code */"} {
		if _, err := patchOmnigentWebEfforts(bad); err == nil {
			t.Fatal("accepted an ambiguous/uncorrelated web contract")
		}
	}
}

func omnigentRuntimeTestOptions(t *testing.T) omnigentRuntimeOptions {
	t.Helper()
	omni := syntheticOmnigentInstallation(t)
	root := t.TempDir()
	normal := t.TempDir()
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("synthetic CLI; never execute"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "config", "config.yaml")
	inference := filepath.Join(root, "inference.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{config, inference} {
		if err := os.WriteFile(p, []byte("{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return omnigentRuntimeOptions{RootDir: root, OmniPath: omni, Platform: runtime.GOOS, NormalHome: normal, CodexPath: cli, ClaudePath: cli, ConfigPath: config, InferencePath: inference, CodexToken: "synthetic-codex", ClaudeToken: "synthetic-claude", Preferences: omnigentPreferences{Version: 1, Codex: true, Claude: true, CodexConnection: "kilo", ClaudeConnection: "kilo", CodexModels: "shared", ClaudeModels: "shared"}, CodexLibrary: modelLibrary{SchemaVersion: 1, DefaultModel: "openai/model-a", Models: []modelLibraryItem{{ID: "openai/model-a", ReasoningCustom: true, ReasoningLevels: []string{"high"}, ReasoningEffort: "high"}}}, ClaudeLibrary: modelLibrary{SchemaVersion: 1, DefaultModel: "anthropic/model-b", Models: []modelLibraryItem{{ID: "anthropic/model-b", ReasoningCustom: true, ReasoningLevels: []string{}}}}}
}

func TestOmnigentRuntimeIntegrityRecomputesExecutionAndFiles(t *testing.T) {
	o := omnigentRuntimeTestOptions(t)
	prepared, err := prepareOmnigentRuntime(o)
	if err != nil {
		t.Fatal(err)
	}
	if !validateOmnigentRuntime(o, prepared) {
		t.Fatal("fresh runtime not valid")
	}
	for _, mutate := range []func(*omnigentRuntime){func(r *omnigentRuntime) { r.PythonPath = "/other/python" }, func(r *omnigentRuntime) { r.Args = []string{"-c", "different command"} }, func(r *omnigentRuntime) { r.Env = map[string]string{"KILO_OMNIGENT_CODEX_TOKEN": "forged"} }, func(r *omnigentRuntime) { r.Files = map[string]string{} }} {
		changed := prepared
		mutate(&changed)
		if validateOmnigentRuntime(o, changed) {
			t.Fatal("accepted forged execution fields with preserved fingerprint")
		}
	}
	for _, path := range []string{"runtime/omnigent_runtime.py", "runtime/sitecustomize.py", "runtime/descriptor.json", "runtime/web-ui/assets/test.js"} {
		absolute := filepath.Join(o.RootDir, filepath.FromSlash(path))
		old, err := os.ReadFile(absolute)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(absolute, append(old, []byte("changed")...), 0600); err != nil {
			t.Fatal(err)
		}
		if validateOmnigentRuntime(o, prepared) {
			t.Fatal("accepted changed private runtime file", path)
		}
		if err = os.WriteFile(absolute, old, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOmnigentRuntimeIdentityTracksCredentialsBinaryAndActiveHomes(t *testing.T) {
	o := omnigentRuntimeTestOptions(t)
	before, err := omnigentRuntimeIdentity(o)
	if err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.CodexToken = "rotated"
	after, err := omnigentRuntimeIdentity(changed)
	if err != nil || before == after {
		t.Fatalf("credential rotation not detected: %v", err)
	}
	if err := os.WriteFile(o.CodexPath, []byte("same path, new contents"), 0700); err != nil {
		t.Fatal(err)
	}
	after, err = omnigentRuntimeIdentity(o)
	if err != nil || before == after {
		t.Fatalf("binary replacement not detected: %v", err)
	}
	changed = o
	changed.NormalEnvironment = map[string]string{"CODEX_HOME": filepath.Join(o.RootDir, "profiles", "codex-kilo")}
	if _, err = omnigentRuntimeIdentity(changed); err == nil {
		t.Fatal("accepted native/Kilo home overlap")
	}
	changed = o
	changed.Preferences.Claude = false
	changed.NormalEnvironment = map[string]string{"CLAUDE_CONFIG_DIR": "/does/not/need/to/exist"}
	if _, err = omnigentRuntimeIdentity(changed); err != nil {
		t.Fatal("disabled Claude home affected runtime", err)
	}
}

func TestOmnigentRuntimeOfficialCompiledWebContract(t *testing.T) {
	path := os.Getenv("KILO_TEST_OMNIGENT_WEB_BUNDLE")
	if path == "" {
		t.Skip("Opt-in official compiled frontend fixture is not attached.")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if omnigentHash(data) != "245fda5ebf680f14725cdaca5e319f85910cc18352a2c497fc007388a16a736e" {
		t.Fatal("unexpected official frontend bytes")
	}
	if _, err = patchOmnigentWebEfforts(string(data)); err != nil {
		t.Fatal(err)
	}
}
