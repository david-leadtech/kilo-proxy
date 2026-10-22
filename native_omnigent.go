//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gioui.org/layout"
)

type nativeOmnigentModelSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	IsDefault       bool   `json:"isDefault"`
	ReasoningEffort string `json:"reasoningEffort"`
}
type nativeOmnigentInfo struct {
	Prepared              bool                         `json:"prepared"`
	Running               bool                         `json:"running"`
	Library               *modelLibrary                `json:"library"`
	Version               string                       `json:"version"`
	Fingerprint           string                       `json:"fingerprint"`
	Message               string                       `json:"message"`
	Preferences           nativeOmnigentPreferences    `json:"preferences"`
	EffectiveCodexModels  []nativeOmnigentModelSummary `json:"effectiveCodexModels"`
	EffectiveClaudeModels []nativeOmnigentModelSummary `json:"effectiveClaudeModels"`
}
type nativeOmnigentState struct {
	Info                  nativeOmnigentInfo
	Requested, Loaded     bool
	Generation            uint64
	Connection            [32]byte
	Error                 string
	PreferencesState      nativeOmnigentPreferencesState
	WatchUntil, WatchNext time.Time
}

func (u *nativeUI) acceptOmnigentProfile(data json.RawMessage, connection [32]byte) {
	s := &u.agentsState().Omnigent
	s.Generation++
	s.Requested, s.Loaded, s.Connection = true, true, connection
	s.Info, s.Error = nativeOmnigentInfo{}, ""
	if json.Unmarshal(data, &s.Info) != nil {
		s.Error = u.tr("Could not read Omnigent settings. Refresh to try again.", "No se pudieron leer los ajustes de Omnigent. Actualiza para reintentarlo.")
	}
	if !s.Info.Running && s.Info.Message == "The private Omnigent workspace could not start. Check the selected installation and open it again." {
		s.WatchUntil = time.Time{}
	}
	if s.Info.Prepared && s.Info.Preferences == s.PreferencesState.Preferences {
		s.PreferencesState.RequiresPreparation = false
	}
}

// Observe only the bounded startup interval. A successful launch acknowledgement
// precedes the supervisor's server/runner readiness and any delayed failure.
func (u *nativeUI) omnigentLaunchConfirmed() {
	s := &u.agentsState().Omnigent
	now := time.Now()
	s.WatchUntil, s.WatchNext = now.Add(50*time.Second), now
	u.pollOmnigentLaunchStatus(now)
}

func (u *nativeUI) pollOmnigentLaunchStatus(now time.Time) {
	if u.agents == nil {
		return
	}
	s := &u.agents.Omnigent
	if s.WatchUntil.IsZero() || !now.Before(s.WatchUntil) {
		s.WatchUntil = time.Time{}
		return
	}
	if now.Before(s.WatchNext) || u.busy["GET/api/clients/omnigent"] || u.busy["POST/api/clients/omnigent/stop"] {
		return
	}
	s.WatchNext = now.Add(time.Second)
	u.refreshOmnigentProfile()
}

func (u *nativeUI) refreshOmnigentProfile() {
	const endpoint = "/api/clients/omnigent"
	if u.busy["GET"+endpoint] {
		return
	}
	s := &u.agentsState().Omnigent
	s.Requested = true
	s.Generation++
	generation, connection := s.Generation, u.launchConnectionFingerprint()
	u.clientRequest("GET", endpoint, nil, func(data json.RawMessage, err error) {
		if s.Generation != generation || !u.omnigentNativeOnly() && connection != u.launchConnectionFingerprint() {
			return
		}
		if err != nil {
			s.Info, s.Loaded, s.Error = nativeOmnigentInfo{}, true, nativeMessage(err.Error(), u.language)
			return
		}
		u.acceptOmnigentProfile(data, connection)
	})
}
func (u *nativeUI) omnigentPrepared(selection *nativeClientSelection) bool {
	s := &u.agentsState().Omnigent
	if !s.Loaded || !s.Info.Prepared || s.Info.Fingerprint == "" || !s.PreferencesState.Loaded || s.Info.Preferences != s.PreferencesState.Preferences || s.PreferencesState.Pending != nil || u.omnigentPreferencesBusy() {
		return false
	}
	if u.omnigentNativeOnly() {
		return true
	}
	if s.Info.Library == nil || s.Connection != u.launchConnectionFingerprint() {
		return false
	}
	payload, err := nativeClientPayload("omnigent", selection)
	return err == nil && omnigentSameModelSelection(payload.(map[string]any)["library"].(modelLibrary), *s.Info.Library)
}

func (u *nativeUI) prepareOmnigentAfter(done func(error)) {
	s := &u.agentsState().Omnigent
	prefs := &s.PreferencesState
	if u.omnigentPreferencesBusy() || prefs.Pending != nil || u.busy["GET"+nativeOmnigentPreferencesEndpoint] {
		done(errors.New(u.tr("Finish saving Omnigent settings before preparing.", "Termina de guardar los ajustes de Omnigent antes de preparar.")))
		return
	}
	if !prefs.Loaded {
		u.refreshOmnigentPreferencesAfter(func(err error) {
			if err != nil {
				done(err)
				return
			}
			u.prepareOmnigentAfter(done)
		})
		return
	}
	if !validNativeOmnigentPreferences(prefs.Preferences) {
		done(errors.New(u.tr("Invalid Omnigent preferences. Reload settings before preparing.", "Preferencias de Omnigent no válidas. Carga los ajustes antes de preparar.")))
		return
	}
	if !prefs.Preferences.Codex && !prefs.Preferences.Claude {
		done(errors.New(u.tr("Enable Codex or Claude before preparing Omnigent.", "Activa Codex o Claude antes de preparar Omnigent.")))
		return
	}
	expected := prefs.Preferences
	payload := map[string]any{"preferences": expected}
	if !u.omnigentNativeOnly() {
		if err := u.packReadinessError("omnigent"); err != nil {
			done(err)
			return
		}
		selection := u.sharedClientSelection("omnigent")
		u.syncClientSelection("omnigent", selection)
		value, err := nativeClientPayload("omnigent", selection)
		if err != nil {
			done(err)
			return
		}
		payload["library"] = value.(map[string]any)["library"]
	}
	before, connection := u.launchSettingsFingerprint("omnigent", ""), u.launchConnectionFingerprint()
	u.clientRequest("GET", nativeOmnigentPreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if err != nil {
			done(err)
			return
		}
		var current nativeOmnigentPreferencesResponse
		if json.Unmarshal(raw, &current) != nil || current.Preferences != expected || before != u.launchSettingsFingerprint("omnigent", "") {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		finish := func(data json.RawMessage, err error) {
			if err != nil {
				done(err)
				return
			}
			var confirmed nativeOmnigentInfo
			if json.Unmarshal(data, &confirmed) != nil || !confirmed.Prepared || confirmed.Fingerprint == "" || confirmed.Preferences != expected || prefs.Preferences != expected || prefs.Pending != nil || before != u.launchSettingsFingerprint("omnigent", "") {
				done(errors.New(u.launchSettingsChangedMessage()))
				return
			}
			if library, hasLibrary := payload["library"]; hasLibrary && (confirmed.Library == nil || !omnigentSameModelSelection(*confirmed.Library, library.(modelLibrary))) {
				done(errors.New(u.launchSettingsChangedMessage()))
				return
			}
			u.acceptOmnigentProfile(data, connection)
			selection := u.sharedClientSelection("omnigent")
			base, local, _ := u.clientBase()
			selection.Saved = nativeSelectionFingerprint("omnigent", selection, base, local, u.clientCaps("omnigent"))
			done(nil)
		}
		u.clientRequest("GET", "/api/clients/omnigent", nil, func(data json.RawMessage, err error) {
			if err != nil {
				done(err)
				return
			}
			var current nativeOmnigentInfo
			if json.Unmarshal(data, &current) != nil {
				done(errors.New(u.launchSettingsChangedMessage()))
				return
			}
			if current.Running {
				finish(data, nil)
				return
			}
			u.clientRequest("POST", "/api/clients/omnigent", payload, finish)
		})
	})
}

func (u *nativeUI) omnigentDetail(selection *nativeClientSelection) uxhDetail {
	s := &u.agentsState().Omnigent
	if !s.Requested {
		u.refreshOmnigentProfile()
	}
	if !s.PreferencesState.Requested {
		u.refreshOmnigentPreferencesAfter(nil)
	}
	_, validation := nativeClientPayload("omnigent", selection)
	status, libraryReady := u.libraryStatus()
	working := u.uxhProfileWorking("omnigent") || u.omnigentPreferencesBusy() || u.busy["GET"+nativeOmnigentPreferencesEndpoint] || !s.PreferencesState.Loaded || s.PreferencesState.Pending != nil
	canPrepare := s.WatchUntil.IsZero() && !s.Info.Running && libraryReady && u.agentConnectionReady() && !u.setupConnectionNeeded() && !u.connectionWorking() && validation == nil && !working
	if u.omnigentNativeOnly() {
		validation, libraryReady, canPrepare = nil, true, !working && !s.Info.Running && s.WatchUntil.IsZero()
	}
	if !s.PreferencesState.Preferences.Codex && !s.PreferencesState.Preferences.Claude {
		canPrepare = false
	}
	d := uxhDetail{canPrepare: &canPrepare, prepareNote: u.tr("Prepare or open Omnigent · Kilo to apply your saved accounts and models.", "Prepara o abre Omnigent · Kilo para aplicar las cuentas y modelos guardados.")}
	if u.omnigentPrepared(selection) {
		d.prepareNote = u.tr("Your saved Omnigent accounts and models are prepared.", "Las cuentas y modelos guardados de Omnigent están preparados.")
	}
	if validation != nil {
		d.status = append(d.status, u.hint(nativeMessage(validation.Error(), u.language)))
	}
	if !libraryReady {
		d.status = append(d.status, u.hint(status))
	}
	if s.Error != "" {
		d.status = append(d.status, u.hint(s.Error))
	} else if s.Info.Message != "" {
		d.status = append(d.status, u.note(nativeMessage(s.Info.Message, u.language)))
	}
	d.blocks = append(d.blocks, u.omnigentPreferencesPanel())
	if s.Info.Running {
		d.blocks = append(d.blocks, u.uxhBlock(u.tr("Running workspace", "Espacio en ejecución"), "", uxhCell(u.note(u.tr("Stop closes only the local Kilo Omnigent server and runner. The browser tab may remain open.", "Detener cierra sólo el servidor y runner locales de Omnigent Kilo. La pestaña del navegador puede quedar abierta."))), uxhCell(u.disabled(!u.busy["POST/api/clients/omnigent/stop"] && !u.omnigentPreferencesBusy() && u.clientState().Launching == "", u.button("omnigent:stop", u.tr("Stop Omnigent · Kilo", "Detener Omnigent · Kilo"), u.stopOmnigent)))))
	}
	rows := []layout.Widget{}
	p := s.PreferencesState.Preferences
	for _, account := range []struct {
		enabled          bool
		name, connection string
		models           []nativeOmnigentModelSummary
	}{{p.Codex, "Codex", p.CodexConnection, s.Info.EffectiveCodexModels}, {p.Claude, "Claude", p.ClaudeConnection, s.Info.EffectiveClaudeModels}} {
		if !account.enabled {
			continue
		}
		label, help := account.name, u.tr("Own CLI login and native catalog", "Sesión CLI propia y catálogo nativo")
		if account.connection == "kilo" {
			label = "Kilo Proxy · " + label
			help = u.tr("Shared models through the proxy", "Modelos compartidos a través del proxy")
		}
		rows = append(rows, u.uxhKV(label, u.uxhValue(help)))
		if account.connection == "kilo" && s.Info.Preferences == p {
			values := []string{}
			for _, m := range account.models {
				text := m.ID
				if m.Name != "" {
					text = m.Name + " · " + text
				}
				if m.IsDefault {
					text += u.tr(" · default", " · predeterminado")
				}
				if m.ReasoningEffort != "" {
					text += " · " + m.ReasoningEffort
				}
				values = append(values, text)
			}
			if len(values) > 0 {
				rows = append(rows, u.uxhKV(label+u.tr(" models", " · modelos"), u.uxhValue(strings.Join(values, "\n"))))
			}
		}
	}
	if len(rows) == 0 {
		rows = append(rows, uxhCell(u.note(u.tr("No agent is enabled. Choose Codex or Claude above.", "No hay ningún agente activado. Elige Codex o Claude arriba."))))
	}
	d.blocks = append(d.blocks, u.uxhBlock(u.tr("Accounts and prepared models", "Cuentas y modelos preparados"), "", rows...))
	d.advanced = append(d.advanced, u.uxhAdvancedRow(u.tr("How this works", "Cómo funciona"), u.tr("Local Omnigent server and runner with Codex and Claude, opened in your browser.", "Servidor y runner Omnigent locales con Codex y Claude, abiertos en tu navegador."), []layout.Widget{u.note(u.tr("Each agent uses one selected connection. Start a new chat after changing connections. Kilo models, their default and compatible reasoning come from Models; existing CLI logins stay with own accounts.", "Cada agente usa una conexión elegida. Inicia un chat nuevo tras cambiar de conexión. Los modelos Kilo, su predeterminado y el razonamiento compatible salen de Modelos; las sesiones CLI existentes se mantienen en las cuentas propias.")), u.note(u.tr("Codex and Claude use the custom or verified managed executables selected in Settings. An unavailable selected executable blocks opening. OpenCode and Muse are not included in this integration.", "Codex y Claude usan los ejecutables personalizados o las copias verificadas elegidos en Ajustes. Un ejecutable seleccionado no disponible bloquea la apertura. Esta integración no incluye OpenCode ni Muse."))}, u.button("omnigent:cli-settings", u.tr("Open executable settings", "Abrir ajustes de ejecutables"), func() { u.page, u.settingsTab = "settings", "cli-tools" })))
	return d
}

func (u *nativeUI) stopOmnigent() {
	if u.busy["POST/api/clients/omnigent/stop"] || u.clientState().Launching != "" {
		return
	}
	u.clientRequest("POST", "/api/clients/omnigent/stop", map[string]any{}, func(raw json.RawMessage, err error) {
		if err != nil {
			u.noticeError(err)
			return
		}
		var response struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(raw, &response) != nil || !response.OK {
			u.noticeError(errors.New(u.tr("Omnigent did not confirm the stop. Refresh to check its state.", "Omnigent no confirmó la detención. Actualiza para comprobar su estado.")))
			return
		}
		u.agentsState().Omnigent.WatchUntil = time.Time{}
		u.agentsState().Omnigent.Generation++
		u.agentsState().Omnigent.Requested = false
		u.refreshOmnigentProfile()
		u.refreshOmnigentPreferencesAfter(nil)
		u.refreshOmnigentLaunchDiscovery()
	})
}
