//go:build desktop

package main

import (
	"strings"

	"gioui.org/layout"
)

// BYOK is an observation of the current Kilo account's catalog, not a model
// capability or a saved preference. Never reuse a selected model's old flag.
func nativeModelHasUserByok(m modelInfo, currentIDs map[string]bool) bool {
	return currentIDs[m.ID] && !strings.HasPrefix(m.ID, "chatgpt/") && (m.Connection == "" || m.Connection == "kilo")
}

// Build once for a catalog render: classifying a large catalog must stay linear.
func (u *nativeUI) modelByokIDs() map[string]bool {
	ids := map[string]bool{}
	if !nativeBool(u.state, "kiloReady") || u.catalogCached {
		return ids
	}
	for _, m := range u.models {
		if m.Connection == "kilo" && !strings.HasPrefix(m.ID, "chatgpt/") && m.HasUserByokAvailable != nil && *m.HasUserByokAvailable {
			ids[m.ID] = true
		}
	}
	return ids
}

func (u *nativeUI) modelByokBadge(m modelInfo, currentIDs map[string]bool) layout.Widget {
	if !nativeModelHasUserByok(m, currentIDs) {
		return nil
	}
	return u.uxmTag("BYOK", nativeIvory)
}

func (u *nativeUI) modelByokContext() string {
	if nativeString(u.state, "orgId") != "" {
		return u.tr("BYOK connection available for the selected team.", "Conexión BYOK disponible en el equipo seleccionado.")
	}
	return u.tr("BYOK connection available for the current Kilo account.", "Conexión BYOK disponible en la cuenta actual de Kilo.")
}
