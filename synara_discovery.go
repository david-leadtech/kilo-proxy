package main

import (
	"errors"
	"strings"
)

const synaraDiscoveryModelsAnchor = "return yield* modelDiscoveryCache.lookup({\n\t\t\t...cacheKey,\n\t\t\t...runtimeVersion !== void 0 ? { runtimeVersion } : {}\n\t\t}, discover, parsed.refresh);"
const synaraOpenCodeRouteAnchor = "function validateModelSelectionMatchesRoute(input) {\n\tconst selection = input.modelSelection;"
const synaraOpenCodeLaunchAnchor = "\t\treturn {\n\t\t\tinstance,\n\t\t\tmodelSelection: modelSelectionForInstance(input.modelSelection, instance),"

func synaraDiscoveryModelsSupported(source string) bool {
	section, ok := synaraSourceSection(source, "src/provider/Layers/ProviderDiscoveryService.ts")
	service, valid := synaraSourceSection(source, "src/provider/Layers/ProviderService.ts")
	return ok && valid && synaraLiveSourceAnchor(section, synaraDiscoveryModelsAnchor) && strings.Count(source, synaraDiscoveryModelsAnchor) == 1 && synaraLiveSourceAnchor(service, synaraOpenCodeRouteAnchor) && strings.Count(source, synaraOpenCodeRouteAnchor) == 1 && strings.Count(source, synaraOpenCodeLaunchAnchor) == 1 && synaraLiveSourceAnchor(service, strings.TrimSpace(synaraOpenCodeLaunchAnchor))
}

// The shared discovery service feeds both the UI and delegation. Filter after
// its cache so a project-specific OpenCode catalog cannot reintroduce models
// outside the prepared Kilo account, including on a cached response.
func patchSynaraDiscoveryModels(source []byte) ([]byte, error) {
	text := string(source)
	if !synaraDiscoveryModelsSupported(text) {
		return nil, errors.New("This Synara installation cannot restrict the Kilo OpenCode picker to its prepared models. Choose its native connection or a compatible macOS build.")
	}
	after := strings.TrimSuffix(synaraDiscoveryModelsAnchor, ";") + ".pipe(Effect.map((result) => kiloSynaraDiscoveryModels(resolved, result)));"
	text = strings.Replace(text, synaraDiscoveryModelsAnchor, after, 1)
	text = strings.Replace(text, synaraOpenCodeRouteAnchor, synaraOpenCodeRouteAnchor+"\n\tconst kiloSelectionError = kiloSynaraOpenCodeSelectionError(input.providerInstanceId, selection);\n\tif (kiloSelectionError) return toValidationError(input.operation, kiloSelectionError);", 1)
	launch := "\t\tconst kiloSelectionError = kiloSynaraOpenCodeSelectionError(instance.instanceId, modelSelectionForInstance(input.modelSelection, instance));\n\t\tif (kiloSelectionError) return yield* toValidationError(input.operation, kiloSelectionError);\n" + synaraOpenCodeLaunchAnchor
	text = strings.Replace(text, synaraOpenCodeLaunchAnchor, launch, 1)
	return []byte(text), nil
}
