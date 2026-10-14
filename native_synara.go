//go:build desktop

package main

import (
	"encoding/json"

	"gioui.org/layout"
)

const synaraDownloadURL = "https://github.com/Emanuele-web04/synara/releases"

type nativeSynaraInfo struct {
	Prepared                bool                       `json:"prepared"`
	Library                 *modelLibrary              `json:"library"`
	Version                 string                     `json:"version"`
	Message                 string                     `json:"message"`
	Preferences             nativeSynaraPreferences    `json:"preferences"`
	EffectiveCodexModels    []nativeSynaraModelSummary `json:"effectiveCodexModels"`
	EffectiveClaudeModels   []nativeSynaraModelSummary `json:"effectiveClaudeModels"`
	EffectiveOpenCodeModels []nativeSynaraModelSummary `json:"effectiveOpenCodeModels"`
}

type nativeSynaraState struct {
	Info             nativeSynaraInfo
	Requested        bool
	Loaded           bool
	Generation       uint64
	Connection       [32]byte
	Error            string
	PreferencesState nativeSynaraPreferencesState
}

// Only confirmed preferences may bypass the shared gateway for native accounts.
func (u *nativeUI) synaraNativeOnly() bool {
	p := &u.agentsState().Synara.PreferencesState
	return p.Loaded && validNativeSynaraPreferences(p.Preferences) && !p.Preferences.KiloCodex && !p.Preferences.KiloClaude && !nativeSynaraOpenCodeUsesKilo(p.Preferences)
}

func (u *nativeUI) acceptSynaraProfile(data json.RawMessage, connection [32]byte) {
	state := &u.agentsState().Synara
	state.Generation++
	state.Requested, state.Loaded, state.Connection = true, true, connection
	state.Info, state.Error = nativeSynaraInfo{}, ""
	if err := json.Unmarshal(data, &state.Info); err != nil {
		state.Error = u.tr("Could not read Synara settings. Refresh to try again.", "No se pudieron leer los ajustes de Synara. Actualiza para reintentarlo.")
	}
	if state.Info.Prepared && nativeSynaraPreferencesEqual(state.PreferencesState.Preferences, state.Info.Preferences) {
		state.PreferencesState.RequiresPreparation = false
	}
}

func (u *nativeUI) refreshSynaraProfile() {
	const endpoint = "/api/clients/synara"
	if u.busy["GET"+endpoint] {
		return
	}
	state := &u.agentsState().Synara
	state.Requested = true
	state.Generation++
	generation, connection := state.Generation, u.launchConnectionFingerprint()
	u.clientRequest("GET", endpoint, nil, func(data json.RawMessage, err error) {
		if state.Generation != generation || !u.synaraNativeOnly() && connection != u.launchConnectionFingerprint() {
			return
		}
		if err != nil {
			state.Info, state.Loaded = nativeSynaraInfo{}, true
			state.Error = u.tr("Could not read Synara settings. Refresh to try again.", "No se pudieron leer los ajustes de Synara. Actualiza para reintentarlo.")
			return
		}
		u.acceptSynaraProfile(data, connection)
	})
}

func (u *nativeUI) synaraPrepared(s *nativeClientSelection) bool {
	state := &u.agentsState().Synara
	if !state.Loaded || !state.Info.Prepared || u.synaraPreferencesBusy() || state.PreferencesState.Pending != nil {
		return false
	}
	if !state.PreferencesState.Loaded || !nativeSynaraPreferencesEqual(state.PreferencesState.Preferences, state.Info.Preferences) {
		return false
	}
	if u.synaraNativeOnly() {
		return true
	}
	if state.Info.Library == nil || state.Connection != u.launchConnectionFingerprint() {
		return false
	}
	payload, err := nativeClientPayload("synara", s)
	if err != nil {
		return false
	}
	library := payload.(map[string]any)["library"].(modelLibrary)
	return libraryFingerprint(library) == libraryFingerprint(*state.Info.Library)
}

// synaraDetail adds Synara's agent list, prepared-state checks and notes.
func (u *nativeUI) synaraDetail(s *nativeClientSelection) uxhDetail {
	const key = "synara"
	state := &u.agentsState().Synara
	if !state.Requested {
		u.refreshSynaraProfile()
	}
	if !state.PreferencesState.Requested {
		u.refreshSynaraPreferences()
	}
	payload, validation := nativeClientPayload(key, s)
	if validation == nil && state.PreferencesState.Loaded {
		validation = nativeSynaraPreferenceLibraryError(state.PreferencesState.Preferences, payload.(map[string]any)["library"].(modelLibrary))
	}
	libraryStatus, libraryReady := u.libraryStatus()
	working := u.clientState().Launching != "" || u.busy["POST"+nativeClientEndpoint(key)] || u.synaraPreferencesBusy() || u.busy["GET"+nativeSynaraPreferencesEndpoint] || !state.PreferencesState.Loaded || state.PreferencesState.Pending != nil
	canPrepare := libraryReady && u.agentConnectionReady() && !u.setupConnectionNeeded() && !u.connectionWorking() && len(s.Models) > 0 && validation == nil && !working
	if u.synaraNativeOnly() {
		validation, libraryReady, canPrepare = nil, true, !working
	}
	if err := u.synaraProviderSelectionError(); err != nil {
		validation, canPrepare = err, false
	}
	d := uxhDetail{canPrepare: &canPrepare}
	d.prepareNote = u.tr("Prepare or open Synara · Kilo to apply your saved accounts and models.", "Prepara o abre Synara · Kilo para aplicar las cuentas y modelos guardados.")
	if u.synaraPrepared(s) {
		d.prepareNote = u.tr("Your saved Synara accounts and models are prepared.", "Las cuentas y modelos guardados de Synara están preparados.")
	}
	if validation != nil {
		d.status = append(d.status, u.hint(nativeMessage(validation.Error(), u.language)))
	}
	if !libraryReady {
		d.status = append(d.status, u.hint(libraryStatus))
	}
	if state.Error != "" {
		d.status = append(d.status, u.hint(state.Error))
	} else if state.Info.Message != "" && nativeMessage(state.Info.Message, u.language) != nativeMessage(u.clientState().LaunchInfo.Clients[key].Reason, u.language) && (!state.Info.Prepared || u.synaraPrepared(s)) {
		d.status = append(d.status, u.note(nativeMessage(state.Info.Message, u.language)))
	}
	d.blocks = append(d.blocks, u.synaraPreferencesPanel())
	d.blocks = append(d.blocks, u.synaraAccountSummary())
	requirements := u.tr("Supports official Synara stable and Beta releases. Preparation checks the CLIs required by your enabled accounts.", "Admite versiones oficiales estables y Beta de Synara. La preparación comprueba los CLI que requieren las cuentas activadas.")
	if state.Info.Version != "" {
		requirements += u.tr(" Detected: ", " Detectado: ") + state.Info.Version + "."
	}
	notesID := "client:synara:notes"
	notes := []layout.Widget{u.disclosure(notesID, u.tr("How this works", "Cómo funciona"))}
	if u.expanded[notesID] {
		notes = append(notes,
			u.note(u.tr("Existing chats keep their account; start a new chat to switch between normal and Kilo. For custom Claude gateway IDs, compatible reasoning defaults come from Kilo Models. Close and reopen Synara · Kilo after changing them; saved levels require Claude Code 2.1.251 or newer (2.1.267 or newer for Opus/Sonnet 5.5).", "Los chats existentes conservan su cuenta; inicia uno nuevo para cambiar entre normal y Kilo. Para IDs Claude personalizados del gateway, el razonamiento compatible inicial viene de Modelos de Kilo. Cierra y reabre Synara · Kilo tras cambiarlos; los niveles guardados requieren Claude Code 2.1.251 o posterior (2.1.267 o posterior para Opus/Sonnet 5.5).")),
			u.note(u.tr("You can disable unused accounts without blocking the other agents. Voice notes require Codex enabled and signed in with ChatGPT through codex login, even in Claude Kilo chats. Allow Synara microphone access when prompted.", "Puedes desactivar las cuentas que no uses sin bloquear los demás agentes. Las notas de voz requieren Codex activado y autenticado con ChatGPT mediante codex login, incluso en chats Claude Kilo. Concede acceso al micrófono a Synara cuando lo solicite.")),
			u.note(u.tr("Select the exact gateway ID shown in the prepared model list and check the selected model before continuing. A built-in Claude choice maps to your exact gateway ID when its family and version match one prepared model; other built-ins may be unavailable.", "Selecciona el ID exacto del gateway mostrado en la lista preparada y revisa el modelo elegido antes de continuar. Una opción Claude incluida se asigna a tu ID exacto del gateway cuando su familia y versión coinciden con un modelo preparado; las demás pueden no estar disponibles.")),
			u.note(u.tr("Your regular Synara workspace and its chats stay separate and can remain open. Normal agents use their existing CLI sessions, not the Desktop apps. Codex requires file-based CLI authentication; Synara does not support Codex keyring/auto authentication. Quit only Synara · Kilo before changing models or the connection, then reopen it.", "Tu espacio Synara habitual y sus chats siguen separados y pueden permanecer abiertos. Los agentes normales usan sus sesiones CLI existentes, no las apps Desktop. Codex requiere autenticación CLI en archivo; Synara no admite la autenticación keyring/auto de Codex. Cierra solo Synara · Kilo antes de cambiar los modelos o la conexión y vuelve a abrirlo.")),
		)
	}
	d.advanced = append(d.advanced,
		u.uxhAdvancedRow(u.tr("Prepared settings", "Ajustes preparados"), requirements, notes,
			u.disabled(!u.busy["GET/api/clients/synara"], u.button("client:synara:refresh", u.tr("Refresh", "Actualizar"), u.refreshSynaraProfile))))
	if u.clientState().LaunchInfo.Platform == "linux" {
		d.blocks = append(d.blocks, u.uxhBlock(u.tr("Linux installation", "Instalación en Linux"), "",
			uxhCell(u.note(u.tr("A detected AppImage is a package candidate, not a prepared installation. Install a verified Kilo copy, or select a persistent extracted Synara executable in Settings → Application and CLI installations. Kilo never starts an AppImage during detection.", "Un AppImage detectado es un paquete candidato, no una instalación preparada. Instala una copia Kilo verificada o selecciona un ejecutable de Synara extraído en una ubicación permanente en Ajustes → Instalaciones de aplicaciones y CLI. Kilo nunca inicia un AppImage durante la detección."))),
			uxhCell(u.note(u.tr("Ubuntu AppArmor and user-namespace restrictions can block a private Electron copy. Kilo keeps the sandbox enabled. Account-aware delegation in the private macOS runtime has not been validated on Linux.", "Las restricciones de AppArmor y los espacios de nombres de usuario de Ubuntu pueden bloquear una copia privada de Electron. Kilo mantiene el sandbox activado. La delegación entre cuentas del runtime privado de macOS no se ha validado en Linux.")))))
	}
	return d
}

func (u *nativeUI) synaraAccountSummary() layout.Widget {
	state := &u.agentsState().Synara
	p := state.PreferencesState.Preferences
	rows := []layout.Widget{}
	for _, account := range []struct {
		enabled     bool
		label, help string
	}{{p.Codex, "Codex", u.tr("Existing Codex CLI login", "Sesión existente de Codex CLI")}, {p.Claude, "Claude", u.tr("Existing Claude Code login", "Sesión existente de Claude Code")}, {p.KiloCodex, "Kilo Proxy · Codex", u.tr("Models through the proxy", "Modelos a través del proxy")}, {p.KiloClaude, "Kilo Proxy · Claude", u.tr("Compatible models through the proxy", "Modelos compatibles a través del proxy")}} {
		if account.enabled {
			rows = append(rows, u.uxhKV(account.label, u.uxhValue(account.help)))
		}
	}
	for _, choice := range nativeSynaraProviderChoices(p, state.PreferencesState.SupportedProviders) {
		if helperContains(p.Providers, choice.ID) {
			label := choice.Label
			help := u.tr("Own installation and login", "Instalación y sesión propias")
			if choice.ID == "opencode" {
				if p.OpenCode2 {
					label += " 2"
				}
				if nativeSynaraOpenCodeUsesKilo(p) {
					label = "Kilo Proxy · " + label
					help = u.tr("Shared models through the proxy", "Modelos compartidos a través del proxy")
				}
			}
			rows = append(rows, u.uxhKV(label, u.uxhValue(help)))
		}
	}
	if nativeSynaraPreferencesEqual(p, state.Info.Preferences) {
		for _, group := range []struct {
			label  string
			models []nativeSynaraModelSummary
		}{{"Kilo Proxy · Codex", state.Info.EffectiveCodexModels}, {"Kilo Proxy · Claude", state.Info.EffectiveClaudeModels}} {
			values := ""
			for _, m := range group.models {
				if values != "" {
					values += "\n"
				}
				if m.Name != "" {
					values += m.Name + " · "
				}
				values += m.ID
			}
			if values != "" {
				rows = append(rows, u.uxhKV(group.label, u.uxhValue(values)))
			}
		}
	}
	return u.uxhBlock(u.tr("Agents and prepared models", "Agentes y modelos preparados"), u.tr("Start a new chat to change accounts.", "Inicia un chat nuevo para cambiar de cuenta."), rows...)
}
