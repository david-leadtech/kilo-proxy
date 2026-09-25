package main

import "fmt"

const (
	contextPresetRecommended = "recommended"
	contextPresetLow         = "low"
	contextPresetMaximum     = "maximum"
	contextPresetCustom      = "custom"
	contextRecommendedTokens = 272000
	contextLowTokens         = 128000
)

// Capacity comes from the gateway catalog. The saved policy is a working
// budget, not a claim that the provider supports a particular context size.
type contextResolution struct {
	Preset                string
	ContextWindow         int
	MaxOutputTokens       int
	MaximumContextWindow  int
	MaximumKnown          bool
	AutoCompactTokenLimit int
}

func resolveContextPolicy(preset string, customTokens, knownMaximum, outputTokens int) (contextResolution, error) {
	result := contextResolution{Preset: preset, MaximumKnown: knownMaximum >= 1024 && knownMaximum <= 100000000}
	if result.MaximumKnown {
		result.MaximumContextWindow = knownMaximum
	}
	var target int
	switch preset {
	case contextPresetRecommended:
		target = contextRecommendedTokens
	case contextPresetLow:
		target = contextLowTokens
	case contextPresetMaximum:
		if !result.MaximumKnown {
			return result, fmt.Errorf("The model's maximum context is unknown. Refresh the catalog or choose Recommended, Low, or Custom.")
		}
		target = knownMaximum
	case contextPresetCustom:
		if customTokens < 1024 || customTokens > 100000000 {
			return result, fmt.Errorf("Custom context must contain 1,024–100,000,000 whole tokens.")
		}
		target = customTokens
	default:
		return result, fmt.Errorf("Choose Recommended, Low, Maximum, or Custom context.")
	}
	if result.MaximumKnown {
		target = min(target, knownMaximum)
	}
	if outputTokens < 0 || outputTokens > 100000000 {
		return result, fmt.Errorf("Use a valid output token limit.")
	}
	if outputTokens == 0 {
		outputTokens = 8192
	}
	result.ContextWindow = target
	// A catalog output ceiling can be as large as the chosen working window.
	// Reserve at least three quarters for instructions, history, and tool results.
	result.MaxOutputTokens = min(outputTokens, target/4)
	result.AutoCompactTokenLimit = min(target*9/10, target-result.MaxOutputTokens-min(8192, target/20))
	return result, nil
}

// Before presets, contextWindow was the user's only editable limit. Preserve
// every saved nonzero value as Custom rather than guessing its provenance.
func contextPolicyFromLibrary(item modelLibraryItem) (string, int) {
	if item.ContextPreset != "" {
		return item.ContextPreset, item.ContextWindow
	}
	if item.ContextWindow > 0 {
		return contextPresetCustom, item.ContextWindow
	}
	return contextPresetRecommended, 0
}

func contextChoiceFromLibrary(item modelLibraryItem, metadata modelInfo) nativeModelChoice {
	maximumOutput := metadata.MaxOutputTokens
	metadata.ID = item.ID
	if metadata.Name == "" {
		metadata.Name = item.ID
	}
	metadata.MaxOutputTokens = item.MaxOutputTokens
	preset, tokens := contextPolicyFromLibrary(item)
	return nativeModelChoice{Model: metadata, DisplayName: item.DisplayName, DefaultReasoning: item.ReasoningEffort,
		ReasoningCustom: item.ReasoningCustom, ReasoningLevels: append([]string(nil), item.ReasoningLevels...),
		ContextPreset: preset, ContextTokens: tokens, MaximumOutputTokens: maximumOutput}
}

func contextPolicyForChoice(choice nativeModelChoice) (contextResolution, error) {
	preset, tokens := choice.ContextPreset, choice.ContextTokens
	if preset == "" {
		// Legacy imports and callers supplied their editable context in Model.
		preset = contextPresetRecommended
		if choice.Model.ContextWindow > 0 {
			preset, tokens = contextPresetCustom, choice.Model.ContextWindow
		}
	}
	output := choice.Model.MaxOutputTokens
	if choice.MaximumOutputTokens > 0 && choice.MaximumOutputTokens <= 100000000 {
		if output == 0 {
			output = 8192
		}
		output = min(output, choice.MaximumOutputTokens)
	}
	return resolveContextPolicy(preset, tokens, choice.Model.ContextWindow, output)
}
