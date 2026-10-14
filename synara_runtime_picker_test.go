package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func synaraRuntimePickerOfficialArchives(t *testing.T) []string {
	t.Helper()
	paths := os.Getenv("KILO_TEST_SYNARA_PICKER_ASARS")
	if paths == "" {
		if root := os.Getenv("KILO_TEST_SYNARA_PICKER_PACKAGES"); root != "" {
			paths = filepath.Join(root, "stable", "app.asar") + string(os.PathListSeparator) + filepath.Join(root, "beta", "app.asar")
		}
	}
	if paths == "" {
		t.Skip("official owned Synara runtime/client archives are opt-in")
	}
	return filepath.SplitList(paths)
}

// Exercise runtime3 readiness with complete official private client assets and
// six inert runtime files. No Electron executable, signing command, provider or
// original application module is run by this test.
func TestSynaraPrivateRuntimeOfficialWebReadiness(t *testing.T) {
	for _, original := range synaraRuntimePickerOfficialArchives(t) {
		t.Run(filepath.Base(filepath.Dir(original)), func(t *testing.T) {
			before, err := openDesignFileHash(original, 512<<20)
			if err != nil {
				t.Fatal(err)
			}
			base := t.TempDir()
			binary := filepath.Join(base, "Synara fixture.app")
			archivePath := filepath.Join(binary, "Contents", "Resources", "app.asar")
			stub := filepath.Join(binary, "Contents", "MacOS", "Synara fixture")
			for _, dir := range []string{filepath.Dir(archivePath), filepath.Dir(stub)} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Link(original, archivePath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stub, []byte("inert executable fixture; never run"), 0700); err != nil {
				t.Fatal(err)
			}
			plist := []byte(`<plist><dict><key>CFBundleExecutable</key><string>Synara fixture</string></dict></plist>`)
			if err := os.WriteFile(filepath.Join(binary, "Contents", "Info.plist"), plist, 0600); err != nil {
				t.Fatal(err)
			}
			options := synaraProfileTestOptions(t)
			options.RootDir = filepath.Join(base, "private")
			prefs := defaultSynaraPreferences()
			prefs.Codex, prefs.Claude, prefs.KiloCodex = false, false, false
			options.Preferences = &prefs
			library := modelLibrary{SchemaVersion: 1, DefaultModel: "anthropic/claude-opus-5.5", Models: []modelLibraryItem{
				{ID: "anthropic/claude-opus-5.5", ReasoningCustom: true, ReasoningLevels: []string{"low", "high"}, ReasoningEffort: "low"},
				{ID: "anthropic/claude-sonnet-5.5", ReasoningCustom: true, ReasoningLevels: []string{"medium", "high"}, ReasoningEffort: "high"},
				{ID: "anthropic/claude-fable-5.1", ReasoningCustom: true, ReasoningLevels: []string{}},
			}}
			routes, err := t3CodeBuildClaudeRoutes(library)
			if err != nil {
				t.Fatal(err)
			}
			options.Library = library
			options.Policy = &t3CodeModelPolicy{ClaudeLibrary: library, ClaudeRoutes: routes}
			web, err := prepareSynaraPrivateWeb(synaraRuntimePickerOptions(options), binary, "macos")
			if err != nil || web.Root == "" || len(web.Files) < 2000 {
				t.Fatal("complete official web preparation failed", err, len(web.Files))
			}
			packageSource, header, err := synaraReadASARSource(archivePath, "package.json", 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			_, mainEntry, err := synaraRuntimePackageData(packageSource)
			if err != nil {
				t.Fatal(err)
			}
			main, _, err := synaraReadASARSource(archivePath, mainEntry, synaraRuntimeSourceLimit)
			if err != nil {
				t.Fatal(err)
			}
			server, _, err := synaraReadASARSource(archivePath, "apps/server/dist/index.mjs", synaraRuntimeSourceLimit)
			if err != nil {
				t.Fatal(err)
			}
			serverData := []byte("export const inertRuntimeFixture = true;\n")
			saved := synaraPrivateRuntime{Revision: synaraPrivateRuntimeRevision, SourceApp: binary, SourceArchive: archivePath, SourceMainEntry: mainEntry, SourceMainSHA256: openDesignHash(main), SourceServerSHA256: openDesignHash(server), ArchiveHeaderSHA256: header, Files: map[string]string{}, Web: &web}
			saved.Root = filepath.Join(options.RootDir, "runtime", openDesignHash(synaraRuntimeIdentity(binary, header, options.RootDir, mainEntry, saved.SourceMainSHA256, saved.SourceServerSHA256, openDesignHash(serverData), web)))
			saved.Executable = filepath.Join(saved.Root, synaraRuntimeBundleName, "Contents", "MacOS", filepath.Base(stub))
			for _, name := range synaraRuntimeFileNames(filepath.Base(stub)) {
				data := []byte("inert owned runtime file: " + name)
				if name == synaraRuntimeServerName {
					data = serverData
				}
				path := filepath.Join(saved.Root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				saved.Files[name] = openDesignHash(data)
			}
			manifest, err := json.MarshalIndent(saved, "", "  ")
			if err != nil || len(manifest) <= 64<<10 || len(manifest) > synaraRuntimeManifestLimit {
				t.Fatal("complete runtime/client manifest did not exercise the old 64KiB boundary", err, len(manifest))
			}
			manifestPath := filepath.Join(saved.Root, synaraRuntimeManifestName)
			if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
				t.Fatal(err)
			}
			if !synaraPrivateRuntimeReady(options, binary, "macos", saved) {
				t.Fatal("runtime3 with complete official client manifest was not ready")
			}
			readback, err := readOpenDesignShimFile(manifestPath, synaraRuntimeManifestLimit)
			var reused synaraPrivateRuntime
			if err != nil || json.Unmarshal(readback, &reused) != nil || !synaraPrivateRuntimeReady(options, binary, "macos", reused) {
				t.Fatal("reading the same complete runtime manifest lost readiness", err)
			}
			if openDesignHash(synaraRuntimeIdentity(binary, header, options.RootDir, mainEntry, saved.SourceMainSHA256, saved.SourceServerSHA256, openDesignHash(serverData))) == filepath.Base(saved.Root) {
				t.Fatal("runtime identity did not bind private web policy/assets")
			}
			index := filepath.Join(web.Root, "index.html")
			indexData, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(index, append(append([]byte{}, indexData...), '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			if synaraPrivateRuntimeReady(options, binary, "macos", saved) {
				t.Fatal("changed private web bytes retained runtime readiness")
			}
			if err := os.WriteFile(index, indexData, 0600); err != nil {
				t.Fatal(err)
			}
			if !synaraPrivateRuntimeReady(options, binary, "macos", saved) {
				t.Fatal("restored exact private web bytes did not recover readiness")
			}
			after, err := openDesignFileHash(original, 512<<20)
			if err != nil || after != before {
				t.Fatal("official source archive changed")
			}
			t.Logf("complete official web/runtime readiness: assets=%d manifestBytes=%d sourceSHA256=%s", len(web.Files), len(manifest), before)
		})
	}
}

// Node's real fs and crypto enforce the source digest. The fake Module merely
// captures transformed bytes: the official desktop module is never evaluated.
func TestSynaraPrivateRuntimeOfficialBootstrapCapture(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for pure bootstrap capture")
	}
	for _, archive := range synaraRuntimePickerOfficialArchives(t) {
		t.Run(filepath.Base(filepath.Dir(archive)), func(t *testing.T) {
			metadata, _, err := synaraReadASARSource(archive, "package.json", 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			_, mainEntry, err := synaraRuntimePackageData(metadata)
			if err != nil {
				t.Fatal(err)
			}
			main, _, err := synaraReadASARSource(archive, mainEntry, synaraRuntimeSourceLimit)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			sourceRoot := filepath.Join(root, "extracted-own-source.asar")
			mainPath := filepath.Join(sourceRoot, filepath.FromSlash(mainEntry))
			if err := os.MkdirAll(filepath.Dir(mainPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mainPath, main, 0600); err != nil {
				t.Fatal(err)
			}
			hook, web := filepath.Join(root, "private", "backend-hook.mjs"), filepath.Join(root, "private", "web-root")
			bootstrap := synaraRuntimeBootstrap(sourceRoot, mainEntry, hook, openDesignHash(main), web)
			bootstrapPath := filepath.Join(root, "bootstrap.cjs")
			if err := os.WriteFile(bootstrapPath, bootstrap, 0600); err != nil {
				t.Fatal(err)
			}
			anchors, _ := json.Marshal([]string{synaraDesktopStaticRootAnchor, synaraDesktopSpawnAnchor})
			wrapper := `const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict'),path=require('node:path'),RealModule=require('node:module');
const [bootstrapPath,sourceRoot,mainEntry,hook,web,anchorsRaw]=process.argv.slice(2), anchors=JSON.parse(anchorsRaw),originalMain=path.join(sourceRoot,mainEntry),original=fs.readFileSync(originalMain,'utf8'),bootstrap=fs.readFileSync(bootstrapPath,'utf8');
let captured,app={getAppPath:()=> 'unmodified'};
function FakeModule(filename){this.filename=filename} FakeModule._nodeModulePaths=RealModule._nodeModulePaths;
FakeModule.prototype._compile=function(source,filename){assert.equal(captured,undefined);captured={source,filename}};
const scopedRequire=name=>name==='electron'?{app}:name==='node:module'?FakeModule:require(name);
function capture(){vm.runInNewContext(bootstrap,{require:scopedRequire,module:{},Buffer},{timeout:1000})}
capture();assert.ok(captured);assert.equal(captured.filename,originalMain);assert.equal(app.getAppPath(),sourceRoot);
const staticReplacement='function resolveServedStaticRoot() {\n\treturn {dir:'+JSON.stringify(web)+',snapshotted:true};\n}',spawnReplacement='const child = node_child_process.spawn(process.execPath, [...backendNodeArgs(), "--import", '+JSON.stringify(hook)+', backendEntry], {';
assert.equal(captured.source,original.replace(anchors[0],staticReplacement).replace(anchors[1],spawnReplacement));assert.equal(captured.source.includes(anchors[0]),false);assert.equal(captured.source.includes(anchors[1]),false);
fs.writeFileSync(originalMain,original+'\n// owned digest-negative control');captured=undefined;assert.throws(capture,/Synara desktop source changed/);assert.equal(captured,undefined);
console.log(JSON.stringify({capturedOnly:true,originalModuleEvaluated:false,staticRoot:web,hook,sourceRoot,digestNegativeRejected:true}));`
			wrapperPath := filepath.Join(root, "capture.cjs")
			if err := os.WriteFile(wrapperPath, []byte(wrapper), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, node, wrapperPath, bootstrapPath, sourceRoot, mainEntry, hook, web, string(anchors)).CombinedOutput()
			if err != nil || !strings.Contains(string(output), `"originalModuleEvaluated":false`) || !strings.Contains(string(output), `"digestNegativeRejected":true`) {
				t.Fatalf("pure official bootstrap capture failed: %v %s", err, output)
			}
			t.Log(strings.TrimSpace(string(output)))
		})
	}
}
