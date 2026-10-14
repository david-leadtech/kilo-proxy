package main

import "reflect"

// Reconstruct registration data without profile-file planning or creation.
// The outer readiness check separately verifies all active file hashes.
func synaraAdditionalProvidersReady(options synaraProfileOptions, actual map[string]any) bool {
	if options.Preferences == nil {
		return len(actual) == 4
	}
	expected := normalizeT3CodeProviders(options.AdditionalProviders)
	provider, _, _, err := synaraPlanOpenCode(options)
	if err != nil {
		return false
	}
	if provider != nil {
		expected[synaraOpenCodeProxyID] = provider
	}
	if len(actual) != 4+len(expected) {
		return false
	}
	for id, raw := range expected {
		instance, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if !reflect.DeepEqual(actual[id], redactT3CodeProvider(instance)) {
			return false
		}
	}
	return true
}
