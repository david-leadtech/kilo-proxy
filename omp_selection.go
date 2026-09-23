package main

import "slices"

// Keep the shared library's gateway IDs and editable limits. Only capabilities
// actually published for the model (or explicitly overridden by the user) are
// offered to Oh My Pi; its backend narrows these to its supported vocabulary.
func ompSelectionFromChoices(choices []nativeModelChoice, initial string) ompSelection {
	selection := ompSelection{Initial: initial}
	for _, choice := range choices {
		model := choice.Model
		if model.ContextWindow == 0 {
			model.ContextWindow = 200000
		}
		levels, effort := nativeReasoningFor(choice)
		reasoning := false
		for _, level := range levels {
			if level != "none" {
				reasoning = true
			}
		}
		if model.Reasoning != nil {
			reasoning = *model.Reasoning
		}
		if choice.ReasoningCustom && len(choice.ReasoningLevels) == 0 {
			reasoning = false
		}
		if !reasoning {
			levels, effort = nil, ""
		}
		name := choice.DisplayName
		if name == "" {
			name = model.Name
		}
		selection.Models = append(selection.Models, ompModel{
			editorModel:      editorModel{ID: model.ID, Name: name, Context: model.ContextWindow, Output: model.MaxOutputTokens},
			Reasoning:        reasoning,
			ReasoningEfforts: slices.Clone(levels),
			Effort:           effort,
			InputModalities:  slices.Clone(model.InputModalities),
			InputPrice:       model.InputPrice,
			OutputPrice:      model.OutputPrice,
		})
	}
	return selection
}
