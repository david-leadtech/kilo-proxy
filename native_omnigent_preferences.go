//go:build desktop

package main

import (
	"bytes"
	"encoding/json"
	"errors"

	"gioui.org/layout"
)

const nativeOmnigentPreferencesEndpoint = "/api/clients/omnigent/preferences"

type nativeOmnigentPreferences struct {
	Version          int    `json:"version"`
	Codex            bool   `json:"codex"`
	Claude           bool   `json:"claude"`
	CodexConnection  string `json:"codexConnection"`
	ClaudeConnection string `json:"claudeConnection"`
	CodexModels      string `json:"codexModels"`
	ClaudeModels     string `json:"claudeModels"`
}

// Connections and enabled flags must be explicit in an acknowledged snapshot.
func (p *nativeOmnigentPreferences) UnmarshalJSON(data []byte) error {
	type plain nativeOmnigentPreferences
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"codex", "claude"} {
		raw, ok := fields[key]
		if !ok || string(bytes.TrimSpace(raw)) != "true" && string(bytes.TrimSpace(raw)) != "false" {
			return errors.New("Invalid Omnigent account selection.")
		}
	}
	*p = nativeOmnigentPreferences(next)
	return nil
}

type nativeOmnigentPreferencesResponse struct {
	Preferences         nativeOmnigentPreferences `json:"preferences"`
	RequiresPreparation bool                      `json:"requiresPreparation"`
}

type nativeOmnigentPreferencesState struct {
	nativeOmnigentPreferencesResponse
	Requested, Loaded bool
	Generation        uint64
	Error             string
	Pending           *nativeOmnigentPreferences
}

func validNativeOmnigentPreferences(p nativeOmnigentPreferences) bool {
	return p.Version == 1 && (p.CodexConnection == "native" || p.CodexConnection == "kilo") && (p.ClaudeConnection == "native" || p.ClaudeConnection == "kilo") && (p.CodexModels == "official" || p.CodexModels == "shared") && (p.ClaudeModels == "official" || p.ClaudeModels == "shared")
}

func nativeOmnigentUsesKilo(p nativeOmnigentPreferences) bool {
	return p.Codex && p.CodexConnection == "kilo" || p.Claude && p.ClaudeConnection == "kilo"
}

func (u *nativeUI) omnigentNativeOnly() bool {
	s := &u.agentsState().Omnigent.PreferencesState
	return s.Loaded && validNativeOmnigentPreferences(s.Preferences) && !nativeOmnigentUsesKilo(s.Preferences)
}

func (u *nativeUI) omnigentPreferencesBusy() bool {
	return u.busy["POST"+nativeOmnigentPreferencesEndpoint]
}

func (u *nativeUI) refreshOmnigentPreferencesAfter(done func(error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	s := &u.agentsState().Omnigent.PreferencesState
	if u.busy["GET"+nativeOmnigentPreferencesEndpoint] || u.omnigentPreferencesBusy() {
		finish(errors.New(u.tr("Wait for Omnigent settings to finish loading or saving.", "Espera a que terminen de cargarse o guardarse los ajustes de Omnigent.")))
		return
	}
	s.Requested, s.Error = true, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("GET", nativeOmnigentPreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		var response nativeOmnigentPreferencesResponse
		if json.Unmarshal(raw, &response) != nil || !validNativeOmnigentPreferences(response.Preferences) {
			s.Error = u.tr("Could not read Omnigent preferences. Reload settings before preparing.", "No se pudieron leer las preferencias de Omnigent. Carga los ajustes antes de preparar.")
			finish(errors.New(s.Error))
			return
		}
		changed := s.Loaded && s.Preferences != response.Preferences
		s.nativeOmnigentPreferencesResponse, s.Loaded = response, true
		if changed {
			u.refreshOmnigentLaunchDiscovery()
		}
		finish(nil)
	})
}

func (u *nativeUI) saveOmnigentPreferences(next nativeOmnigentPreferences) {
	agent := &u.agentsState().Omnigent
	s := &agent.PreferencesState
	if !s.Loaded || u.omnigentPreferencesBusy() || !validNativeOmnigentPreferences(next) {
		return
	}
	s.Pending, s.Error = &next, ""
	s.Generation++
	generation := s.Generation
	agent.Generation++
	agent.Info.Prepared = false
	u.clientRequest("POST", nativeOmnigentPreferencesEndpoint, next, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			return
		}
		var response nativeOmnigentPreferencesResponse
		if json.Unmarshal(raw, &response) != nil || !validNativeOmnigentPreferences(response.Preferences) || response.Preferences != next {
			s.Error = u.tr("Omnigent did not confirm the saved preferences. Reload settings before preparing.", "Omnigent no confirmó las preferencias guardadas. Carga los ajustes antes de preparar.")
			return
		}
		s.nativeOmnigentPreferencesResponse, s.Pending, s.Loaded = response, nil, true
		s.RequiresPreparation = true
		u.refreshOmnigentLaunchDiscovery()
		u.refreshOmnigentProfile()
	})
}

func (u *nativeUI) omnigentPreferencesPanel() layout.Widget {
	s := &u.agentsState().Omnigent.PreferencesState
	if !s.Requested {
		u.refreshOmnigentPreferencesAfter(nil)
	}
	checking, saving := u.busy["GET"+nativeOmnigentPreferencesEndpoint], u.omnigentPreferencesBusy()
	ready := s.Loaded && !checking && !saving && s.Pending == nil && !u.agentsState().Omnigent.Info.Running && u.agentsState().Omnigent.WatchUntil.IsZero()
	status := u.tr("Choose one connection for each enabled agent. Changes save automatically.", "Elige una conexión para cada agente activado. Los cambios se guardan solos.")
	if checking || !s.Loaded {
		status = u.tr("Loading Omnigent settings…", "Cargando ajustes de Omnigent…")
	}
	if saving {
		status = u.tr("Saving Omnigent settings…", "Guardando ajustes de Omnigent…")
	}
	widgets := []layout.Widget{u.note(status)}
	if s.Error != "" {
		widgets = append(widgets, u.message(nativeToneError, s.Error))
		if s.Pending != nil {
			widgets = append(widgets, u.disabled(!saving, u.button("omnigent-preferences:retry", u.tr("Retry saving", "Reintentar guardado"), func() {
				if s.Pending != nil {
					u.saveOmnigentPreferences(*s.Pending)
				}
			})))
		}
	}
	if s.RequiresPreparation {
		widgets = append(widgets, u.hint(u.tr("Stop Omnigent · Kilo and prepare again to apply these settings. Start a new chat after changing a connection.", "Detén Omnigent · Kilo y prepara de nuevo para aplicar estos ajustes. Inicia un chat nuevo tras cambiar una conexión.")))
	}
	rows := []layout.Widget{u.uxsRow(u.tr("Separate workspace", "Espacio separado"), "", u.column(widgets...), u.disabled(!saving && !checking, u.button("omnigent-preferences:reload", u.tr("Reload settings", "Cargar ajustes"), func() { s.Pending = nil; u.refreshOmnigentPreferencesAfter(nil) })), nil)}
	p := s.Preferences
	if saving && s.Pending != nil {
		p = *s.Pending
	}
	for _, kind := range []string{"codex", "claude"} {
		kind := kind
		label, enabled, connection, scope := "Codex", p.Codex, p.CodexConnection, p.CodexModels
		if kind == "claude" {
			label, enabled, connection, scope = "Claude", p.Claude, p.ClaudeConnection, p.ClaudeModels
		}
		rows = append(rows, u.uxsRow(label, "", u.disabled(ready, u.uxsToggle("omnigent-preferences:"+kind, label, u.tr("Enable this agent", "Activar este agente"), enabled, func(value bool) {
			next := p
			if kind == "codex" {
				next.Codex = value
			} else {
				next.Claude = value
			}
			u.saveOmnigentPreferences(next)
		})), nil, nil))
		choices := []nativeChoice{{Value: "native", Label: u.tr("Own account", "Cuenta propia")}, {Value: "kilo", Label: "Kilo Proxy"}}
		rows = append(rows, u.uxsRow(label+u.tr(" connection", " · conexión"), "", u.uxsRadioList("omnigent-preferences:"+kind+"-connection:", choices, connection, ready && enabled, func(value string) {
			next := p
			if kind == "codex" {
				next.CodexConnection = value
			} else {
				next.ClaudeConnection = value
			}
			u.saveOmnigentPreferences(next)
		}), nil, nil))
		official := u.tr("OpenAI and ChatGPT", "OpenAI y ChatGPT")
		if kind == "claude" {
			official = u.tr("Opus 5.5, Sonnet 5.5 and Fable 5.1", "Opus 5.5, Sonnet 5.5 y Fable 5.1")
		}
		rows = append(rows, u.uxsRow(label+u.tr(" Kilo models", " · modelos Kilo"), "", u.uxsRadioList("omnigent-preferences:"+kind+"-models:", []nativeChoice{{Value: "official", Label: official}, {Value: "shared", Label: u.tr("All shared models", "Todos los modelos compartidos")}}, scope, ready && enabled && connection == "kilo", func(value string) {
			next := p
			if kind == "codex" {
				next.CodexModels = value
			} else {
				next.ClaudeModels = value
			}
			u.saveOmnigentPreferences(next)
		}), nil, nil))
	}
	rows = append(rows, uxhCell(u.note(u.tr("Kilo connections use the default model and compatible reasoning saved in Models. Own accounts keep their CLI login and native catalog. Executable paths are shared with the CLI installation settings.", "Las conexiones Kilo usan el modelo predeterminado y el razonamiento compatible guardados en Modelos. Las cuentas propias conservan su sesión CLI y su catálogo nativo. Las rutas de los ejecutables se comparten con los ajustes de instalación de las CLI."))))
	return u.uxsBlock(u.tr("Accounts and models", "Cuentas y modelos"), "", rows...)
}
