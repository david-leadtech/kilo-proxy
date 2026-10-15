package main

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type omnigentModelPolicy struct{ CodexLibrary, ClaudeLibrary modelLibrary }

// Native clients send a selection, without the shared library's pack provenance.
// Compare its effective policy while retaining the exact saved library for the
// prepared profile and its later change detection.
func omnigentSameModelSelection(left, right modelLibrary) bool {
	if validateModelLibrary(left) != nil || validateModelLibrary(right) != nil {
		return false
	}
	canonical := func(library modelLibrary) modelLibrary {
		result := cloneModelLibrary(library)
		result.StartingPack, result.Roles = "", nil
		for i, item := range result.Models {
			// The UI presents these unique choices in its standard level order.
			// Their membership is policy; the order of selected models is retained.
			sort.Strings(result.Models[i].ReasoningLevels)
			preset, tokens := contextPolicyFromLibrary(item)
			if preset != contextPresetCustom {
				tokens = 0
			}
			result.Models[i].ContextPreset, result.Models[i].ContextWindow = preset, tokens
		}
		return result
	}
	return reflect.DeepEqual(canonical(left), canonical(right))
}

func cloneOmnigentLibrary(library modelLibrary) modelLibrary {
	result := cloneModelLibrary(library)
	for i, item := range library.Models {
		if item.ReasoningLevels != nil {
			result.Models[i].ReasoningLevels = append([]string{}, item.ReasoningLevels...)
		}
	}
	return result
}

func deriveOmnigentModelPolicy(p omnigentPreferences, library modelLibrary, catalog []modelInfo) (omnigentModelPolicy, error) {
	result := omnigentModelPolicy{emptyModelLibrary(), emptyModelLibrary()}
	if err := validateOmnigentPreferences(p); err != nil {
		return result, err
	}
	if err := validateModelLibrary(library); err != nil {
		return result, err
	}
	if p.Codex && p.CodexConnection == "kilo" {
		result.CodexLibrary = cloneOmnigentLibrary(library)
		if p.CodexModels == "official" {
			result.CodexLibrary = emptyModelLibrary()
			for _, item := range library.Models {
				if strings.HasPrefix(item.ID, "openai/") || strings.HasPrefix(item.ID, "chatgpt/") {
					result.CodexLibrary.Models = append(result.CodexLibrary.Models, item)
				}
			}
			result.CodexLibrary.DefaultModel = t3CodeEffectiveDefault(result.CodexLibrary, library.DefaultModel)
		}
		if len(result.CodexLibrary.Models) == 0 {
			return result, errors.New("Choose Codex models in the shared library, or use its native connection.")
		}
	}
	if p.Claude && p.ClaudeConnection == "kilo" {
		result.ClaudeLibrary = cloneOmnigentLibrary(library)
		if p.ClaudeModels == "official" {
			result.ClaudeLibrary = emptyModelLibrary()
			for _, official := range t3CodeOfficialClaudeModels {
				var found *modelInfo
				for i := range catalog {
					parts := t3CodeClaudeGatewayID.FindStringSubmatch(catalog[i].ID)
					if parts == nil || strings.ReplaceAll(parts[1], ".", "-") != official.Slug {
						continue
					}
					if found != nil && found.ID != catalog[i].ID {
						return result, fmt.Errorf("Several gateway IDs match %s. Choose exact shared models for Omnigent.", official.Name)
					}
					found = &catalog[i]
				}
				if found == nil {
					return result, fmt.Errorf("%s is unavailable. Refresh the catalog or choose shared models for Omnigent.", official.Name)
				}
				result.ClaudeLibrary.Models = append(result.ClaudeLibrary.Models, t3CodeCatalogLibraryItem(*found, library))
			}
			result.ClaudeLibrary.DefaultModel = t3CodeEffectiveDefault(result.ClaudeLibrary, library.DefaultModel)
		}
		if len(result.ClaudeLibrary.Models) == 0 {
			return result, errors.New("Choose Claude models in the shared library, or use its native connection.")
		}
	}
	return result, nil
}

type omnigentModelSummary struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	IsDefault       bool     `json:"isDefault"`
	ReasoningEffort string   `json:"reasoningEffort,omitempty"`
	ReasoningLevels []string `json:"reasoningLevels"`
}

func omnigentModelSummaries(library modelLibrary, catalog []modelInfo) []omnigentModelSummary {
	result := []omnigentModelSummary{}
	for _, choice := range terminalLibraryChoices(library, catalog) {
		levels, effort := nativeReasoningFor(choice)
		name := choice.Model.Name
		if choice.DisplayName != "" {
			name = choice.DisplayName
		}
		if name == "" {
			name = choice.Model.ID
		}
		result = append(result, omnigentModelSummary{choice.Model.ID, name, choice.Model.ID == library.DefaultModel, effort, append([]string{}, levels...)})
	}
	return result
}
