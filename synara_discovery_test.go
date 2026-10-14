package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSynaraDiscoveryCatalogRequiresLiveServiceContract(t *testing.T) {
	source := "//#region src/provider/Layers/ProviderDiscoveryService.ts\nconst listModels = input => {\n" + synaraDiscoveryModelsAnchor + "\n};\n//#endregion\n"
	source += "//#region src/provider/Layers/ProviderService.ts\n" + synaraOpenCodeRouteAnchor + "\n}\nfunction resolveLaunchProviderInstance(input) {\n" + synaraOpenCodeLaunchAnchor + "\n};\n}\n//#endregion\n"
	patched, err := patchSynaraDiscoveryModels([]byte(source))
	if err != nil || !strings.Contains(string(patched), ".pipe(Effect.map((result) => kiloSynaraDiscoveryModels(resolved, result)))") {
		t.Fatal("shared discovery service was not filtered after cache lookup", err)
	}
	for _, altered := range []string{
		"/*\n" + source + "*/", "`\n" + source + "`", source + source,
		strings.Replace(source, "parsed.refresh", "other.refresh", 1),
		strings.Replace(source, "ProviderDiscoveryService.ts", "UnrelatedService.ts", 1),
		source + synaraOpenCodeRouteAnchor, source + synaraOpenCodeLaunchAnchor,
	} {
		if result, err := patchSynaraDiscoveryModels([]byte(altered)); err == nil || result != nil {
			t.Fatal("unverified discovery service accepted")
		}
	}
}

func TestSynaraDiscoveryInstalledSources(t *testing.T) {
	paths := os.Getenv("KILO_TEST_SYNARA_PROVIDER_SOURCES")
	if paths == "" {
		t.Skip("explicit extracted official sources required; no application is executed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Split(paths, string(os.PathListSeparator)) {
		t.Run(path, func(t *testing.T) {
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			options := synaraProfileTestOptions(t)
			prefs := defaultSynaraPreferences()
			prefs.Codex, prefs.Claude, prefs.KiloCodex, prefs.KiloClaude = false, false, false, false
			prefs.Providers, prefs.OpenCodeConnection = []string{"opencode"}, "kilo"
			options.Preferences = &prefs
			policy, err := deriveSynaraModelPolicy(prefs, options.Library, options.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			options.Policy = &policy
			patched, err := patchSynaraDelegationServer(source, options)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Count(patched, []byte(".pipe(Effect.map((result) => kiloSynaraDiscoveryModels(resolved, result)))")) != 1 {
				t.Fatal("the shared discovery consumer was not patched exactly once")
			}
			start := bytes.Index(patched, []byte("function validateModelSelectionMatchesRoute(input) {"))
			if start < 0 {
				t.Fatal("route validation function is missing")
			}
			end := bytes.Index(patched[start:], []byte("\nfunction runtimePayloadRecord("))
			if end < 0 {
				t.Fatal("route validation function is not uniquely bounded")
			}
			probe := append([]byte("const kiloSynaraGatewayModels={opencode:[{slug:'kilo-local/allowed',kiloAllowedVariants:['high']}]};\nfunction toValidationError(operation,message){return {operation,message};}\n"), []byte(synaraDelegationHelpers[strings.Index(synaraDelegationHelpers, "function kiloSynaraOpenCodeSelectionError("):strings.Index(synaraDelegationHelpers, "function kiloSynaraGatewayCatalogs(")])...)
			probe = append(probe, patched[start:start+end]...)
			probe = append(probe, []byte("\nconst valid={operation:'test',provider:'opencode',providerInstanceId:'kilo_opencode_proxy',modelSelection:{provider:'opencode',instanceId:'kilo_opencode_proxy',model:'kilo-local/allowed',options:{variant:'high'}}};\nif(validateModelSelectionMatchesRoute(valid)!==undefined)throw Error('valid prepared model rejected');\nfor(const modelSelection of [undefined,{...valid.modelSelection,model:'foreign/old-model'},{...valid.modelSelection,options:{variant:'low'}}]){if(!validateModelSelectionMatchesRoute({...valid,modelSelection}))throw Error('stale selection was not rejected before dispatch');}\nif(validateModelSelectionMatchesRoute({...valid,providerInstanceId:'opencode',modelSelection:{provider:'opencode',instanceId:'opencode',model:'native/other'}})!==undefined)throw Error('native selection changed');\n")...)
			probePath := filepath.Join(t.TempDir(), "actual-route-probe.cjs")
			if err := os.WriteFile(probePath, probe, 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := exec.Command(node, probePath).CombinedOutput(); err != nil {
				t.Fatalf("actual official route validation: %v\n%s", err, result)
			}
			output := filepath.Join(t.TempDir(), "private-server.mjs")
			if err := os.WriteFile(output, patched, 0600); err != nil {
				t.Fatal(err)
			}
			if result, err := exec.Command(node, "--check", output).CombinedOutput(); err != nil {
				t.Fatalf("patched official server syntax: %v\n%s", err, result)
			}
		})
	}
}
