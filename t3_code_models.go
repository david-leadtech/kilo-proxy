package main

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
)

type t3CodeModelPolicy struct {
	CodexLibrary, ClaudeLibrary modelLibrary
	ClaudeRoutes                map[string]string
}

var t3CodeClaudeGatewayID = regexp.MustCompile(`^anthropic/(claude-(?:opus|sonnet|haiku|fable)-[0-9]+(?:[.-][0-9]+)?)(-[0-9]{8})?$`)

var t3CodeOfficialClaudeModels = []struct{ Slug, Name string }{
	{"claude-opus-5-5", "Claude Opus 5.5"},
	{"claude-sonnet-5-5", "Claude Sonnet 5.5"},
	{"claude-fable-5-1", "Claude Fable 5.1"},
}

// Effective selections belong to individual T3 instances. Deriving them never
// rewrites the shared library or the selections used by other integrations.
func deriveT3CodeModelPolicy(prefs t3CodePreferences, library modelLibrary, catalog []modelInfo) (t3CodeModelPolicy, error) {
	result := t3CodeModelPolicy{CodexLibrary: emptyModelLibrary(), ClaudeLibrary: emptyModelLibrary(), ClaudeRoutes: map[string]string{}}
	prefs = normalizeT3CodePreferences(prefs)
	if err := validateT3CodePreferences(prefs); err != nil {
		return result, err
	}
	if err := validateModelLibrary(library); err != nil {
		return result, err
	}
	if prefs.KiloCodex {
		if prefs.CodexModels == "shared" {
			result.CodexLibrary = cloneModelLibrary(library)
		} else {
			for _, model := range library.Models {
				if !strings.HasPrefix(model.ID, "openai/") && !strings.HasPrefix(model.ID, "chatgpt/") {
					continue
				}
				model.ReasoningLevels = append([]string(nil), model.ReasoningLevels...)
				result.CodexLibrary.Models = append(result.CodexLibrary.Models, model)
			}
			result.CodexLibrary.DefaultModel = t3CodeEffectiveDefault(result.CodexLibrary, library.DefaultModel)
			if len(result.CodexLibrary.Models) == 0 {
				return result, errors.New("Select at least one OpenAI or ChatGPT model in Models, choose shared models, or disable Codex · Kilo.")
			}
		}
		if err := validateT3CodeEffectiveLibrary(result.CodexLibrary, "Codex", prefs.CodexModels); err != nil {
			return result, err
		}
	}
	if prefs.KiloClaude {
		if prefs.ClaudeModels == "shared" {
			result.ClaudeLibrary = cloneModelLibrary(library)
		} else {
			for _, official := range t3CodeOfficialClaudeModels {
				var match *modelInfo
				for i := range catalog {
					parts := t3CodeClaudeGatewayID.FindStringSubmatch(catalog[i].ID)
					if parts == nil || strings.ReplaceAll(parts[1], ".", "-") != official.Slug {
						continue
					}
					if match != nil && match.ID != catalog[i].ID {
						return result, fmt.Errorf("T3 Code Claude has multiple gateway IDs for %s. Choose an exact shared selection instead.", official.Name)
					}
					match = &catalog[i]
				}
				if match == nil {
					return result, fmt.Errorf("%s is unavailable in the connected catalog. Refresh the catalog, choose shared models, or disable Claude · Kilo.", official.Name)
				}
				result.ClaudeLibrary.Models = append(result.ClaudeLibrary.Models, t3CodeCatalogLibraryItem(*match, library))
			}
			result.ClaudeLibrary.DefaultModel = t3CodeEffectiveDefault(result.ClaudeLibrary, library.DefaultModel)
		}
		if err := validateT3CodeEffectiveLibrary(result.ClaudeLibrary, "Claude", prefs.ClaudeModels); err != nil {
			return result, err
		}
		var err error
		result.ClaudeRoutes, err = t3CodeBuildClaudeRoutes(result.ClaudeLibrary)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func validateT3CodeEffectiveLibrary(library modelLibrary, provider, mode string) error {
	if len(library.Models) == 0 {
		return fmt.Errorf("No %s models are available for T3 Code in %s mode. Refresh the catalog, choose shared models, or disable %s · Kilo.", provider, mode, provider)
	}
	if len(library.Models) > t3CodeCustomModelLimit {
		return fmt.Errorf("T3 Code %s supports at most %d models. Choose a shared selection with fewer models instead.", provider, t3CodeCustomModelLimit)
	}
	return validateT3CodeLibrary(library)
}

func t3CodeCatalogLibraryItem(model modelInfo, library modelLibrary) modelLibraryItem {
	for _, selected := range library.Models {
		if selected.ID == model.ID {
			selected.ReasoningLevels = append([]string(nil), selected.ReasoningLevels...)
			return selected
		}
	}
	// Catalog capabilities remain metadata, resolved by terminalLibraryChoices.
	// Do not invent a reasoning default or turn advertised levels into overrides.
	return modelLibraryItem{ID: model.ID, ContextPreset: contextPresetRecommended}
}

func t3CodeEffectiveDefault(library modelLibrary, preferred string) string {
	for _, model := range library.Models {
		if model.ID == preferred {
			return preferred
		}
	}
	if len(library.Models) > 0 {
		return library.Models[0].ID
	}
	return ""
}

func t3CodeNativeClaudeModelID(id string) string {
	parts := t3CodeClaudeGatewayID.FindStringSubmatch(id)
	if parts == nil {
		return ""
	}
	return strings.ReplaceAll(parts[1], ".", "-")
}

// A version alias can only resolve to one prepared gateway ID. In particular,
// dotted/hyphenated IDs and different dated snapshots may not silently collide.
func t3CodeBuildClaudeRoutes(library modelLibrary) (map[string]string, error) {
	routes := map[string]string{}
	selected := map[string]bool{}
	for _, model := range library.Models {
		selected[model.ID] = true
	}
	for _, model := range library.Models {
		parts := t3CodeClaudeGatewayID.FindStringSubmatch(model.ID)
		if parts == nil {
			continue
		}
		native := t3CodeNativeClaudeModelID(model.ID)
		for _, alias := range []string{native, native + parts[2]} {
			if selected[alias] && alias != model.ID || routes[alias] != "" && routes[alias] != model.ID {
				return nil, errors.New("T3 Code Claude has ambiguous native model IDs. Choose one exact gateway ID per Claude family/version.")
			}
			routes[alias] = model.ID
		}
	}
	return routes, nil
}

func validateT3CodeClaudeRoutes(library modelLibrary, routes map[string]string) error {
	if err := validateT3CodeLibrary(library); err != nil {
		return err
	}
	want, err := t3CodeBuildClaudeRoutes(library)
	if err != nil {
		return err
	}
	if len(want) != len(routes) || len(want) > 0 && !reflect.DeepEqual(want, routes) {
		return errors.New("The prepared T3 Code Claude model routes are invalid. Prepare T3 Code again.")
	}
	return nil
}
