//go:build desktop

package main

import (
	"fmt"
	"slices"
	"strconv"

	"gioui.org/layout"
)

// Keep the user's requested policy separate from published catalog capacity.
// Only Custom writes a numeric window to models.json; presets remain portable
// when the provider changes the model's capacity.
func nativeLibraryItem(choice nativeModelChoice) modelLibraryItem {
	preset, tokens := choice.ContextPreset, choice.ContextTokens
	if preset == "" {
		preset, tokens = contextPolicyFromLibrary(modelLibraryItem{ContextWindow: choice.Model.ContextWindow})
	}
	if preset != contextPresetCustom {
		tokens = 0
	}
	return modelLibraryItem{
		ID: choice.Model.ID, DisplayName: choice.DisplayName,
		ReasoningEffort: choice.DefaultReasoning, ReasoningLevels: slices.Clone(choice.ReasoningLevels), ReasoningCustom: choice.ReasoningCustom,
		ContextPreset: preset, ContextWindow: tokens, MaxOutputTokens: choice.Model.MaxOutputTokens,
	}
}

func (u *nativeUI) contextCatalogModel(id string) modelInfo {
	for _, model := range u.models {
		if model.ID == id {
			return model
		}
	}
	return modelInfo{ID: id, Name: id}
}

func nativeContextTokens(tokens int) string {
	if tokens >= 1000000 && tokens%1000 == 0 {
		return strconv.FormatFloat(float64(tokens)/1000000, 'f', -1, 64) + "M"
	}
	if tokens >= 1000 && tokens%1000 == 0 {
		return strconv.Itoa(tokens/1000) + "K"
	}
	return strconv.Itoa(tokens)
}

func (u *nativeUI) contextPresetLabel(preset string) string {
	switch preset {
	case contextPresetRecommended:
		return u.tr("Recommended · 272K", "Recomendado · 272K")
	case contextPresetLow:
		return u.tr("Low · 128K", "Bajo · 128K")
	case contextPresetMaximum:
		return u.tr("Maximum", "Máximo")
	default:
		return u.tr("Custom", "Personalizado")
	}
}

func (u *nativeUI) contextChoiceSummary(choice nativeModelChoice) string {
	limits, err := contextPolicyForChoice(choice)
	if err != nil {
		if choice.ContextPreset == contextPresetMaximum && !limits.MaximumKnown {
			return u.tr("Maximum unavailable · refresh the catalog", "Máximo no disponible · actualiza el catálogo")
		}
		return u.tr("Enter a valid custom context window", "Introduce una ventana de contexto válida")
	}
	maximum := u.tr("unknown", "desconocido")
	if limits.MaximumKnown {
		maximum = nativeContextTokens(limits.MaximumContextWindow)
	}
	return fmt.Sprintf(u.tr("Working window: %s · Model maximum: %s", "Ventana de trabajo: %s · Máximo del modelo: %s"), nativeContextTokens(limits.ContextWindow), maximum)
}

func (u *nativeUI) setContextChoice(key string, choice *nativeModelChoice, preset string, tokens int) {
	choice.ContextPreset, choice.ContextTokens = preset, tokens
	if preset != contextPresetCustom {
		choice.ContextTokens = 0
	}
	u.setValue(nativeClientField(key, choice.Model.ID, "context"), strconv.Itoa(choice.ContextTokens))
}

func (u *nativeUI) applySharedContext(preset string, tokens int) bool {
	// Validate the entire operation first so a missing maximum cannot leave a
	// partially updated library. Persisted offline choices are still retained.
	for _, choice := range u.library.selection.Models {
		if _, err := resolveContextPolicy(preset, tokens, choice.Model.ContextWindow, choice.Model.MaxOutputTokens); err != nil {
			u.notice = err.Error()
			return false
		}
	}
	for i := range u.library.selection.Models {
		u.setContextChoice(sharedModelKey, &u.library.selection.Models[i], preset, tokens)
	}
	u.expanded["models.context.custom"] = false
	u.notice = ""
	return true
}

func (u *nativeUI) sharedContextPanel() layout.Widget {
	choices := u.library.selection.Models
	common := ""
	maximumAvailable := len(choices) > 0
	if len(choices) > 0 {
		common = nativeLibraryItem(choices[0]).ContextPreset
	}
	for _, choice := range choices {
		if nativeLibraryItem(choice).ContextPreset != common {
			common = ""
		}
		maximumAvailable = maximumAvailable && choice.Model.ContextWindow >= 1024
	}
	buttons := []layout.Widget{}
	for _, preset := range []string{contextPresetRecommended, contextPresetLow, contextPresetMaximum, contextPresetCustom} {
		preset := preset
		label := u.contextPresetLabel(preset)
		if common == preset {
			label = "● " + label
		}
		button := u.button("models.context."+preset, label, func() {
			if preset == contextPresetCustom {
				u.expanded["models.context.custom"] = !u.expanded["models.context.custom"]
				if u.value("models.context.tokens") == "" {
					tokens := contextRecommendedTokens
					if common == contextPresetCustom && len(choices) > 0 {
						tokens = choices[0].ContextTokens
					}
					u.setValue("models.context.tokens", strconv.Itoa(tokens))
				}
				return
			}
			u.applySharedContext(preset, 0)
		})
		buttons = append(buttons, u.disabled(len(choices) > 0 && (preset != contextPresetMaximum || maximumAvailable), button))
	}
	widgets := []layout.Widget{
		u.heading(u.tr("Context window", "Ventana de contexto")),
		u.note(u.tr("Apply to all saved models. New models start at Recommended; use Edit for a model override.", "Aplica a todos los modelos guardados. Los nuevos usan Recomendado; usa Editar para cambiar uno.")),
		u.pills(buttons...),
	}
	if u.expanded["models.context.custom"] {
		widgets = append(widgets, u.actionRow(u.field("models.context.tokens", u.tr("Custom tokens", "Tokens personalizados"), "272000", false), u.button("models.context.apply", u.tr("Apply to all models", "Aplicar a todos los modelos"), func() {
			tokens, err := strconv.Atoi(u.value("models.context.tokens"))
			if err != nil {
				u.notice = u.tr("Token limits must be whole numbers.", "Los límites de tokens deben ser números enteros.")
				return
			}
			u.applySharedContext(contextPresetCustom, tokens)
		})))
	}
	if !maximumAvailable {
		widgets = append(widgets, u.note(u.tr("Maximum needs a published limit for every model. Refresh the catalog, or set known models individually.", "Máximo necesita un límite publicado para cada modelo. Actualiza el catálogo o cambia los modelos conocidos individualmente.")))
	}
	widgets = append(widgets, u.note(u.tr("Windows are capped to model capacity. Changes apply when you reopen an agent.", "Las ventanas se ajustan a la capacidad del modelo. Los cambios se aplican al volver a abrir el agente.")))
	return u.card(widgets...)
}

func (u *nativeUI) contextChoiceControls(key string, choice *nativeModelChoice) layout.Widget {
	preset := nativeLibraryItem(*choice).ContextPreset
	buttons := []layout.Widget{}
	for _, option := range []string{contextPresetRecommended, contextPresetLow, contextPresetMaximum, contextPresetCustom} {
		option := option
		label := u.contextPresetLabel(option)
		if option == preset {
			label = "● " + label
		}
		button := u.button(nativeClientField(key, choice.Model.ID, "context-preset:"+option), label, func() {
			tokens := 0
			if option == contextPresetCustom {
				tokens = contextRecommendedTokens
				if limits, err := contextPolicyForChoice(*choice); err == nil {
					tokens = limits.ContextWindow
				}
			}
			u.setContextChoice(key, choice, option, tokens)
		})
		buttons = append(buttons, u.disabled(option != contextPresetMaximum || choice.Model.ContextWindow >= 1024, button))
	}
	widgets := []layout.Widget{u.note(u.tr("Context window", "Ventana de contexto")), u.pills(buttons...)}
	if preset == contextPresetCustom {
		widgets = append(widgets, u.field(nativeClientField(key, choice.Model.ID, "context"), u.tr("Custom tokens", "Tokens personalizados"), "272000", false))
	}
	return u.column(widgets...)
}
