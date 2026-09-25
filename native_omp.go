//go:build desktop

package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
)

func nativeOMPSelection(source *nativeClientSelection) (ompSelection, error) {
	return ompSelectionFromChoices(source.Models, source.Initial)
}

func nativeOMPProfileDir(path string) string {
	if filepath.Base(path) == "models.yml" {
		return filepath.Dir(path)
	}
	return path
}

func decodeNativeOMPSelection(data []byte, catalog []modelInfo) (*nativeClientSelection, error) {
	var source struct {
		Selection  ompSelection `json:"selection"`
		ConfigPath string       `json:"configPath"`
		ProfileDir string       `json:"profileDir"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		return nil, err
	}
	if err := validateOMPSelection(source.Selection); err != nil {
		return nil, err
	}
	selection := &nativeClientSelection{Initial: source.Selection.Initial, Path: source.ConfigPath, Aliases: map[string]string{}, Mode: "installed"}
	if selection.Path == "" {
		selection.Path = source.ProfileDir
	}
	for _, saved := range source.Selection.Models {
		model := modelInfo{ID: saved.ID, Name: saved.ID}
		for _, current := range catalog {
			if current.ID == saved.ID {
				model = current
				break
			}
		}
		maximumOutput := model.MaxOutputTokens
		model.MaxOutputTokens = saved.Output
		model.Reasoning = new(bool)
		*model.Reasoning = saved.Reasoning
		model.ReasoningEfforts = slices.Clone(saved.ReasoningEfforts)
		model.InputModalities = slices.Clone(saved.InputModalities)
		model.InputPrice, model.OutputPrice = saved.InputPrice, saved.OutputPrice
		selection.Models = append(selection.Models, nativeModelChoice{Model: model, DisplayName: saved.Name, DefaultReasoning: saved.Effort, ReasoningCustom: true, ReasoningLevels: slices.Clone(saved.ReasoningEfforts), ContextPreset: contextPresetCustom, ContextTokens: saved.Context, MaximumOutputTokens: maximumOutput})
	}
	return selection, nil
}
