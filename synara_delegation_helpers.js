// Loaded only in Kilo Proxy's private, contract-checked Synara server snapshot.
const kiloSynaraGatewayModels = __KILO_SYNARA_GATEWAY_MODELS__;
function kiloSynaraDefaultModel(input) {
	const managed = input.instanceId === "kilo_codex_proxy" ? kiloSynaraGatewayModels.codex : input.instanceId === "kilo_claude_proxy" ? kiloSynaraGatewayModels.claudeAgent : input.instanceId === "kilo_opencode_proxy" ? kiloSynaraGatewayModels.opencode : null;
	if (managed) {
		const configured = kiloSynaraGatewayModels.defaultModels?.[input.provider] ?? kiloSynaraGatewayModels.defaultModel;
		return input.availability?.customModels?.includes(configured) && managed.some(value => value.slug === configured) ? configured : null;
	}
	return providerDefaultModel(input.provider);
}
function kiloSynaraAccountAvailabilities(settings, statuses) {
	const instances = deriveProviderInstances(settings);
	const result = new Map();
	for (const instance of instances) {
		const status = statuses.find(value => value.instanceId === instance.instanceId && (value.driver ?? value.provider) === instance.driver);
		const customModels = Array.isArray(instance.config.customModels) ? instance.config.customModels.filter(value => typeof value === "string" && value.trim().length > 0) : [];
		result.set(instance.instanceId, {
			provider: instance.driver, instanceId: instance.instanceId, displayName: instance.displayName,
			enabled: settings.providers[instance.driver].enabled && instance.enabled,
			available: status?.available === true,
			...(status?.authStatus ? { authStatus: status.authStatus } : {}),
			...(status?.message ? { message: status.message } : {}),
			customModels,
			accountRequired: instance.isDefault && instances.some(value => value.driver === instance.driver && !value.isDefault),
			isDefault: instance.isDefault
		});
	}
	return result;
}
function kiloSynaraGatewayAccount(input) {
	const target = input.target;
	const account = input.availability;
	// The shipped ModelSelection decoder fills a missing instanceId with the
	// provider's legacy default ID. That implicit legacy account is ambiguous
	// as soon as custom Normal/Kilo accounts are present, too.
	if ((!target.instanceId || target.instanceId === target.provider) && account?.accountRequired) throw new AgentGatewayTargetError("account_required", `Choose an exact ${target.provider} account from synara_capabilities providers[].instanceId and include target.instanceId. Normal and Kilo Proxy accounts are separate.`);
	if (target.instanceId && (!account || account.instanceId !== target.instanceId)) throw new AgentGatewayTargetError("account_unavailable", `Account "${target.instanceId}" is not configured. Choose an exact account from synara_capabilities.`);
	if (account?.provider && account.provider !== target.provider) throw new AgentGatewayTargetError("account_provider_mismatch", `Account "${account.instanceId}" belongs to ${account.provider}, not ${target.provider}.`);
	// The ordinary account preflight supplies its disabled/auth message first.
	if (account?.enabled === false || account?.available === false || account?.authStatus === "unauthenticated") return target;
	const managed = target.instanceId === "kilo_claude_proxy" ? kiloSynaraGatewayModels.claudeAgent.find(value => value.slug === target.model) : null;
	if (managed && target.options?.effort !== undefined) {
		const allowed = managed.kiloAllowedEfforts ?? (managed.kiloSavedEffort ? [managed.kiloSavedEffort] : []);
		if (!allowed.includes(target.options.effort)) throw new AgentGatewayTargetError("model_option_unavailable", `This effort is not enabled for "${target.model}" in the prepared Claude · Kilo Proxy account. Choose an advertised effort or prepare its models again. Do not substitute a different account or model.`);
	}
	const openCode = target.instanceId === "kilo_opencode_proxy" ? kiloSynaraGatewayModels.opencode?.find(value => value.slug === target.model) : null;
	if (openCode && target.options?.variant !== undefined && !openCode.kiloAllowedVariants.includes(target.options.variant)) throw new AgentGatewayTargetError("model_option_unavailable", `This variant is not enabled for "${target.model}" in the prepared OpenCode Kilo account.`);
	return target;
}
function kiloSynaraMergeAccountModels(input, discovered) {
	const declared = input.availability?.customModels ?? [];
	const managed = input.instanceId === "kilo_codex_proxy" ? kiloSynaraGatewayModels.codex : input.instanceId === "kilo_claude_proxy" ? kiloSynaraGatewayModels.claudeAgent : input.instanceId === "kilo_opencode_proxy" ? (kiloSynaraGatewayModels.opencode ?? []) : [];
	// Kilo account choices are the exact prepared gateway IDs. Native aliases
	// remain confined to the normal account's discovery catalog.
	if (input.instanceId === "kilo_codex_proxy" || input.instanceId === "kilo_claude_proxy" || input.instanceId === "kilo_opencode_proxy") return declared.flatMap(slug => {
		const descriptor = managed.find(value => value.slug === slug);
		if (!descriptor) return [];
		if (input.instanceId !== "kilo_opencode_proxy") return [descriptor];
		const native = discovered.find(value => value.slug === slug);
		const allowed = descriptor.kiloAllowedVariants;
		const options = (native?.optionDescriptors ?? []).map(option => {
			if (option.id !== "variant" || option.type !== "select") return option;
			const copy = {...option,options:option.options.filter(value => allowed.includes(value.id)).map(value => {
				const choice = {...value};
				delete choice.isDefault;
				if (value.id === descriptor.kiloDefaultVariant) choice.isDefault = true;
				return choice;
			})};
			if (allowed.includes(descriptor.kiloDefaultVariant)) copy.currentValue = descriptor.kiloDefaultVariant;
			else delete copy.currentValue;
			return copy;
		});
		const model = {...native,...descriptor};
		// OpenCode normally exposes legacy reasoning metadata only. An empty
		// descriptor array would suppress Synara's native variant-menu fallback.
		if (native?.optionDescriptors !== undefined) model.optionDescriptors = options;
		if (Array.isArray(model.supportedReasoningEfforts)) model.supportedReasoningEfforts = model.supportedReasoningEfforts.filter(value => allowed.includes(value.value));
		if (allowed.includes(descriptor.kiloDefaultVariant)) model.defaultReasoningEffort = descriptor.kiloDefaultVariant;
		else delete model.defaultReasoningEffort;
		return [model];
	});
	const bySlug = new Map(discovered.map(value => [value.slug, value]));
	for (const slug of declared) {
		const descriptor = managed.find(value => value.slug === slug);
		if (descriptor) bySlug.set(slug, descriptor);
		else if (!bySlug.has(slug)) bySlug.set(slug, { slug, name: slug });
	}
	return [...bySlug.values()];
}
function kiloSynaraDiscoveryModels(input, result) {
	if (input.provider !== "opencode" || input.instanceId !== "kilo_opencode_proxy") return result;
	const declared = (kiloSynaraGatewayModels.opencode ?? []).map(model => model.slug);
	return {...result, models:kiloSynaraMergeAccountModels({instanceId:input.instanceId,availability:{customModels:declared}}, result.models)};
}
function kiloSynaraOpenCodeSelectionError(instanceId, selection) {
	if (instanceId !== "kilo_opencode_proxy") return;
	const model = selection?.provider === "opencode" ? kiloSynaraGatewayModels.opencode?.find(row => row.slug === selection.model) : null;
	if (!model) return "Choose a model from the prepared Kilo OpenCode library before starting this chat. The previous model was not substituted.";
	if (selection.options?.variant !== undefined && !model.kiloAllowedVariants.includes(selection.options.variant)) return "Choose an enabled reasoning level for this Kilo OpenCode model, or prepare its models again.";
}
function kiloSynaraGatewayCatalogs(availabilities, discovery, cwd) {
	const accounts = [...availabilities.values()].filter(value => !value.isDefault || !value.accountRequired);
	return Effect.forEach(accounts, account => loadAgentGatewayProviderCatalog({ provider: account.provider, instanceId: account.instanceId, availability: account, discovery, cwd }));
}
