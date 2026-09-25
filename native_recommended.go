//go:build desktop

package main

import (
	"fmt"
	"slices"

	"gioui.org/layout"
)

// recommendedModels lists catalog models marked by the published
// recommendations, in list order. Models the team cannot use never appear.
func (u *nativeUI) recommendedModels() []modelInfo {
	models := []modelInfo{}
	for _, m := range u.models {
		if m.Recommendation != nil {
			models = append(models, m)
		}
	}
	slices.SortFunc(models, func(a, b modelInfo) int { return a.Recommendation.Rank - b.Recommendation.Rank })
	return models
}

// addRecommendedModels adds the given models to the shared library. An empty
// library starts with the published default; an existing default is kept.
func (u *nativeUI) addRecommendedModels(models []modelInfo) {
	s := u.library.selection
	wasEmpty := len(s.Models) == 0
	for _, m := range models {
		if s.choice(m.ID) != nil {
			continue
		}
		if err := s.add(m, 50); err != nil {
			u.noticeError(err)
			break
		}
		choice := s.choice(m.ID)
		u.seedClientChoice(sharedModelKey, *choice)
		// A published level applies only when this model offers it.
		if levels, _ := nativeReasoningFor(*choice); m.Recommendation.Reasoning != "" && helperContains(levels, m.Recommendation.Reasoning) {
			u.setValue(nativeClientField(sharedModelKey, m.ID, "reasoning"), m.Recommendation.Reasoning)
		}
	}
	if !wasEmpty {
		return
	}
	for _, m := range models {
		if m.Recommendation.Default && s.choice(m.ID) != nil {
			s.Initial = m.ID
		}
	}
}

func (u *nativeUI) recommendedPanel(models []modelInfo) layout.Widget {
	s := u.library.selection
	missing := 0
	cards, ids := []layout.Widget{}, []string{}
	for _, model := range models {
		m := model
		id := m.ID
		added := s.choice(id) != nil
		if !added {
			missing++
		}
		checkID := "recommended:choose:" + id
		u.setChecked(checkID, added)
		label := m.Name
		if label == "" {
			label = id
		}
		note := m.Recommendation.NoteEN
		if u.language == "es" && m.Recommendation.NoteES != "" {
			note = m.Recommendation.NoteES
		}
		heading := []layout.Widget{u.eyebrow(modelLabLabel(modelLab(m))), u.check(checkID, label, func(checked bool) {
			if checked {
				u.addRecommendedModels([]modelInfo{m})
			} else {
				s.remove(id)
			}
		})}
		if note != "" {
			heading = append(heading, u.note(note))
		}
		row := []layout.Widget{u.column(heading...), u.modelPriceCells(m)}
		if r := m.Recommendation; r.Default {
			text := u.tr("Suggested default", "Predeterminado sugerido")
			if r.Reasoning != "" {
				text += fmt.Sprintf(u.tr(" · reasoning %s", " · razonamiento %s"), r.Reasoning)
			}
			row = append(row, u.message(nativeToneInfo, text))
		}
		if added {
			// Choose the default where the model was picked; setup shows no library cards.
			action := u.libraryDefaultBadge()
			if s.Initial != id {
				action = u.iconButton("recommended:initial:"+id, u.tr("Use by default", "Usar por defecto"), nativeButtonGhost, nativeIconStarOutline, func() { s.Initial = id })
			}
			row = append(row, u.pills(action))
		}
		cards = append(cards, u.modelCard(added, row...))
		ids = append(ids, id)
	}
	addKind := nativeButtonSecondary
	if len(s.Models) == 0 {
		addKind = nativeButtonPrimary
	}
	label := fmt.Sprintf(u.tr("Add all (%d)", "Añadir todos (%d)"), missing)
	if missing == 0 {
		label = u.tr("All added", "Todos añadidos")
	}
	add := u.disabled(missing > 0, u.buttonKind("recommended.add-all", label, addKind, nativeIconAdd, false, func() { u.addRecommendedModels(models) }))
	top := u.actionRow(u.column(
		u.heading(u.tr("Recommended models", "Modelos recomendados")),
		u.note(u.tr("Frontier OpenAI and Anthropic models plus the most popular Chinese models. Only models your team can use are shown.", "Modelos frontera de OpenAI y Anthropic y los modelos chinos más populares. Solo se muestran los que tu equipo puede usar.")),
	), add)
	return u.column(top, u.modelGridLayout("recommended.models", ids, cards, false))
}

// sharedDefaultPicker chooses the default among every selected model, including
// ones picked from the full catalog, where cards offer no default action.
func (u *nativeUI) sharedDefaultPicker() layout.Widget {
	s := u.library.selection
	name := s.Initial
	if choice := s.choice(s.Initial); choice != nil {
		name = nativeCodexDisplayName(*choice)
	}
	const id = "setup.default"
	children := []layout.Widget{u.label(u.tr("Default model", "Modelo predeterminado")), u.dropdownButton(id+".toggle", name, func() { u.expanded[id] = !u.expanded[id] })}
	if u.expanded[id] {
		for _, choice := range s.Models {
			modelID := choice.Model.ID
			children = append(children, u.menuItem(id+".option."+modelID, nativeCodexDisplayName(choice), modelID == s.Initial, func() {
				s.Initial = modelID
				u.expanded[id] = false
			}))
		}
	}
	return u.column(children...)
}
