package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func synaraPickerTestPolicy() synaraPickerPolicy {
	return synaraPickerPolicy{InstanceID: synaraClaudeProxyID, Models: []synaraPickerModel{
		{Slug: "claude-opus-5-5", NativeSlug: "claude-opus-5-5", GatewayID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", ReasoningLevels: []string{"low", "high"}, DefaultEffort: "low"},
		{Slug: "claude-sonnet-5-5", NativeSlug: "claude-sonnet-5-5", GatewayID: "anthropic/claude-sonnet-5.5", Name: "Claude Sonnet 5.5", ReasoningLevels: []string{"medium", "high"}, DefaultEffort: "high"},
		{Slug: "claude-fable-5-1", NativeSlug: "claude-fable-5-1", GatewayID: "anthropic/claude-fable-5.1", Name: "Claude Fable 5.1"},
	}}
}

func synaraPickerOpenCodeTestModels() []synaraPickerOpenCodeModel {
	return []synaraPickerOpenCodeModel{
		{Slug: "kilo-local/openai/gpt-selected", GatewayID: "openai/gpt-selected", Name: "Selected model", VariantLevels: []string{"low", "high"}, DefaultVariant: "low"},
		{Slug: "kilo-local/vendor/no-reasoning", GatewayID: "vendor/no-reasoning", Name: "No reasoning", VariantLevels: []string{}},
	}
}

func synaraPickerCombinedTestPolicy() synaraPickerPolicy {
	p := synaraPickerTestPolicy()
	p.OpenCodeModels = synaraPickerOpenCodeTestModels()
	return p
}

func synaraPickerTestSource(beta bool) []byte {
	variant := 0
	if beta {
		variant = 1
	}
	return []byte(synaraPickerHookAnchor + ";var " + synaraPickerSelectedInstanceAnchor + ";\n" +
		"function instance(){let r={},n={},u=[],d=false,c=[];if(" + synaraPickerInstanceAnchors[variant] + "){}return r}\n" +
		"var Ue=(0,A.useMemo)(()=>{" + synaraPickerProviderAnchors[variant] + "null;\n" +
		"function runtime(){let e={};" + synaraPickerRuntimeInstanceAnchor + "\n" +
		"var Q=(0,A.useMemo)(()=>({" + synaraPickerRuntimeProviderAnchor + ",opencode:R.data?.models??[],pi:z.data?.models??[]})," + synaraPickerRuntimeDependencies + "null;}\n")
}

func synaraComposerTestSource(beta bool) []byte {
	i := 0
	if beta {
		i = 1
	}
	return []byte(synaraComposerFunctionAnchors[i] + ",o=h({provider:t,model:n,runtimeModel:r}),s=null,c;switch(t){" + synaraComposerClaudeAnchors[i] + synaraComposerOpenCodeAnchors[i] + "}let f=null;" + synaraComposerResultAnchor + "};}")
}

// These are source contracts only; no desktop/provider code is run by this
// fixture. UI owners can merge them into their own ASAR while retaining its
// channel metadata and unrelated server/delegation fixtures.
func synaraInstalledPickerTestFiles() map[string][]byte {
	return map[string][]byte{
		"package.json":                       []byte(`{"name":"synara-desktop","version":"1.0.4","main":"apps/desktop/dist-electron/main.js","synaraDesktopFlavor":"production"}`),
		"apps/desktop/dist-electron/main.js": []byte(synaraDesktopStaticRootAnchor + "\n" + synaraDesktopSpawnAnchor + "\n});\n"),
		"apps/server/dist/client/index.html": []byte("<script type=module src=assets/useProviderModelCatalog-fixture.js></script>"),
		"apps/server/dist/client/assets/useProviderModelCatalog-fixture.js": synaraPickerTestSource(false),
		"apps/server/dist/client/assets/SidechatDockPane-fixture.js":        synaraComposerTestSource(false),
	}
}

func addSynaraInstalledPickerFixture(t *testing.T, binary, platform string) {
	t.Helper()
	archive, err := t3CodeOpenPackage(binary, platform)
	if err != nil {
		t.Fatal(err)
	}
	path := archive.file.Name()
	files := map[string][]byte{}
	var copyEntries func(t3CodeASAREntry, string)
	copyEntries = func(entry t3CodeASAREntry, prefix string) {
		for name, child := range entry.Files {
			name = prefix + name
			if child.Files != nil {
				copyEntries(child, name+"/")
				continue
			}
			if child.Size == 0 {
				files[name] = []byte{}
				continue
			}
			data, err := archive.read(name, synaraRuntimeSourceLimit)
			if err != nil {
				archive.file.Close()
				t.Fatal(err)
			}
			files[name] = data
		}
	}
	copyEntries(archive.tree, "")
	archive.file.Close()
	var metadata map[string]any
	if err := json.Unmarshal(files["package.json"], &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["main"] = "apps/desktop/dist-electron/main.js"
	files["package.json"], err = json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range synaraInstalledPickerTestFiles() {
		if name != "package.json" {
			files[name] = data
		}
	}
	writeT3CodeFixtureASAR(t, path, files)
}

func TestSynaraPickerInstalledUIFixturePreservesMetadataAndServer(t *testing.T) {
	for _, withServer := range []bool{false, true} {
		base := t.TempDir()
		binary := filepath.Join(base, "Synara.app")
		path := filepath.Join(binary, "Contents", "Resources", "app.asar")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{"package.json": []byte(`{"name":"synara-desktop-beta","version":"1.0.2-beta.1","synaraDesktopFlavor":"beta"}`)}
		if withServer {
			files["apps/server/dist/index.mjs"] = []byte("// existing delegation and optional provider fixture\nexport {};\n")
		}
		writeT3CodeFixtureASAR(t, path, files)
		addSynaraInstalledPickerFixture(t, binary, "macos")
		archive, err := t3CodeOpenPackage(binary, "macos")
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := archive.read("package.json", 64<<10)
		if err != nil {
			archive.file.Close()
			t.Fatal(err)
		}
		var data synaraPackageInfo
		if json.Unmarshal(metadata, &data) != nil || data.Name != "synara-desktop-beta" || data.Version != "1.0.2-beta.1" || data.SynaraDesktopFlavor != "beta" || !synaraInstalledPickerSupported(archive) {
			archive.file.Close()
			t.Fatal("UI picker fixture changed channel/version or lacks verified contracts")
		}
		server, err := archive.read("apps/server/dist/index.mjs", synaraRuntimeSourceLimit)
		archive.file.Close()
		if withServer && (err != nil || string(server) != string(files["apps/server/dist/index.mjs"])) || !withServer && err == nil {
			t.Fatal("UI picker fixture changed the existing server entries")
		}
		providers, second, kilo := synaraOptionalProviderCapabilities(string(server))
		if len(providers) != 0 || second || kilo {
			t.Fatal("minimal UI server advertises optional capabilities")
		}
	}
}

func synaraPickerFixtureArchive(t *testing.T, files map[string][]byte) *t3CodeArchive {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.asar")
	writeT3CodeFixtureASAR(t, path, files)
	archive, err := t3CodeOpenASAR(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { archive.file.Close() })
	return archive
}

func TestSynaraPickerInstalledCapabilityRequiresLiveCompleteContracts(t *testing.T) {
	files := synaraInstalledPickerTestFiles()
	if !synaraInstalledPickerSupported(synaraPickerFixtureArchive(t, files)) {
		t.Fatal("complete installed picker was not recognized")
	}
	for name, change := range map[string]func(map[string][]byte){
		"package-only": func(files map[string][]byte) {
			delete(files, "apps/desktop/dist-electron/main.js")
			delete(files, "apps/server/dist/client/assets/useProviderModelCatalog-fixture.js")
		},
		"commented-main": func(files map[string][]byte) {
			files["apps/desktop/dist-electron/main.js"] = append(append([]byte("/*"), files["apps/desktop/dist-electron/main.js"]...), []byte("*/")...)
		},
		"duplicate-main": func(files map[string][]byte) {
			files["apps/desktop/dist-electron/main.js"] = append(files["apps/desktop/dist-electron/main.js"], []byte(synaraDesktopStaticRootAnchor)...)
		},
		"changed-spawn": func(files map[string][]byte) {
			files["apps/desktop/dist-electron/main.js"] = []byte(strings.Replace(string(files["apps/desktop/dist-electron/main.js"]), "backendEntry", "otherEntry", 1))
		},
		"duplicate-picker": func(files map[string][]byte) {
			files["apps/server/dist/client/assets/useProviderModelCatalog-other.js"] = synaraPickerTestSource(false)
		},
		"partial-picker": func(files map[string][]byte) {
			files["apps/server/dist/client/assets/useProviderModelCatalog-fixture.js"] = []byte(strings.Replace(string(synaraPickerTestSource(false)), synaraPickerRuntimeInstanceAnchor, "", 1))
		},
		"commented-picker": func(files map[string][]byte) {
			files["apps/server/dist/client/assets/useProviderModelCatalog-fixture.js"] = []byte("/*" + string(synaraPickerTestSource(false)) + "*/")
		},
		"missing-composer": func(files map[string][]byte) {
			delete(files, "apps/server/dist/client/assets/SidechatDockPane-fixture.js")
		},
		"duplicate-composer": func(files map[string][]byte) {
			files["apps/server/dist/client/assets/SidechatDockPane-other.js"] = synaraComposerTestSource(false)
		},
		"partial-composer": func(files map[string][]byte) {
			files["apps/server/dist/client/assets/SidechatDockPane-fixture.js"] = []byte(strings.Replace(string(synaraComposerTestSource(false)), synaraComposerResultAnchor, "", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := synaraInstalledPickerTestFiles()
			change(files)
			if synaraInstalledPickerSupported(synaraPickerFixtureArchive(t, files)) {
				t.Fatal("changed, duplicate, partial or non-live picker advertised support")
			}
		})
	}
	if synaraInstalledPickerSupported(nil) {
		t.Fatal("nil installation advertised picker support")
	}
}

func TestSynaraPickerNativeDefaultsRequireImportedLiveSharedLiteral(t *testing.T) {
	const literal = `const DEFAULT_MODEL_BY_PROVIDER = {codex: "gpt-6-astra", claudeAgent: "claude-sonnet-5", cursor: "auto", devin: "adaptive", antigravity: "Gemini 3.5 Flash", grok: "grok-4.6", droid: "claude-opus-4-8", opencode: "openai/gpt-5"};`
	fixture := func() map[string][]byte {
		return map[string][]byte{
			"apps/server/dist/index.mjs":                   []byte(`import { is as DEFAULT_MODEL_BY_PROVIDER } from "./MigrationBackup-fixture.mjs";` + "\n" + synaraNativeDefaultsConsumer),
			"apps/server/dist/MigrationBackup-fixture.mjs": []byte(literal + "\n" + `export { DEFAULT_MODEL_BY_PROVIDER as is };`),
		}
	}
	defaults := synaraInstalledNativeDefaults(synaraPickerFixtureArchive(t, fixture()))
	if len(defaults) != 8 || defaults["codex"] != "gpt-6-astra" || defaults["claudeAgent"] != "claude-sonnet-5" || defaults["pi"] != "" {
		t.Fatalf("incorrect imported native defaults: %v", defaults)
	}
	for name, change := range map[string]func(map[string][]byte){
		"wrong-module": func(files map[string][]byte) {
			files["apps/server/dist/index.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/index.mjs"]), "MigrationBackup-fixture", "MigrationBackup-other", 1))
		},
		"wrong-export-alias": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/MigrationBackup-fixture.mjs"]), "as is", "as other", 1))
		},
		"commented-import": func(files map[string][]byte) {
			files["apps/server/dist/index.mjs"] = []byte("/*" + string(files["apps/server/dist/index.mjs"]) + "*/")
		},
		"commented-import-binding": func(files map[string][]byte) {
			files["apps/server/dist/index.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/index.mjs"]), "{ is as DEFAULT_MODEL_BY_PROVIDER }", "{ unused as OTHER, /*, is as DEFAULT_MODEL_BY_PROVIDER,*/ }", 1) + "\nconst DEFAULT_MODEL_BY_PROVIDER={codex:\"different-local-model\"};")
		},
		"commented-export-binding": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/MigrationBackup-fixture.mjs"]), "{ DEFAULT_MODEL_BY_PROVIDER as is }", "{ unused as OTHER, /*, DEFAULT_MODEL_BY_PROVIDER as is,*/ }", 1))
		},
		"commented-chunk": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte("/*" + string(files["apps/server/dist/MigrationBackup-fixture.mjs"]) + "*/")
		},
		"duplicate-declaration": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = append(files["apps/server/dist/MigrationBackup-fixture.mjs"], []byte(literal)...)
		},
		"executable-value": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/MigrationBackup-fixture.mjs"]), `"gpt-6-astra"`, `readPrivateAccount()`, 1))
		},
		"duplicate-key": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte(strings.Replace(string(files["apps/server/dist/MigrationBackup-fixture.mjs"]), `codex: "gpt-6-astra",`, `codex: "gpt-6-astra", codex: "other",`, 1))
		},
		"truncated-body": func(files map[string][]byte) {
			files["apps/server/dist/MigrationBackup-fixture.mjs"] = []byte(literal[:len(literal)-1])
		},
	} {
		t.Run(name, func(t *testing.T) {
			files := fixture()
			change(files)
			if got := synaraInstalledNativeDefaults(synaraPickerFixtureArchive(t, files)); got != nil {
				t.Fatalf("unverified native defaults accepted: %v", got)
			}
		})
	}
}

func TestSynaraPickerContractsPreserveNativeAndRejectUnknown(t *testing.T) {
	for _, beta := range []bool{false, true} {
		source := synaraPickerTestSource(beta)
		patched, err := patchSynaraPickerSource(source, synaraPickerTestPolicy())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(patched), "r[n.instanceId]=__kiloSynaraClaudeOptions(n.instanceId,r[n.instanceId])") || !strings.Contains(string(patched), ",Be,t,n]),We=") || !strings.Contains(string(patched), "Y.data?.models,t,n]),Ge=") {
			t.Fatal("account-scoped filtering or memo account dependencies missing")
		}
		for _, bad := range []string{string(source) + string(source), "/*" + string(source) + "*/", "`" + string(source) + "`", strings.Replace(string(source), "dynamicModels:u", "dynamicModels:changed", 1), strings.Replace(string(source), synaraPickerRuntimeInstanceAnchor, "", 1), strings.Replace(string(source), "selectedProviderInstanceId:n", "selectedProviderInstanceId:other", 1), string(patched)} {
			if _, err := patchSynaraPickerSource([]byte(bad), synaraPickerTestPolicy()); err == nil {
				t.Fatal("accepted changed, partial, duplicate, quoted or already patched contract")
			}
		}
		plain, err := patchSynaraPickerSource(source, synaraPickerPolicy{})
		if err != nil || string(plain) != string(source) {
			t.Fatal("legacy/native picker source changed")
		}
	}
}

func TestSynaraPickerComposerContractsPreserveNativeAndRejectUnknown(t *testing.T) {
	for _, beta := range []bool{false, true} {
		source := synaraComposerTestSource(beta)
		patched, err := patchSynaraComposerSource(source, synaraPickerTestPolicy())
		if err != nil || !strings.Contains(string(patched), "__kiloSynaraClaudeDispatch(r,e,") || !strings.Contains(string(patched), "promptEffort:__kiloSynaraClaudePrompt(r,f,s)") {
			t.Fatal("missing account-scoped composer preservation", err)
		}
		for _, bad := range []string{string(source) + string(source), "/*" + string(source) + "*/", "`" + string(source) + "`", strings.Replace(string(source), synaraComposerResultAnchor, "", 1), strings.Replace(string(source), "runtimeModel:r", "runtimeModel:other", 1), string(patched)} {
			if _, err := patchSynaraComposerSource([]byte(bad), synaraPickerTestPolicy()); err == nil {
				t.Fatal("unknown, commented, duplicate or partial composer accepted")
			}
		}
		plain, err := patchSynaraComposerSource(source, synaraPickerPolicy{})
		if err != nil || string(plain) != string(source) {
			t.Fatal("native composer source changed")
		}
	}
}

func TestSynaraPickerPolicyRequiresExactGatewayIdentity(t *testing.T) {
	policy := synaraPickerTestPolicy()
	if err := validateSynaraPickerPolicy(policy); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*synaraPickerPolicy){
		func(p *synaraPickerPolicy) { p.InstanceID = synaraClaudeNormalID },
		func(p *synaraPickerPolicy) { p.Models[0].GatewayID = "anthropic/claude-sonnet-5.5" },
		func(p *synaraPickerPolicy) { p.Models[0].DefaultEffort = "xhigh" },
		func(p *synaraPickerPolicy) { p.Models[0].ReasoningLevels = []string{"low", "low"} },
		func(p *synaraPickerPolicy) { p.Models[0].ReasoningLevels = []string{"invented"} },
		func(p *synaraPickerPolicy) { p.Models[1].Slug = p.Models[0].Slug },
		func(p *synaraPickerPolicy) { p.Models[0].Name = "bad\nname" },
	} {
		data, _ := json.Marshal(policy)
		var copy synaraPickerPolicy
		_ = json.Unmarshal(data, &copy)
		change(&copy)
		if validateSynaraPickerPolicy(copy) == nil {
			t.Fatal("accepted an invalid picker policy")
		}
	}
}

func TestSynaraPickerOpenCodePolicyUsesGeneralLibraryBounds(t *testing.T) {
	policy := synaraPickerPolicy{OpenCodeModels: synaraPickerOpenCodeTestModels()}
	if !synaraPrivateWebPolicyEnabled(policy) || validateSynaraPickerPolicy(policy) != nil {
		t.Fatal("OpenCode-only policy is unavailable")
	}
	for _, change := range []func(*synaraPickerPolicy){
		func(p *synaraPickerPolicy) { p.OpenCodeModels[0].Slug = "openai/gpt-selected" },
		func(p *synaraPickerPolicy) { p.OpenCodeModels[0].GatewayID = "other/route" },
		func(p *synaraPickerPolicy) { p.OpenCodeModels[0].VariantLevels = []string{"invented"} },
		func(p *synaraPickerPolicy) { p.OpenCodeModels[0].DefaultVariant = "max" },
		func(p *synaraPickerPolicy) { p.OpenCodeModels[1] = p.OpenCodeModels[0] },
	} {
		p := synaraPickerPolicy{OpenCodeModels: synaraPickerOpenCodeTestModels()}
		change(&p)
		if validateSynaraPickerPolicy(p) == nil {
			t.Fatal("invalid exact OpenCode model/variant accepted")
		}
	}
	policy.OpenCodeModels = []synaraPickerOpenCodeModel{}
	for i := 0; i < 50; i++ {
		id := "vendor/model-" + strconv.Itoa(i)
		policy.OpenCodeModels = append(policy.OpenCodeModels, synaraPickerOpenCodeModel{Slug: "kilo-local/" + id, GatewayID: id, Name: id, VariantLevels: []string{}})
	}
	if err := validateSynaraPickerPolicy(policy); err != nil {
		t.Fatal("artificial 32-model OpenCode limit introduced", err)
	}
	policy.OpenCodeModels = append(policy.OpenCodeModels, synaraPickerOpenCodeModel{Slug: "kilo-local/vendor/too-many", GatewayID: "vendor/too-many", Name: "too many"})
	if validateSynaraPickerPolicy(policy) == nil {
		t.Fatal("general model-library bound ignored")
	}
}

func TestSynaraPickerInstalledCapabilityScopesOpenCodeContracts(t *testing.T) {
	files := synaraInstalledPickerTestFiles()
	if !synaraInstalledOpenCodePickerSupported(synaraPickerFixtureArchive(t, files)) {
		t.Fatal("complete OpenCode contracts not supported")
	}
	files["apps/server/dist/client/assets/SidechatDockPane-fixture.js"] = []byte(strings.Replace(string(files["apps/server/dist/client/assets/SidechatDockPane-fixture.js"]), synaraComposerOpenCodeAnchors[0], "case`opencode`:{break}", 1))
	archive := synaraPickerFixtureArchive(t, files)
	if synaraInstalledOpenCodePickerSupported(archive) || !synaraInstalledPickerSupported(archive) {
		t.Fatal("OpenCode clause incorrectly affected Claude-only compatibility")
	}
}

func TestSynaraPickerOpenCodeDropsStickyHintsWithoutChangingNativeOrHistory(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	policy := synaraPickerCombinedTestPolicy()
	script := synaraPickerHelpers(policy) + synaraComposerHelpers() + synaraComposerOpenCodeHelpers() + `
const assert=require('node:assert/strict');
const models=[{slug:'openai/old-native',isCustom:true},{slug:'kilo-local/unselected/old-chat',isCustom:true},{slug:'kilo-local/openai/gpt-selected',name:'Old',optionDescriptors:[{id:'variant',type:'select',options:[{id:'evil'}]},{id:'agent',type:'select',options:[{id:'build'}]}]}];
const previous=JSON.stringify(models), selectedChat='kilo-local/unselected/old-chat';
assert.equal(__kiloSynaraClaudeOptions('opencode',models),models);
assert.equal(__kiloSynaraClaudeRuntime('opencode',models),models);
const projected=__kiloSynaraClaudeOptions('kilo_opencode_proxy',models);
assert.deepEqual(projected.map(x=>x.slug),['kilo-local/openai/gpt-selected','kilo-local/vendor/no-reasoning']);
assert.equal(selectedChat,'kilo-local/unselected/old-chat');assert.equal(JSON.stringify(models),previous);
const runtime=__kiloSynaraClaudeRuntime('kilo_opencode_proxy',models);
assert.deepEqual(runtime[0].kiloSynaraVariantLevels,['low','high']);
assert.equal(runtime[0].kiloSynaraDefaultVariant,'low');
assert.deepEqual(runtime[0].optionDescriptors.find(x=>x.id==='variant').options.map(x=>x.id),['low','high']);
assert.deepEqual(runtime[0].optionDescriptors.find(x=>x.id==='agent').options,[{id:'build'}]);
for(const variant of ['low','high']) assert.deepEqual(__kiloSynaraOpenCodeDispatch(runtime[0],{variant},{agent:'build'}),{agent:'build',variant});
assert.deepEqual(__kiloSynaraOpenCodeDispatch(runtime[1],{variant:'high'},{agent:'build',variant:'high'}),{agent:'build'});
assert.equal(__kiloSynaraOpenCodePrompt(runtime[1],'high','high'),null);
const native={variant:'high',agent:'build'};
assert.equal(__kiloSynaraOpenCodeDispatch({kiloSynaraInstanceId:'opencode'},{variant:'high'},native),native);
assert.equal(__kiloSynaraOpenCodePrompt({},'high','high'),'high');
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("OpenCode exact picker: %v %s", err, output)
	}
	for _, beta := range []bool{false, true} {
		if patched, err := patchSynaraPickerSource(synaraPickerTestSource(beta), policy); err != nil || !strings.Contains(string(patched), "t.opencode=__kiloSynaraClaudeOptions") || !strings.Contains(string(patched), "opencode:__kiloSynaraClaudeRuntime") {
			t.Fatal("final OpenCode provider/instance projections absent", err)
		}
		if _, err := patchSynaraComposerSource(synaraComposerTestSource(beta), policy); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSynaraPickerJavaScriptOnlyChangesPreparedClaudeAccount(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is not installed")
	}
	script := synaraPickerHelpers(synaraPickerTestPolicy()) + synaraComposerHelpers() + `
const assert=require('node:assert/strict');
const original=[{slug:'claude-haiku-4-5',name:'Haiku',optionDescriptors:[{id:'effort',options:[{id:'max'}]}]},{slug:'claude-opus-5-5',name:'Old',optionDescriptors:[{id:'effort',options:[{id:'max'}]},{id:'thinking',type:'boolean'}]}];
const before=JSON.stringify(original);
assert.equal(__kiloSynaraClaudeOptions('kilo_claude_normal',original),original);
assert.equal(__kiloSynaraClaudeOptions('claudeAgent',original),original);
assert.equal(__kiloSynaraClaudeRuntime('kilo_claude_normal',original),original);
assert.equal(__kiloSynaraClaudeRuntime('kilo_codex_proxy',original),original);
assert.equal(__kiloSynaraClaudeOptions('kilo_opencode_proxy',original),original);
assert.equal(__kiloSynaraClaudeRuntime('kilo_opencode_proxy',original),original);
const picker=__kiloSynaraClaudeOptions('kilo_claude_proxy',original);
assert.deepEqual(picker.map(x=>x.slug),['claude-opus-5-5','claude-sonnet-5-5','claude-fable-5-1']);
const runtime=__kiloSynaraClaudeRuntime('kilo_claude_proxy',original);
assert.deepEqual(runtime[0].supportedReasoningEfforts.map(x=>x.value),['low','high']);
assert.equal(runtime[0].defaultReasoningEffort,'low');
assert.deepEqual(runtime[0].optionDescriptors.find(x=>x.id==='effort').options.map(x=>x.id),['low','high']);
assert.equal(runtime[0].optionDescriptors.some(x=>x.id==='thinking'),true);
assert.deepEqual(runtime[2].supportedReasoningEfforts,[]);
assert.equal(Object.hasOwn(runtime[2],'defaultReasoningEffort'),false);
assert.deepEqual(runtime[2].optionDescriptors.find(x=>x.id==='effort').options,[]);
assert.equal(runtime[0].kiloSynaraInstanceId,'kilo_claude_proxy');
for(const effort of ['low','high']){
 const chosen=__kiloSynaraClaudeDispatch(runtime[0],{effort},{thinking:false});
 assert.deepEqual(chosen,{thinking:false,effort});
 assert.equal(__kiloSynaraClaudePrompt(runtime[0],'high',effort),effort);
}
assert.equal(__kiloSynaraClaudeDispatch(runtime[0],undefined,undefined).effort,'low');
assert.equal(__kiloSynaraClaudeDispatch(runtime[0],{effort:'max'},undefined).effort,'low');
assert.equal(__kiloSynaraClaudeDispatch(runtime[2],{effort:'high'},undefined),undefined);
assert.equal(__kiloSynaraClaudePrompt(runtime[2],'high','high'),null);
const nativeOptions={thinking:false};
assert.equal(__kiloSynaraClaudeDispatch({slug:'claude-opus-5-5'}, {effort:'high'},nativeOptions),nativeOptions);
assert.equal(__kiloSynaraClaudePrompt({kiloSynaraInstanceId:'kilo_claude_normal'},'high','low'),'high');
assert.equal(JSON.stringify(original),before);
`
	if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("private picker helper: %v %s", err, output)
	}
}

func synaraPrivateWebFixture(t *testing.T) (synaraPrivateWebOptions, string) {
	t.Helper()
	base := t.TempDir()
	binary := filepath.Join(base, "Synara.app")
	archive := filepath.Join(binary, "Contents", "Resources", "app.asar")
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	files := synaraInstalledPickerTestFiles()
	files["apps/server/dist/client/assets/unchanged.css"] = []byte("body{}")
	writeT3CodeFixtureASAR(t, archive, files)
	return synaraPrivateWebOptions{RootDir: filepath.Join(base, "private"), Policy: synaraPickerTestPolicy()}, binary
}

func TestSynaraPrivateWebImmutableCacheReadinessAndIsolation(t *testing.T) {
	options, binary := synaraPrivateWebFixture(t)
	saved, err := prepareSynaraPrivateWeb(options, binary, "macos")
	if err != nil {
		t.Fatal(err)
	}
	if !synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("new private client is not ready")
	}
	reused, err := prepareSynaraPrivateWeb(options, binary, "macos")
	if err != nil || !reflect.DeepEqual(saved, reused) {
		t.Fatal("unchanged private client not reused")
	}
	path := filepath.Join(saved.Root, filepath.FromSlash(saved.PickerEntry))
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("changed private JS remains ready")
	}
	if _, err = prepareSynaraPrivateWeb(options, binary, "macos"); err == nil {
		t.Fatal("changed private cache silently overwritten")
	}
	if data, _ := os.ReadFile(path); string(data) != "changed" {
		t.Fatal("failed preparation modified prior cache")
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(saved.Root, "unexpected.js")
	if err = os.WriteFile(extra, []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("unexpected private asset accepted")
	}
	_ = os.Remove(extra)
	for _, name := range []string{"unexpected-empty", "assets/extra/" + strings.Repeat("deep/", 35)} {
		directory := filepath.Join(saved.Root, filepath.FromSlash(name))
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if synaraPrivateWebFilesReady(saved) {
			t.Fatal("unexpected empty or deep directories accepted")
		}
		remove := filepath.Join(saved.Root, "unexpected-empty")
		if strings.HasPrefix(name, "assets/") {
			remove = filepath.Join(saved.Root, "assets", "extra")
		}
		if err := os.RemoveAll(remove); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(saved.Root, "index.html"), path); err != nil {
		t.Fatal(err)
	}
	if synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("linked private asset accepted")
	}
	_ = os.Remove(path)
	_ = os.WriteFile(path, original, 0600)
	changed := options
	changed.Policy = synaraPickerTestPolicy()
	changed.Policy.Models[0].DefaultEffort = "high"
	if synaraPrivateWebReady(changed, binary, "macos", saved) {
		t.Fatal("old policy remains ready")
	}
	next, err := prepareSynaraPrivateWeb(changed, binary, "macos")
	if err != nil || next.Root == saved.Root {
		t.Fatal("changed selection did not get separate immutable cache")
	}
	if !synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("old immutable cache was damaged")
	}
}

func TestSynaraPrivateWebRejectsChangedSourceWithoutWriting(t *testing.T) {
	options, binary := synaraPrivateWebFixture(t)
	options.Policy.Models[0].GatewayID = "anthropic/claude-sonnet-5.5"
	if _, err := prepareSynaraPrivateWeb(options, binary, "macos"); err == nil {
		t.Fatal("invalid route accepted")
	}
	if _, err := os.Stat(options.RootDir); !os.IsNotExist(err) {
		t.Fatal("invalid plan wrote private data")
	}
	options.Policy = synaraPickerTestPolicy()
	if _, err := prepareSynaraPrivateWeb(options, binary, "linux"); err == nil {
		t.Fatal("unqualified platform silently accepted")
	}
	if _, err := os.Stat(options.RootDir); !os.IsNotExist(err) {
		t.Fatal("unsupported plan wrote private data")
	}
}

func TestSynaraPrivateWebOpenCodeOnlyPreparation(t *testing.T) {
	options, binary := synaraPrivateWebFixture(t)
	options.Policy = synaraPickerPolicy{OpenCodeModels: synaraPickerOpenCodeTestModels()}
	saved, err := prepareSynaraPrivateWeb(options, binary, "macos")
	if err != nil || saved.Root == "" || !synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("OpenCode-only web was not prepared", err)
	}
	if saved.Revision != 3 || saved.ComposerEntry == "" || saved.PickerEntry == "" {
		t.Fatal("OpenCode picker/composer are not bound")
	}
	options.Policy.OpenCodeModels[0].DefaultVariant = "high"
	if synaraPrivateWebReady(options, binary, "macos", saved) {
		t.Fatal("changed OpenCode default retained old readiness")
	}
}

// Opt-in reads downloaded official package sources. It parses and patches the
// module and runs Node's syntax checker only; no Synara imports/app are run.
func TestSynaraPickerOfficialCompiledClientContracts(t *testing.T) {
	root := os.Getenv("KILO_TEST_SYNARA_PICKER_PACKAGES")
	if root == "" {
		t.Skip("official package source acceptance is opt-in")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"stable", "beta"} {
		t.Run(channel, func(t *testing.T) {
			base := filepath.Join(root, channel, "sources", "apps", "server", "dist", "client", "assets")
			paths, err := filepath.Glob(filepath.Join(base, "useProviderModelCatalog-*.js"))
			if err != nil || len(paths) != 1 {
				t.Fatal("missing unique official picker source")
			}
			source, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			patched, err := patchSynaraPickerSource(source, synaraPickerCombinedTestPolicy())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "picker.mjs")
			if err = os.WriteFile(path, patched, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("patched official client syntax: %v %s", err, output)
			}
			composers, err := filepath.Glob(filepath.Join(base, "SidechatDockPane-*.js"))
			if err != nil || len(composers) != 1 {
				t.Fatal("missing unique official composer source")
			}
			composer, err := os.ReadFile(composers[0])
			if err != nil {
				t.Fatal(err)
			}
			patched, err = patchSynaraComposerSource(composer, synaraPickerCombinedTestPolicy())
			if err != nil {
				t.Fatal(err)
			}
			path = filepath.Join(t.TempDir(), "composer.mjs")
			if err := os.WriteFile(path, patched, 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
				t.Fatalf("patched official composer syntax: %v %s", err, output)
			}

		})
	}
}

func synaraPickerCompiledFunction(t *testing.T, source, name string) string {
	t.Helper()
	prefix := "function " + name + "("
	if strings.Count(source, prefix) != 1 {
		t.Fatal("missing unique compiled pure function", name)
	}
	start := strings.Index(source, prefix)
	open := strings.Index(source[start:], "{") + start
	body, ok := t3CodeContractBody(source, open)
	if !ok {
		t.Fatal("invalid compiled pure function", name)
	}
	return source[start : open+len(body)]
}

// Execute extracted pure functions only. Importing the application bundles
// would run React/desktop code and is deliberately outside this proof.
func TestSynaraPickerOfficialComposerPreservesSelectedEffort(t *testing.T) {
	root := os.Getenv("KILO_TEST_SYNARA_PICKER_PACKAGES")
	if root == "" {
		t.Skip("official compiled composer proof is opt-in")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"stable", "beta"} {
		t.Run(channel, func(t *testing.T) {
			assets := filepath.Join(root, channel, "sources/apps/server/dist/client/assets")
			read := func(pattern string) string {
				paths, err := filepath.Glob(filepath.Join(assets, pattern))
				if err != nil || len(paths) != 1 {
					t.Fatal("missing unique compiled utility", pattern)
				}
				data, err := os.ReadFile(paths[0])
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			composer, picker, pending, settings := read("SidechatDockPane-*.js"), read("useProviderModelCatalog-*.js"), read("pendingInteractions-*.js"), read("appSettings-*.js")
			patched, err := patchSynaraComposerSource([]byte(composer), synaraPickerCombinedTestPolicy())
			if err != nil {
				t.Fatal(err)
			}
			name, buildName, metadataName, aliases := "q1", "Qt", "N", "const h=capsFor,ti=trim,_i=defaultEffort,di=hasEffort,ci=()=>false,bi=normalize,ei=normalizeOpenCode,ri=resolveVariant;"
			mergeName, mergeAliases := "Wt", "const zt=(_provider,id)=>id.trim(),Rt=({slug})=>slug,It=name=>name;"
			if channel == "beta" {
				name, buildName, metadataName, aliases = "VQ", "Zt", "P", "const h=capsFor,Zr=trim,pi=defaultEffort,si=hasEffort,ii=()=>false,_i=normalize,Xr=normalizeOpenCode,$r=resolveVariant;"
				mergeName, mergeAliases = "Ut", "const Rt=(_provider,id)=>id.trim(),Lt=({slug})=>slug,Ft=name=>name;"
			}
			functions := func(source string, names ...string) string {
				result := ""
				for _, n := range names {
					result += synaraPickerCompiledFunction(t, source, n) + "\n"
				}
				return result
			}
			script := synaraPickerHelpers(synaraPickerCombinedTestPolicy()) + synaraComposerHelpers() + synaraComposerOpenCodeHelpers() + `
const assert=require('node:assert/strict');
const staticCaps={reasoningEffortLevels:[{value:'low'},{value:'high',isDefault:true}],variantOptions:[],promptInjectedEffortLevels:[],contextWindowOptions:[],autoCompactWindowOptions:[],supportsFastMode:true,supportsThinkingToggle:true};
const trim=x=>typeof x==='string'&&x.trim()||null;
const defaultEffort=c=>c.reasoningEffortLevels.find(x=>x.isDefault)?.value??null;
const hasEffort=(c,v)=>c.reasoningEffortLevels.some(x=>x.value===v);
const resolveVariant=(rows,value)=>rows?.some(x=>x.value===value)?value:rows?.find(x=>x.isDefault)?.value??null;
const normalize=(()=>{const F=()=>staticCaps;
` + functions(pending, "C", "S", "V", "fe", "de", "H") + `return H;})();
const normalizeOpenCode=(()=>{` + functions(pending, "V", "ke") + `return ke;})();
const capsFor=(()=>{const x=()=>staticCaps,S=defaultEffort,v=trim;
` + functions(picker, "G", "ye") + `return ye;})();
const descriptors=(()=>{
` + functions(pending, "V", "me", "he", "k", "A", "j", "M", "N") + `return N;})();
const original=(()=>{` + aliases + synaraPickerCompiledFunction(t, composer, name) + `return ` + name + `;})();
const current=(()=>{` + aliases + synaraPickerCompiledFunction(t, string(patched), name) + `return ` + name + `;})();
const build=(()=>{` + functions(settings, buildName, metadataName) + `return ` + buildName + `;})();
const mergeDynamic=(()=>{` + mergeAliases + synaraPickerCompiledFunction(t, settings, mergeName) + `return ` + mergeName + `;})();
const rows=__kiloSynaraClaudeRuntime('kilo_claude_proxy',[]);
const input=(runtime,effort)=>({provider:'claudeAgent',model:runtime.slug,runtimeModel:runtime,prompt:'synthetic',modelOptions:{claudeAgent:{...(effort===undefined?{}:{effort}),thinking:false}}});
assert.equal(original(input(rows[0],'high')).modelOptionsForDispatch?.effort,undefined,'negative control: native normalizer omits its default high');
for(const effort of ['low','high']){
 const result=current(input(rows[0],effort));
 const selection=build('claudeAgent',rows[0].slug,result.modelOptionsForDispatch,undefined,{instanceId:'kilo_claude_proxy'});
 assert.equal(selection.options.effort,effort);assert.equal(selection.options.thinking,false);
 assert.equal(selection.instanceId,'kilo_claude_proxy');assert.equal(result.promptEffort,effort);
}
assert.equal(current(input(rows[0],undefined)).modelOptionsForDispatch.effort,'low');
assert.equal(current(input(rows[0],'max')).modelOptionsForDispatch.effort,'low');
const empty=current(input(rows[2],'high'));
const emptySelection=build('claudeAgent',rows[2].slug,empty.modelOptionsForDispatch,undefined,{instanceId:'kilo_claude_proxy'});
assert.equal(emptySelection.options?.effort,undefined);assert.equal(empty.promptEffort,null);
const emptyCaps=capsFor({provider:'claudeAgent',model:rows[2].slug,runtimeModel:rows[2]});
const emptyDescriptors=descriptors({provider:'claudeAgent',caps:emptyCaps,selections:{effort:'high'}});
assert.deepEqual(emptyDescriptors.find(x=>x.id==='effort').options,[]);
assert.equal(emptyDescriptors.find(x=>x.id==='effort').currentValue,undefined);
assert.deepEqual(descriptors({provider:'claudeAgent',caps:capsFor({provider:'claudeAgent',model:rows[0].slug,runtimeModel:rows[0]})}).find(x=>x.id==='effort').options.map(x=>x.id),['low','high']);
for(const effort of [undefined,'low','high']){
 const native={slug:rows[0].slug,name:'Native',kiloSynaraInstanceId:'kilo_claude_normal'};
 assert.deepEqual(current(input(native,effort)),original(input(native,effort)));
 delete native.kiloSynaraInstanceId;
 assert.deepEqual(current(input(native,effort)),original(input(native,effort)));
}
const oc=__kiloSynaraClaudeRuntime('kilo_opencode_proxy',[]);
const ocInput=(runtime,variant)=>({provider:'opencode',model:runtime.slug,runtimeModel:runtime,prompt:'synthetic',modelOptions:{opencode:{...(variant===undefined?{}:{variant}),agent:'build'}}});
assert.equal(original(ocInput(oc[1],'high')).modelOptionsForDispatch.variant,'high','negative control: native empty variant list preserves stale raw variant');
for(const variant of ['low','high']){
 const result=current(ocInput(oc[0],variant));
 const selection=build('opencode',oc[0].slug,result.modelOptionsForDispatch,{instanceId:'kilo_opencode_proxy'});
 assert.equal(selection.options.variant,variant);assert.equal(selection.options.agent,'build');assert.equal(selection.instanceId,'kilo_opencode_proxy');assert.equal(result.promptEffort,variant);
}
assert.equal(current(ocInput(oc[0],undefined)).modelOptionsForDispatch.variant,'low');
const noVariant=current(ocInput(oc[1],'high'));
assert.deepEqual(noVariant.modelOptionsForDispatch,{agent:'build'});assert.equal(noVariant.promptEffort,null);
assert.deepEqual(descriptors({provider:'opencode',caps:capsFor({provider:'opencode',model:oc[1].slug,runtimeModel:oc[1]}),selections:{variant:'high'}}).find(x=>x.id==='variant').options,[]);
const oldSlug='kilo-local/unselected/old-chat';
const merged=mergeDynamic({provider:'opencode',staticOptions:[{slug:oldSlug,name:'Previous chat',isCustom:true}],dynamicModels:oc});
assert.equal(merged.some(x=>x.slug===oldSlug),true,'negative control: actual upstream merger retains the stale custom hint');
assert.deepEqual(__kiloSynaraClaudeOptions('kilo_opencode_proxy',merged).map(x=>x.slug),oc.map(x=>x.slug));
assert.equal(__kiloSynaraClaudeOptions('opencode',merged),merged);
for(const variant of [undefined,'low','high']){
 const native={slug:oc[0].slug,name:'Native'};
 assert.deepEqual(current(ocInput(native,variant)),original(ocInput(native,variant)));
}
`
			if output, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
				t.Fatalf("compiled pure composer: %v %s", err, output)
			}
			t.Log("actual compiled composer+normalizer+runtime capabilities+option descriptors+ModelSelection constructor: Claude low→low high→high []→no effort; OpenCode low/high explicit []purged agent preserved; sticky upstream hint removed only in Kilo options; native unchanged")
		})
	}
}

// Full immutable web preparation uses only previously verified owned ASARs.
// No app binary, install script, login profile or Electron process is present.
func TestSynaraPrivateWebInstalledPackages(t *testing.T) {
	paths := os.Getenv("KILO_TEST_SYNARA_PICKER_ASARS")
	if paths == "" {
		if root := os.Getenv("KILO_TEST_SYNARA_PICKER_PACKAGES"); root != "" {
			paths = filepath.Join(root, "stable", "app.asar") + string(os.PathListSeparator) + filepath.Join(root, "beta", "app.asar")
		}
	}
	if paths == "" {
		t.Skip("official ASAR web preparation is opt-in")
	}
	for _, sourceArchive := range filepath.SplitList(paths) {
		t.Run(filepath.Base(filepath.Dir(sourceArchive)), func(t *testing.T) {
			sourceDigest, err := openDesignFileHash(sourceArchive, 512<<20)
			if err != nil {
				t.Fatal(err)
			}
			fixture := t.TempDir()
			binary := filepath.Join(fixture, "Synara.app")
			archivePath := filepath.Join(binary, "Contents", "Resources", "app.asar")
			if err := os.MkdirAll(filepath.Dir(archivePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(sourceArchive, archivePath); err != nil {
				t.Fatal(err)
			}
			archive, err := t3CodeOpenASAR(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.file.Close()
			pickerSupported := synaraInstalledPickerSupported(archive) && synaraInstalledOpenCodePickerSupported(archive)
			defaults := synaraInstalledNativeDefaults(archive)
			if !pickerSupported || len(defaults) != 8 || defaults["codex"] != "gpt-6-astra" || defaults["claudeAgent"] != "claude-sonnet-5" {
				t.Fatalf("official compiled capabilities mismatch: supported=%v defaults=%v", pickerSupported, defaults)
			}
			packageData, err := archive.read("package.json", 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			var metadata synaraPackageInfo
			if json.Unmarshal(packageData, &metadata) != nil {
				t.Fatal("invalid official package metadata")
			}
			wantCount, wantBytes := 0, int64(0)
			switch metadata.Name + "@" + metadata.Version {
			case "synara-desktop@1.0.4":
				wantCount, wantBytes = 2834, 34157278
			case "synara-desktop-beta@1.0.2-beta.1":
				wantCount, wantBytes = 2812, 34054207
			default:
				t.Fatal("this explicit package acceptance has no recorded asset counts", metadata.Name, metadata.Version)
			}
			options := synaraPrivateWebOptions{RootDir: filepath.Join(fixture, "private"), Policy: synaraPickerCombinedTestPolicy()}
			saved, err := prepareSynaraPrivateWeb(options, binary, "macos")
			if err != nil || len(saved.Files) != wantCount {
				t.Fatalf("full official private preparation: err=%v files=%d expected=%d", err, len(saved.Files), wantCount)
			}
			if !synaraPrivateWebReady(options, binary, "macos", saved) {
				t.Fatal("complete official web manifest/source is not ready")
			}
			sourceBytes := int64(0)
			unchanged, tamper := 0, ""
			var original []byte
			for name, digest := range saved.Files {
				data, err := archive.read("apps/server/dist/client/"+name, synaraPrivateWebFileLimit)
				if err != nil {
					t.Fatal(err)
				}
				sourceBytes += int64(len(data))
				if name == saved.PickerEntry || name == saved.ComposerEntry {
					patch := patchSynaraPickerSource
					if name == saved.ComposerEntry {
						patch = patchSynaraComposerSource
					}
					patched, err := patch(data, options.Policy)
					if err != nil || openDesignHash(patched) != digest {
						t.Fatal("official picker bytes do not match prepared policy")
					}
					continue
				}
				if openDesignHash(data) != digest {
					t.Fatal("an unrelated official asset was changed", name)
				}
				unchanged++
				if tamper == "" && strings.HasSuffix(name, ".css") {
					tamper, original = name, data
				}
			}
			if sourceBytes != wantBytes || unchanged != wantCount-2 || tamper == "" {
				t.Fatalf("full original bytes/count mismatch: bytes=%d unchanged=%d", sourceBytes, unchanged)
			}
			reused, err := prepareSynaraPrivateWeb(options, binary, "macos")
			if err != nil || !reflect.DeepEqual(saved, reused) {
				t.Fatal("complete official cache was not reused", err)
			}
			changedPath := filepath.Join(saved.Root, filepath.FromSlash(tamper))
			if err := os.WriteFile(changedPath, []byte("changed official asset"), 0600); err != nil {
				t.Fatal(err)
			}
			if synaraPrivateWebReady(options, binary, "macos", saved) {
				t.Fatal("tampered full official cache remained ready")
			}
			if _, err := prepareSynaraPrivateWeb(options, binary, "macos"); err == nil {
				t.Fatal("tampered full official cache was silently overwritten")
			}
			if data, _ := os.ReadFile(changedPath); string(data) != "changed official asset" {
				t.Fatal("failed reprepare modified tampered cache")
			}
			if err := os.WriteFile(changedPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			if !synaraPrivateWebReady(options, binary, "macos", saved) {
				t.Fatal("restored exact official cache is not ready")
			}
			after, err := openDesignFileHash(sourceArchive, 512<<20)
			if err != nil || after != sourceDigest {
				t.Fatal("original official ASAR bytes changed")
			}
			t.Logf("official=%s version=%s files=%d originalBytes=%d unchangedAssets=%d pickerPatched=1 composerPatched=1 capability=true defaults=8 reuse=true tamperRejected=true sourceUnchanged=true", metadata.Name, metadata.Version, wantCount, sourceBytes, unchanged)
		})
	}
}
