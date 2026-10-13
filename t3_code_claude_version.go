package main

import "fmt"

// T3 exposes model-specific effort controls only after the CLI version that
// introduced each native model. The general Claude picker minimum is separate.
func validateT3CodeNativeClaudeVersion(library modelLibrary, caps claudeCapabilities) error {
	for _, model := range library.Models {
		minimum := map[string]string{
			"claude-opus-5-5":   "2.1.280",
			"claude-sonnet-5-5": "2.1.284",
			"claude-fable-5-1":  "2.1.257",
		}[t3CodeNativeClaudeModelID(model.ID)]
		if minimum != "" && (claudeVersionPattern.FindStringSubmatch(caps.Version) == nil || managedToolCompareNumericVersion(caps.Version, minimum) < 0) {
			return fmt.Errorf("T3 Code requires Claude Code %s or newer for %s and its thinking controls. Update Claude Code and prepare again.", minimum, model.ID)
		}
	}
	return nil
}
