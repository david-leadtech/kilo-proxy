package main

func t3CodeRuntimeModelAllowlist(policy t3CodeModelPolicy) map[string][]string {
	result := map[string][]string{t3CodeCodexProxyID: {}, t3CodeClaudeProxyID: {}}
	for _, model := range policy.CodexLibrary.Models {
		result[t3CodeCodexProxyID] = append(result[t3CodeCodexProxyID], model.ID)
	}
	for _, model := range t3CodeClaudePickerLibrary(policy.ClaudeLibrary, policy.ClaudeRoutes).Models {
		result[t3CodeClaudeProxyID] = append(result[t3CodeClaudeProxyID], model.ID)
	}
	normalized, _ := normalizeT3CodeRuntimeModelAllowlist(result)
	return normalized
}

func t3CodeRuntimeModelDefaults(policy t3CodeModelPolicy) map[string]string {
	result := map[string]string{}
	if len(policy.CodexLibrary.Models) > 0 {
		result[t3CodeCodexProxyID] = policy.CodexLibrary.DefaultModel
	}
	claude := t3CodeClaudePickerLibrary(policy.ClaudeLibrary, policy.ClaudeRoutes)
	if len(claude.Models) > 0 {
		result[t3CodeClaudeProxyID] = claude.DefaultModel
	}
	return result
}
