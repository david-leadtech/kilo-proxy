//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"gioui.org/layout"
)

const nativeCLIPreferencesEndpoint = "/api/clients/cli/preferences"

type nativeCLIPreferencesState struct {
	Preferences       cliPreferences
	Requested, Loaded bool
	Generation        uint64
	Pending           *cliPreferences
	Error             string
	Changed           bool
}

func nativeCLIPathClient(key string) bool {
	switch key {
	case "codex-cli", "claude", "muse", "t3-code", "synara", "open-design", "xcode-codex", "xcode-claude":
		return true
	}
	return false
}

func nativeCLIPathValue(p cliPreferences, client string) string {
	if client == "synara" {
		return p.SynaraExecutable
	}
	if client == "muse" {
		return p.MuseExecutable
	}
	if client == "claude" {
		return p.ClaudeExecutable
	}
	return p.CodexExecutable
}

func nativeCLIPathField(client string) string { return "cli-executable-" + client }

func (u *nativeUI) cliPreferencesBusy() bool {
	return u.busy["GET"+nativeCLIPreferencesEndpoint] || u.busy["POST"+nativeCLIPreferencesEndpoint]
}

func (u *nativeUI) cliPathDirty() bool {
	s := &u.clientState().CLIPreferences
	return s.Loaded && (strings.TrimSpace(u.value(nativeCLIPathField("codex-cli"))) != s.Preferences.CodexExecutable || strings.TrimSpace(u.value(nativeCLIPathField("claude"))) != s.Preferences.ClaudeExecutable || strings.TrimSpace(u.value(nativeCLIPathField("muse"))) != s.Preferences.MuseExecutable || strings.TrimSpace(u.value(nativeCLIPathField("synara"))) != s.Preferences.SynaraExecutable)
}

func (u *nativeUI) cliPreferencesReady() bool {
	s := &u.clientState().CLIPreferences
	return s.Loaded && s.Preferences.Version == 1 && s.Pending == nil && !u.cliPreferencesBusy() && !u.cliPathDirty()
}

func (u *nativeUI) acceptCLIPreferences(next cliPreferences, discardDraft bool) {
	s := &u.clientState().CLIPreferences
	changed := s.Loaded && s.Preferences != next
	claudeChanged := s.Loaded && s.Preferences.ClaudeExecutable != next.ClaudeExecutable
	synaraChanged := s.Loaded && s.Preferences.SynaraExecutable != next.SynaraExecutable
	for _, client := range []string{"codex-cli", "claude", "muse", "synara"} {
		id := nativeCLIPathField(client)
		if discardDraft || !s.Loaded || strings.TrimSpace(u.value(id)) == nativeCLIPathValue(s.Preferences, client) {
			u.setValue(id, nativeCLIPathValue(next, client))
		}
	}
	s.Preferences, s.Loaded = next, true
	if synaraChanged && u.clientState().ManagedTools != nil {
		u.clientState().ManagedTools.requestError = ""
	}
	if claudeChanged {
		c := u.clientState()
		c.Claude, c.ClaudeChecked, c.ClaudeDetectStarted = claudeCapabilities{}, false, false
		c.Xcode.Claude, c.XcodeChecked, c.XcodeDetectStarted = claudeCapabilities{}, false, false
		if !u.busy["GET/api/claude/info"] {
			u.detectAgentCapabilities()
		}
	}
	if changed {
		u.clientState().OpenDesignChecked, u.clientState().OpenDesignDetectStarted = false, false
		u.detectOpenDesign()
		for key, selection := range u.clientState().Selections {
			if nativeCLIPathClient(key) {
				selection.Saved = ""
			}
		}
		u.agentsState().T3Code.Info.Prepared = false
		u.agentsState().Synara.Info.Prepared = false
	}
}

func (u *nativeUI) refreshCLIPreferencesAfter(done func(error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	s := &u.clientState().CLIPreferences
	if u.cliPreferencesBusy() {
		finish(errors.New(u.tr("Wait for CLI settings to finish loading or saving.", "Espera a que terminen de cargarse o guardarse los ajustes de las CLI.")))
		return
	}
	s.Requested, s.Error = true, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("GET", nativeCLIPreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		var current cliPreferences
		if json.Unmarshal(raw, &current) != nil || current.Version != 1 {
			s.Error = u.tr("Could not read CLI executable settings. Reload to try again.", "No se pudieron leer las rutas de las CLI. Carga los ajustes para reintentarlo.")
			finish(errors.New(s.Error))
			return
		}
		u.acceptCLIPreferences(current, true)
		s.Pending = nil
		finish(nil)
	})
}

func (u *nativeUI) verifyCLIPreferencesAfter(expected cliPreferences, done func(error)) {
	s := &u.clientState().CLIPreferences
	if !u.cliPreferencesReady() {
		done(errors.New(u.tr("Load or finish saving CLI executable settings before opening.", "Carga o termina de guardar las rutas de las CLI antes de abrir.")))
		return
	}
	generation := s.Generation
	u.clientRequest("GET", nativeCLIPreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation || s.Pending != nil || u.cliPathDirty() {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			done(err)
			return
		}
		var current cliPreferences
		if json.Unmarshal(raw, &current) != nil || current.Version != 1 {
			done(errors.New(u.tr("Could not read CLI executable settings.", "No se pudieron leer las rutas de las CLI.")))
			return
		}
		changed := current != expected
		u.acceptCLIPreferences(current, false)
		if changed {
			s.Generation++
			u.detectLaunchers()
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		done(nil)
	})
}

func (u *nativeUI) saveCLIPreferences(next cliPreferences) {
	u.saveCLIPreferencesAfter(next, nil)
}

func (u *nativeUI) saveCLIPreferencesAfter(next cliPreferences, done func(error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	s := &u.clientState().CLIPreferences
	if !s.Loaded || u.cliPreferencesBusy() || u.clientState().Launching != "" || next.Version != 1 {
		finish(errors.New(u.tr("Wait for executable settings to finish loading or saving.", "Espera a que terminen de cargarse o guardarse las rutas de los ejecutables.")))
		return
	}
	s.Pending, s.Error = &next, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("POST", nativeCLIPreferencesEndpoint, next, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		var confirmed cliPreferences
		if json.Unmarshal(raw, &confirmed) != nil || confirmed != next {
			s.Error = u.tr("CLI settings did not confirm the saved paths. Reload before opening.", "No se confirmaron las rutas guardadas de las CLI. Carga los ajustes antes de abrir.")
			finish(errors.New(s.Error))
			return
		}
		u.acceptCLIPreferences(confirmed, false)
		s.Pending, s.Changed = nil, true
		u.detectLaunchers()
		finish(nil)
	})
}

func (u *nativeUI) saveCLIExecutable(client string, automatic bool) {
	s := &u.clientState().CLIPreferences
	path := strings.TrimSpace(u.value(nativeCLIPathField(client)))
	if automatic {
		path = ""
		u.setValue(nativeCLIPathField(client), "")
	}
	if path != "" && !filepath.IsAbs(path) {
		s.Error = u.tr("Enter an absolute path to the CLI executable, or leave it empty for automatic detection.", "Introduce una ruta absoluta al ejecutable CLI, o déjala vacía para la detección automática.")
		return
	}
	next := s.Preferences
	if client == "synara" {
		next.SynaraExecutable = path
	} else if client == "muse" {
		next.MuseExecutable = path
	} else if client == "claude" {
		next.ClaudeExecutable = path
	} else {
		next.CodexExecutable = path
	}
	u.saveCLIPreferences(next)
}

func (u *nativeUI) cliPreferencesPanel() layout.Widget {
	s := &u.clientState().CLIPreferences
	if !s.Requested {
		u.refreshCLIPreferencesAfter(nil)
	}
	editable := s.Loaded && s.Pending == nil && !u.cliPreferencesBusy() && u.clientState().Launching == ""
	rows := []layout.Widget{uxhCell(u.note(u.tr("Choose an installed application or CLI, including a custom version. Empty paths use the current verified Kilo copy first, then the system installation. T3 Code, Synara and Open Design use these same CLI paths. Accounts and chats are kept.", "Elige una aplicación o CLI instalada, incluida una versión personalizada. Las rutas vacías usan primero la copia Kilo actual verificada y después la instalación del sistema. T3 Code, Synara y Open Design usan las mismas rutas de las CLI. Se conservan las cuentas y los chats.")))}
	for _, choice := range []nativeChoice{{Value: "codex-cli", Label: "Codex CLI"}, {Value: "claude", Label: "Claude Code"}, {Value: "muse", Label: "Muse Code"}, {Value: "synara", Label: "Synara"}} {
		client, label := choice.Value, choice.Label
		id := nativeCLIPathField(client)
		dirty := s.Loaded && strings.TrimSpace(u.value(id)) != nativeCLIPathValue(s.Preferences, client)
		pathLabel := label + u.tr(" executable path (optional)", " · ruta del ejecutable (opcional)")
		if client == "synara" {
			pathLabel = label + u.tr(" application path (optional)", " · ruta de la aplicación (opcional)")
		}
		rows = append(rows, uxhCell(u.column(
			u.disabled(editable, u.field(id, pathLabel, u.tr("Absolute path; empty = automatic detection", "Ruta absoluta; vacía = detección automática"), false)),
			u.pills(u.disabled(editable && dirty, u.button("cli-executable-save:"+client, u.tr("Save path", "Guardar ruta")+" · "+label, func() { u.saveCLIExecutable(client, false) })), u.disabled(editable && (dirty || nativeCLIPathValue(s.Preferences, client) != ""), u.button("cli-executable-auto:"+client, u.tr("Automatic detection", "Detección automática")+" · "+label, func() { u.saveCLIExecutable(client, true) }))),
		)))
		info := u.clientState().LaunchInfo.Clients[client]
		if info.Path != "" {
			rows = append(rows, uxhCell(u.note(u.tr("Resolved executable: ", "Ejecutable detectado: ")+info.Path)))
		}
		if client == "muse" {
			rows = append(rows, uxhCell(u.note(u.tr("Muse Code uses its own Meta login and official models in the terminal and T3 Code. It does not use your Kilo gateway or shared model selection.", "Muse Code usa su propio inicio de sesión de Meta y sus modelos oficiales en la terminal y T3 Code. No usa tu gateway de Kilo ni la selección compartida de modelos."))))
		}
		if client == "synara" {
			rows = append(rows, uxhCell(u.note(u.tr("On Linux, select a persistent extracted Synara executable. A raw AppImage can be detected but is not ready to open: install a verified Kilo copy or select the extracted executable. Detection does not verify its source, version or channel. Ubuntu sandbox restrictions still apply.", "En Linux, selecciona un ejecutable de Synara extraído en una ubicación permanente. Un AppImage sin extraer puede detectarse, pero no está listo para abrir: instala una copia Kilo verificada o selecciona el ejecutable extraído. La detección no verifica su origen, versión ni canal. Se mantienen las restricciones del sandbox de Ubuntu."))))
		}
	}
	status := ""
	switch {
	case u.busy["POST"+nativeCLIPreferencesEndpoint]:
		status = u.tr("Saving CLI paths…", "Guardando rutas de las CLI…")
	case !s.Loaded:
		status = u.tr("Loading CLI paths…", "Cargando rutas de las CLI…")
	case u.cliPathDirty():
		status = u.tr("Save the executable paths before preparing or opening a harness.", "Guarda las rutas de los ejecutables antes de preparar o abrir un harness.")
	case s.Changed:
		status = u.tr("Paths saved. Close the affected Kilo workspace and prepare again. Chats are kept.", "Rutas guardadas. Cierra el espacio Kilo afectado y prepara de nuevo. Los chats se conservan.")
	}
	if status != "" {
		rows = append(rows, uxhCell(u.note(status)))
	}
	if s.Error != "" {
		rows = append(rows, uxhCell(u.message(nativeToneError, s.Error)))
		if s.Pending != nil {
			next := *s.Pending
			rows = append(rows, uxhCell(u.button("cli-preferences-retry", u.tr("Retry saving", "Reintentar guardado"), func() { u.saveCLIPreferences(next) })))
		}
	}
	rows = append(rows, uxhCell(u.disabled(!u.cliPreferencesBusy(), u.button("cli-preferences-reload", u.tr("Reload paths", "Cargar rutas"), func() { u.refreshCLIPreferencesAfter(nil) }))))
	return u.uxhBlock(u.tr("Application and CLI installations", "Instalaciones de aplicaciones y CLI"), "", rows...)
}
