package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

func t3CodeFallbackLibrary(selected, fallback modelLibrary) modelLibrary {
	if len(selected.Models) == 0 {
		return fallback
	}
	return selected
}

func t3CodeModelSummaries(library modelLibrary, catalog []modelInfo) []t3CodeModelSummary {
	models := []t3CodeModelSummary{}
	for _, choice := range terminalLibraryChoices(library, catalog) {
		models = append(models, t3CodeModelSummary{ID: choice.Model.ID, Name: nativeCodexDisplayName(choice), ReasoningEffort: choice.DefaultReasoning})
	}
	return models
}

// T3 owns the native Claude selection and its effort controls. Preserve that
// identity in the CLI; only the scoped HTTP adapter translates it to Kilo.
func t3CodeNativeClaudeSettings(data []byte, policy t3CodeModelPolicy, initial string, port int) ([]byte, error) {
	settings, err := decodeClaudeSettings(data)
	if err != nil {
		return nil, err
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(settings["env"], &env); err != nil {
		return nil, err
	}
	for name := range env {
		if strings.HasPrefix(name, "ANTHROPIC_DEFAULT_") {
			delete(env, name)
		}
	}
	set := func(name, value string) { env[name], _ = json.Marshal(value) }
	set("ANTHROPIC_BASE_URL", "http://127.0.0.1:"+strconv.Itoa(port)+"/t3-code")
	for _, family := range []string{"opus", "sonnet", "fable", "haiku"} {
		// The first selected model in each family is deterministic. Every
		// version-qualified slug still routes through its exact prepared ID.
		for _, model := range policy.ClaudeLibrary.Models {
			native := t3CodeNativeClaudeModelID(model.ID)
			if strings.HasPrefix(native, "claude-"+family+"-") {
				set("ANTHROPIC_DEFAULT_"+strings.ToUpper(family)+"_MODEL", native)
				break
			}
		}
	}
	if native := t3CodeNativeClaudeModelID(initial); native != "" && policy.ClaudeRoutes[native] == initial {
		initial = native
	}
	set("ANTHROPIC_MODEL", initial)
	settings["model"], _ = json.Marshal(initial)
	settings["env"], _ = json.Marshal(env)
	// Saved defaults use T3's native spelling as well. No replacement picker
	// or global effort can override a level chosen for an individual turn.
	var levels map[string]json.RawMessage
	if json.Unmarshal(settings["modelSettings"], &levels) == nil {
		for _, model := range policy.ClaudeLibrary.Models {
			if model.ReasoningEffort == "" || !validClaudeEffort(model.ID, model.ReasoningEffort) {
				continue
			}
			if native := t3CodeNativeClaudeModelID(model.ID); native != "" {
				levels[native], _ = json.Marshal(map[string]string{"effortLevel": model.ReasoningEffort})
			}
		}
		settings["modelSettings"], _ = json.Marshal(levels)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	return append(encoded, '\n'), err
}

func t3CodeClaudePickerLibrary(library modelLibrary, routes map[string]string) modelLibrary {
	result := cloneModelLibrary(library)
	for i := range result.Models {
		id := result.Models[i].ID
		if native := t3CodeNativeClaudeModelID(id); native != "" && routes[native] == id {
			result.Models[i].ID = native
			if result.DefaultModel == id {
				result.DefaultModel = native
			}
		}
	}
	// Roles are informational and keyed on shared IDs, not picker identities.
	result.Roles = nil
	return result
}
