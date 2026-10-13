package main

// Only Kilo instances have a fixed prepared model policy. Direct and optional
// instances retain T3's own native catalog and stored model options.
func t3CodeDefaultSelection(current any, providers map[string]any, policy t3CodeModelPolicy) any {
	enabled := func(id string) bool {
		provider, ok := providers[id].(map[string]any)
		if !ok {
			return false
		}
		value, exists := provider["enabled"]
		return !exists || value == true
	}
	contains := func(library modelLibrary, id string) bool {
		for _, model := range library.Models {
			if model.ID == id {
				return true
			}
		}
		return false
	}
	claudePicker := t3CodeClaudePickerLibrary(policy.ClaudeLibrary, policy.ClaudeRoutes)
	if selected, ok := current.(map[string]any); ok {
		id, _ := selected["instanceId"].(string)
		model, _ := selected["model"].(string)
		if enabled(id) && model != "" {
			switch id {
			case t3CodeCodexProxyID:
				if contains(policy.CodexLibrary, model) {
					return current
				}
			case t3CodeClaudeProxyID:
				if contains(claudePicker, model) {
					return current
				}
				// A previous profile could store the exact gateway ID. Retain the
				// account and turn options while upgrading its native picker identity.
				target := policy.ClaudeRoutes[model]
				if target == "" && contains(policy.ClaudeLibrary, model) {
					target = model
				}
				native := t3CodeNativeClaudeModelID(target)
				if native != "" && contains(claudePicker, native) {
					result := map[string]any{}
					for key, value := range selected {
						result[key] = value
					}
					result["model"] = native
					return result
				}
			default:
				return current
			}
		}
	}
	for _, candidate := range []struct {
		id      string
		library modelLibrary
	}{{t3CodeCodexProxyID, policy.CodexLibrary}, {t3CodeClaudeProxyID, claudePicker}} {
		if enabled(candidate.id) && len(candidate.library.Models) > 0 {
			return map[string]any{"instanceId": candidate.id, "model": candidate.library.DefaultModel}
		}
	}
	// Null delegates direct/optional model choice to T3's enabled-instance
	// resolver and native driver defaults. It also represents all accounts off.
	return nil
}
