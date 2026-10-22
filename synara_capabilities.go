package main

import (
	"errors"
	"strings"
)

func synaraProviderCapabilities(binary, platform string) (map[string]any, error) {
	result := map[string]any{"supportedProviders": []t3CodeProviderSupport{}, "openCode2Supported": false, "openCodeKiloSupported": false, "museKiloSupported": false, "claudePickerSupported": false}
	if _, err := synaraPackageMetadata(binary, platform); err != nil {
		return result, err
	}
	archive, err := t3CodeOpenPackage(binary, platform)
	if err != nil {
		return result, errors.New("Cannot verify the installed Synara provider drivers.")
	}
	defer archive.file.Close()
	result["claudePickerSupported"] = (platform == "macos" || platform == "darwin") && synaraInstalledPickerSupported(archive)
	result["nativeDefaults"] = synaraInstalledNativeDefaults(archive)
	result["sourceDigest"] = openDesignHash(archive.header)
	source, err := archive.read("apps/server/dist/index.mjs", synaraRuntimeSourceLimit)
	if err != nil {
		// An older/unknown backend advertises no optional drivers. The picker
		// contract is independent; preparing a private backend still validates
		// its complete server source before activating any profiles.
		return result, nil
	}
	providers, second, kilo := synaraOptionalProviderCapabilities(string(source))
	kilo = kilo && (platform == "macos" || platform == "darwin") && synaraDiscoveryModelsSupported(string(source)) && synaraInstalledOpenCodePickerSupported(archive)
	result["supportedProviders"], result["openCode2Supported"], result["openCodeKiloSupported"] = providers, second, kilo
	result["sourceDigest"] = openDesignHash(append(append([]byte(nil), archive.header...), source...))
	return result, nil
}

// Sections and executable anchors are inspected together: a copied example,
// string or commented-out adapter cannot turn a provider name into support.
// These are structural contracts from both official stable and Beta bundles,
// rather than an allowlist of release versions or package hashes.
func synaraSourceSection(source, path string, masks ...string) (string, bool) {
	anchor := "//#region " + path + "\n"
	if strings.Count(source, anchor) != 1 {
		return "", false
	}
	start := strings.Index(source, anchor) + len(anchor)
	end := strings.Index(source[start:], "\n//#endregion")
	if end < 0 || end > 512<<10 {
		return "", false
	}
	mask := ""
	if len(masks) > 0 {
		mask = masks[0]
	} else {
		mask = synaraContractCode(source)
	}
	if len(mask) != len(source) || strings.TrimSpace(mask[start:start+end]) == "" {
		return "", false
	}
	section := source[start : start+end]
	// A region marker inside a preceding comment/template must not lose its
	// lexical context when inspected in isolation. Reject that split context.
	if synaraContractCode(section) != mask[start:start+end] {
		return "", false
	}
	return section, true
}

func synaraLiveSourceAnchor(source, anchor string, counts ...int) bool {
	count := 1
	if len(counts) > 0 {
		count = counts[0]
	}
	if strings.Count(source, anchor) != count {
		return false
	}
	code := synaraContractCode(source)
	// Require the first executable token in the exact contract to remain live.
	end := strings.IndexAny(anchor, " (\t\r\n.:[")
	if end < 0 {
		end = len(anchor)
	}
	if end == 0 {
		return false
	}
	for offset := 0; offset < len(source); {
		index := strings.Index(source[offset:], anchor)
		if index < 0 {
			break
		}
		index += offset
		if code[index:index+end] != anchor[:end] {
			return false
		}
		offset = index + len(anchor)
	}
	return true
}

func synaraFactoryReturnsAdapter(source, factory, adapter string) bool {
	anchor := "function " + factory + "("
	if !synaraLiveSourceAnchor(source, anchor) {
		return false
	}
	code := synaraContractCode(source)
	start := strings.Index(source, anchor) + len(anchor) - 1
	depth, bodyStart := 0, -1
	for i := start; i < len(code); i++ {
		if code[i] == '(' {
			depth++
		} else if code[i] == ')' {
			depth--
			if depth == 0 {
				bodyStart = i + 1
				break
			}
		}
	}
	for bodyStart >= 0 && bodyStart < len(code) && strings.ContainsRune(" \t\r\n", rune(code[bodyStart])) {
		bodyStart++
	}
	if bodyStart < 0 || bodyStart >= len(code) || code[bodyStart] != '{' {
		return false
	}
	depth = 0
	for i := bodyStart; i < len(code); i++ {
		if code[i] == '{' {
			depth++
		} else if code[i] == '}' {
			depth--
			if depth == 0 {
				body := strings.TrimSpace(code[bodyStart+1 : i])
				direct := "return Layer.effect(" + adapter + ","
				if strings.HasPrefix(body, direct) {
					return true
				}
				return adapter == "OpenCodeAdapter" && strings.HasPrefix(body, "const adapterConfig = options?.adapterConfig ?? OPENCODE_ADAPTER_CONFIG;\n\treturn Layer.effect(OpenCodeAdapter,")
			}
		}
	}
	return false
}

func synaraOptionalProviderCapabilities(source string) ([]t3CodeProviderSupport, bool, bool) {
	providers := []t3CodeProviderSupport{}
	mask := synaraContractCode(source)
	registry, ok := synaraSourceSection(source, "src/provider/Layers/ProviderAdapterRegistry.ts", mask)
	if !ok || !synaraLiveSourceAnchor(registry, "const makeProviderAdapterRegistry = (options) => Effect.gen(function* () {") {
		return providers, false, false
	}
	for _, spec := range []struct{ id, label, adapter, factory, section string }{
		{"cursor", "Cursor", "CursorAdapter", "makeCursorAdapterLive", "CursorAdapter"},
		{"grok", "Grok", "GrokAdapter", "makeGrokAdapterLive", "GrokAdapter"},
		{"opencode", "OpenCode", "OpenCodeAdapter", "makeOpenCodeAdapterLive", "OpenCodeAdapter"},
		{"antigravity", "Antigravity", "AntigravityAdapter", "makeAntigravityAdapterLive", "AntigravityAdapter"},
		{"pi", "Pi", "PiAdapter", "makePiAdapterLive", "PiAdapter"},
	} {
		body, valid := synaraSourceSection(source, "src/provider/Layers/"+spec.section+".ts", mask)
		if valid && synaraLiveSourceAnchor(registry, "yield* "+spec.adapter) && synaraFactoryReturnsAdapter(body, spec.factory, spec.adapter) {
			providers = append(providers, t3CodeProviderSupport{ID: spec.id, Label: spec.label})
		}
	}
	adapter, valid := synaraSourceSection(source, "src/provider/Layers/OpenCodeAdapter.ts", mask)
	openCode := false
	for _, provider := range providers {
		openCode = openCode || provider.ID == "opencode"
	}
	if !valid || !openCode {
		return providers, false, false
	}
	protocol, pOK := synaraSourceSection(source, "src/provider/openCodeProtocol.ts", mask)
	client, cOK := synaraSourceSection(source, "src/provider/openCodeV2Client.ts", mask)
	second := pOK && cOK && synaraLiveSourceAnchor(protocol, "async function detectOpenCodeProtocol(input) {") &&
		synaraLiveSourceAnchor(protocol, "const response = await request(`${input.baseUrl.replace(/\\/$/, \"\")}/api/info`, {") &&
		synaraLiveSourceAnchor(client, "function withOpenCodeV2Client(legacy, input) {") &&
		synaraLiveSourceAnchor(client, `return protocol === "v2" ? run(...args) : old(...args);`)
	kilo := second
	kilo = kilo && synaraLiveSourceAnchor(adapter, "const providerOptions = input.providerOptions?.[adapterConfig.providerOptionsKey];\n\t\t\tconst binaryPath = providerOptions?.binaryPath?.trim() || adapterConfig.defaultBinaryPath;", 3)
	for _, anchor := range []string{
		"const getByInstance = (instanceId, options) => serverSettings.getSettings.pipe(Effect.flatMap((settings) => {",
		"const instance = deriveProviderInstances(settings).find((candidate) => candidate.instanceId === instanceId);",
		"return getByProvider(instance.driver).pipe(Effect.map((adapter) => adapterFacadeForInstance(adapter, instance.instanceId, untaggedSessionClaims)));",
	} {
		kilo = kilo && synaraLiveSourceAnchor(registry, anchor)
	}
	for _, anchor := range []string{
		"const environment = providerOptions?.environment;\n\t\t\tconst experimentalWebSockets = providerOptions?.experimentalWebSockets;",
		"const server = yield* openCodeRuntime.connectToOpenCodeServer({\n\t\t\t\t\t\tbinaryPath,\n\t\t\t\t\t\tcliSpec: adapterConfig.cliSpec,\n\t\t\t\t\t\tcwd: directory,\n\t\t\t\t\t\thomeDir: serverConfig.homeDir,\n\t\t\t\t\t\tisolationRootDir: serverConfig.stateDir,\n\t\t\t\t\t\t...resolvedProviderInstanceId !== void 0 ? { instanceId: resolvedProviderInstanceId } : {},\n\t\t\t\t\t\t...serverUrl ? { serverUrl } : {},\n\t\t\t\t\t\t...environment !== void 0 ? { environment } : {},",
		"const withDiscoveryClient = (input, fn) => Effect.gen(function* () {",
		"const server = yield* openCodeRuntime.connectToOpenCodeServer({\n\t\t\t\t\tbinaryPath: input.binaryPath?.trim() || adapterConfig.defaultBinaryPath,\n\t\t\t\t\tcliSpec: adapterConfig.cliSpec,\n\t\t\t\t\tcwd: input.cwd?.trim() || serverConfig.cwd,\n\t\t\t\t\thomeDir: serverConfig.homeDir,\n\t\t\t\t\tisolationRootDir: serverConfig.stateDir,\n\t\t\t\t\t...input.instanceId != null ? { instanceId: input.instanceId } : {},\n\t\t\t\t\t...serverUrl ? { serverUrl } : {},\n\t\t\t\t\t...input.environment !== void 0 ? { environment: input.environment } : {},",
	} {
		kilo = kilo && synaraLiveSourceAnchor(adapter, anchor)
	}
	return providers, second, kilo
}
