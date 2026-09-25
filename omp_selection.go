package main

import "slices"

// Keep the shared library's gateway IDs and editable limits. Only capabilities
// actually published for the model (or explicitly overridden by the user) are
// offered to Oh My Pi; its backend narrows these to its supported vocabulary.
func ompSelectionFromChoices(choices []nativeModelChoice, initial string) (ompSelection, error) {
	selection := ompSelection{Initial: initial}
	for _, choice := range choices {
		model := choice.Model
		context, err := contextPolicyForChoice(choice)
		if err != nil {
			return selection, err
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
			editorModel:      editorModel{ID: model.ID, Name: name, Context: context.ContextWindow, Output: context.MaxOutputTokens},
			Reasoning:        reasoning,
			ReasoningEfforts: slices.Clone(levels),
			Effort:           effort,
			InputModalities:  slices.Clone(model.InputModalities),
			InputPrice:       model.InputPrice,
			OutputPrice:      model.OutputPrice,
		})
	}
	return selection, nil
}
