package main

import (
	"os"
	"strings"
	"testing"
)

func TestSynaraOptionalProvidersRequireRegisteredLiveFactories(t *testing.T) {
	source := `//#region src/provider/Layers/ProviderAdapterRegistry.ts
const makeProviderAdapterRegistry = (options) => Effect.gen(function* () {
 const adapters = [yield* CursorAdapter, yield* OpenCodeAdapter]; return adapters;
});
//#endregion
//#region src/provider/Layers/CursorAdapter.ts
function makeCursorAdapterLive() { return Layer.effect(CursorAdapter, implementation); }
//#endregion
//#region src/provider/Layers/OpenCodeAdapter.ts
function makeOpenCodeAdapterLive() { return Layer.effect(OpenCodeAdapter, implementation); }
//#endregion
`
	providers, second, kilo := synaraOptionalProviderCapabilities(source)
	if len(providers) != 2 || providers[0].ID != "cursor" || providers[1].ID != "opencode" || second || kilo {
		t.Fatal("a provider label was confused with a complete V2/account isolation contract", providers, second, kilo)
	}
	for _, altered := range []string{
		strings.Replace(source, "yield* CursorAdapter", "yield* OtherAdapter", 1),
		strings.Replace(source, "function makeCursorAdapterLive", "function unusedFactory", 1),
		strings.Replace(source, "Layer.effect(CursorAdapter,", "Layer.effect(OtherAdapter,", 1),
		strings.Replace(source, "function makeCursorAdapterLive() { return Layer.effect(CursorAdapter, implementation); }", "function makeCursorAdapterLive() { return undefined; }\nconst unrelated = Layer.effect(CursorAdapter, implementation);", 1),
	} {
		providers, _, _ := synaraOptionalProviderCapabilities(altered)
		for _, provider := range providers {
			if provider.ID == "cursor" {
				t.Fatal("an unregistered or incomplete Cursor factory was advertised")
			}
		}
	}
	for _, altered := range []string{source + source, "/*\n" + source + "\n*/", "`\n" + source + "\n`", "const example=`outer ${`\n" + source + "\n`}`;", strings.Replace(source, "const makeProviderAdapterRegistry", "const UNUSED_REGISTRY", 1)} {
		if providers, second, kilo := synaraOptionalProviderCapabilities(altered); len(providers) != 0 || second || kilo {
			t.Fatal("duplicated, quoted, absent or commented registry was accepted")
		}
	}
}

// Explicitly extracted official sources only: this test never starts Synara,
// opens a login profile, or calls a provider.
func TestSynaraOptionalInstalledSourceContracts(t *testing.T) {
	paths := os.Getenv("KILO_TEST_SYNARA_PROVIDER_SOURCES")
	if paths == "" {
		t.Skip("set explicit official Synara server source paths for source acceptance")
	}
	for _, path := range strings.Split(paths, string(os.PathListSeparator)) {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			providers, second, kilo := synaraOptionalProviderCapabilities(source)
			if len(providers) != 5 || !second || !kilo {
				t.Fatal("official native providers/V2/instance environment contract rejected", providers, second, kilo)
			}
			for _, replacement := range [][2]string{
				{"candidate.instanceId === instanceId", "candidate.driver === instanceId"},
				{"const environment = providerOptions?.environment;", "const environment = process.env;"},
				{"...resolvedProviderInstanceId !== void 0 ? { instanceId: resolvedProviderInstanceId } : {},\n\t\t\t\t\t\t...serverUrl ? { serverUrl } : {},\n\t\t\t\t\t\t...environment !== void 0 ? { environment } : {},", "...environment !== void 0 ? { environment: process.env } : {},"},
				{"...input.environment !== void 0 ? { environment: input.environment } : {},\n\t\t\t\t\t...input.experimentalWebSockets", "...input.environment !== void 0 ? { environment: process.env } : {},\n\t\t\t\t\t...input.experimentalWebSockets"},
			} {
				// Choose a changed complete contract, not a blind retry.
				changed := strings.ReplaceAll(source, replacement[0], replacement[1])
				if changed == source {
					t.Fatal("negative fixture did not change the installed source", replacement[0])
				}
				if _, _, accepted := synaraOptionalProviderCapabilities(changed); accepted {
					t.Fatal("an altered account/environment boundary was accepted", replacement[0])
				}
			}
		})
	}
}

func TestSynaraOptionalCommentedContractWithLiveTail(t *testing.T) {
	source := `/*
//#region src/provider/Layers/ProviderAdapterRegistry.ts
const makeProviderAdapterRegistry = (options) => Effect.gen(function* () {
 const adapters = [yield* CursorAdapter]; return adapters;
});
*/
const unrelatedRegistry = true;
//#endregion
/*
//#region src/provider/Layers/CursorAdapter.ts
function makeCursorAdapterLive() { return Layer.effect(CursorAdapter, implementation); }
*/
const unrelatedFactory = true;
//#endregion
`
	if providers, second, kilo := synaraOptionalProviderCapabilities(source); len(providers) != 0 || second || kilo {
		t.Fatal("commented contracts acquired the lexical context of unrelated live code", providers, second, kilo)
	}
}
