//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"strings"

	"gioui.org/layout"
)

const nativeMusePreferencesEndpoint = "/api/clients/muse/preferences"
const nativeMuseEndpoint = "/api/clients/muse"

type nativeMusePreferences struct {
	Version    int    `json:"version"`
	Connection string `json:"connection"`
}
type nativeMuseInfo struct {
	Preferences         nativeMusePreferences      `json:"preferences"`
	Library             *modelLibrary              `json:"library"`
	EffectiveModels     []nativeT3CodeModelSummary `json:"effectiveModels"`
	ResolvedExecutable  string                     `json:"resolvedExecutable"`
	Prepared            bool                       `json:"prepared"`
	RequiresPreparation bool                       `json:"requiresPreparation"`
	KiloSupported       bool                       `json:"kiloSupported"`
	Reason              string                     `json:"reason"`
	ProfileDir          string                     `json:"profileDir"`
	Message             string                     `json:"message"`
}
type nativeMuseState struct {
	Preferences                     nativeMusePreferences
	Pending                         *nativeMusePreferences
	Requested, Loaded               bool
	Generation                      uint64
	Info                            nativeMuseInfo
	ProfileRequested, ProfileLoaded bool
	ProfileGeneration               uint64
	Connection                      [32]byte
	Error                           string
}

func validNativeMusePreferences(p nativeMusePreferences) bool {
	return p.Version == 1 && (p.Connection == "kilo" || p.Connection == "native")
}
func (u *nativeUI) museNative() bool {
	s := &u.clientState().Muse
	return s.Loaded && validNativeMusePreferences(s.Preferences) && s.Preferences.Connection == "native"
}
func (u *nativeUI) musePreferencesReady() bool {
	s := &u.clientState().Muse
	return s.Loaded && validNativeMusePreferences(s.Preferences) && s.Pending == nil && !u.busy["GET"+nativeMusePreferencesEndpoint] && !u.busy["POST"+nativeMusePreferencesEndpoint]
}
func (u *nativeUI) refreshMusePreferencesAfter(done func(error)) {
	s := &u.clientState().Muse
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	if u.busy["GET"+nativeMusePreferencesEndpoint] || u.busy["POST"+nativeMusePreferencesEndpoint] {
		finish(errors.New(u.launchSettingsChangedMessage()))
		return
	}
	s.Requested = true
	s.Generation++
	generation := s.Generation
	u.clientRequest("GET", nativeMusePreferencesEndpoint, nil, func(data json.RawMessage, err error) {
		if generation != s.Generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		var response struct {
			Preferences nativeMusePreferences `json:"preferences"`
		}
		if err == nil && (json.Unmarshal(data, &response) != nil || !validNativeMusePreferences(response.Preferences)) {
			err = errors.New(u.tr("Could not read the Muse connection. Reload settings.", "No se pudo leer la conexión de Muse. Carga los ajustes."))
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		changed := s.Loaded && s.Preferences != response.Preferences
		s.Preferences, s.Loaded, s.Error = response.Preferences, true, ""
		if changed {
			s.Info.Prepared = false
			s.ProfileRequested = false
			s.ProfileGeneration++
		}
		finish(nil)
	})
}
func (u *nativeUI) verifyMusePreferencesAfter(expected nativeMusePreferences, done func(error)) {
	s := &u.clientState().Muse
	if !u.musePreferencesReady() {
		done(errors.New(u.launchSettingsChangedMessage()))
		return
	}
	generation := s.Generation
	u.clientRequest("GET", nativeMusePreferencesEndpoint, nil, func(data json.RawMessage, err error) {
		var response struct {
			Preferences nativeMusePreferences `json:"preferences"`
		}
		if err == nil && (json.Unmarshal(data, &response) != nil || !validNativeMusePreferences(response.Preferences)) {
			err = errors.New(u.launchSettingsChangedMessage())
		}
		if err == nil && (generation != s.Generation || s.Pending != nil || s.Preferences != expected || response.Preferences != expected) {
			if generation == s.Generation && s.Pending == nil {
				s.Preferences = response.Preferences
				s.Generation++
				s.Info.Prepared = false
				s.ProfileRequested = false
			}
			err = errors.New(u.launchSettingsChangedMessage())
		}
		done(err)
	})
}
func (u *nativeUI) saveMusePreferences(next nativeMusePreferences) {
	s := &u.clientState().Muse
	if !validNativeMusePreferences(next) || !s.Loaded || u.busy["GET"+nativeMusePreferencesEndpoint] || u.busy["POST"+nativeMusePreferencesEndpoint] {
		return
	}
	s.Pending = &next
	s.Generation++
	generation := s.Generation
	s.ProfileGeneration++
	s.Info.Prepared = false
	s.Error = ""
	u.clientRequest("POST", nativeMusePreferencesEndpoint, next, func(data json.RawMessage, err error) {
		if generation != s.Generation {
			return
		}
		var response struct {
			Preferences nativeMusePreferences `json:"preferences"`
		}
		if err == nil && (json.Unmarshal(data, &response) != nil || !validNativeMusePreferences(response.Preferences) || response.Preferences != next) {
			err = errors.New(u.tr("Muse did not confirm the saved connection. Reload settings before opening.", "Muse no confirmó la conexión guardada. Carga los ajustes antes de abrir."))
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			return
		}
		s.Preferences, s.Pending, s.ProfileRequested = response.Preferences, nil, false
		u.refreshMuseProfile()
	})
}
func (u *nativeUI) refreshMuseProfile() {
	s := &u.clientState().Muse
	if u.busy["GET"+nativeMuseEndpoint] {
		return
	}
	s.ProfileRequested = true
	s.Error = ""
	s.ProfileGeneration++
	generation, prefs := s.ProfileGeneration, s.Generation
	connection, path := u.launchConnectionFingerprint(), u.clientState().LaunchInfo.Clients["muse"].Path
	u.clientRequest("GET", nativeMuseEndpoint, nil, func(data json.RawMessage, err error) {
		if generation != s.ProfileGeneration || prefs != s.Generation || path != u.clientState().LaunchInfo.Clients["muse"].Path {
			return
		}
		var info nativeMuseInfo
		if err == nil && (json.Unmarshal(data, &info) != nil || !validNativeMusePreferences(info.Preferences)) {
			err = errors.New(u.tr("Could not read the Muse profile. Refresh to try again.", "No se pudo leer el perfil de Muse. Actualiza para reintentarlo."))
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			return
		}
		s.Info, s.ProfileLoaded, s.Connection = info, true, connection
	})
}
func (u *nativeUI) museReadinessReason() string {
	s := &u.clientState().Muse
	if !u.musePreferencesReady() {
		return u.tr("Load or finish saving the Muse connection before opening.", "Carga o termina de guardar la conexión de Muse antes de abrir.")
	}
	if u.museNative() {
		return ""
	}
	if s.Error != "" {
		return s.Error
	}
	if u.busy["GET"+nativeMuseEndpoint] || !s.ProfileLoaded || s.Info.Preferences != s.Preferences {
		return u.tr("Checking Muse Kilo compatibility…", "Comprobando compatibilidad de Muse Kilo…")
	}
	if !s.Info.KiloSupported {
		if s.Info.Reason != "" {
			return nativeMessage(s.Info.Reason, u.language)
		}
		return u.tr("This Muse installation cannot use Kilo models. Choose a compatible executable or your Meta account.", "Esta instalación de Muse no admite modelos de Kilo. Elige un ejecutable compatible o tu cuenta de Meta.")
	}
	if !u.agentConnectionReady() || u.setupConnectionNeeded() || u.connectionWorkingExceptLaunch("muse") {
		return u.tr("Connect Kilo or ChatGPT before opening Muse with Kilo models.", "Conecta Kilo o ChatGPT antes de abrir Muse con modelos de Kilo.")
	}
	status, ready := u.libraryStatus()
	if !ready {
		return status
	}
	library, valid := u.validatedModelLibraryValue()
	if !valid {
		return nativeMessage(u.library.validation, u.language)
	}
	if len(library.Models) == 0 {
		return u.tr("Add and save shared models in Models before opening Muse Kilo.", "Añade y guarda modelos compartidos en Modelos antes de abrir Muse Kilo.")
	}
	return ""
}
func (u *nativeUI) prepareMuseAfter(executable string, done func(error)) {
	s := &u.clientState().Muse
	if reason := u.museReadinessReason(); reason != "" {
		done(errors.New(reason))
		return
	}
	if u.museNative() {
		done(nil)
		return
	}
	library, valid := u.validatedModelLibraryValue()
	if !valid {
		done(errors.New(u.library.validation))
		return
	}
	expected, generation, connection := s.Preferences, s.Generation, u.launchConnectionFingerprint()
	u.clientRequest("POST", nativeMuseEndpoint, map[string]any{"library": library, "preferences": expected, "museExecutable": executable}, func(data json.RawMessage, err error) {
		var info nativeMuseInfo
		if err == nil && (json.Unmarshal(data, &info) != nil || !info.Prepared || info.Preferences != expected || info.Library == nil || libraryFingerprint(*info.Library) != libraryFingerprint(library) || info.ResolvedExecutable != executable) {
			err = errors.New(u.tr("Muse did not confirm the prepared Kilo profile.", "Muse no confirmó el perfil Kilo preparado."))
		}
		if err == nil && (s.Generation != generation || s.Pending != nil || s.Preferences != expected || connection != u.launchConnectionFingerprint() || libraryFingerprint(library) != libraryFingerprint(u.modelLibraryValue())) {
			err = errors.New(u.launchSettingsChangedMessage())
		}
		if err == nil {
			s.Info, s.ProfileLoaded, s.Connection = info, true, connection
		}
		done(err)
	})
}
func (u *nativeUI) museConnectionRow() layout.Widget {
	s := &u.clientState().Muse
	if !s.Requested {
		u.refreshMusePreferencesAfter(nil)
	}
	if !s.ProfileRequested && s.Loaded {
		u.refreshMuseProfile()
	}
	p := s.Preferences
	if s.Pending != nil {
		p = *s.Pending
	}
	ready := s.Loaded && !u.busy["GET"+nativeMusePreferencesEndpoint] && !u.busy["POST"+nativeMusePreferencesEndpoint] && u.clientState().Launching == ""
	choices := []nativeChoice{{Value: "kilo", Label: u.tr("Kilo Proxy · shared models", "Kilo Proxy · modelos compartidos")}, {Value: "native", Label: u.tr("Own Meta account", "Cuenta propia de Meta")}}
	controls := u.uxsRadioList("muse-preferences:connection:", choices, p.Connection, ready && s.Pending == nil, func(value string) { u.saveMusePreferences(nativeMusePreferences{Version: 1, Connection: value}) })
	extra := []layout.Widget{controls}
	if s.Error != "" {
		extra = append(extra, u.hint(s.Error))
	}
	if s.Pending != nil {
		extra = append(extra, u.note(u.tr("The connection is not confirmed. Retry saving or reload settings.", "La conexión no está confirmada. Reintenta el guardado o carga los ajustes.")), u.pills(u.disabled(ready, u.button("muse-preferences:retry", u.tr("Retry saving", "Reintentar guardado"), func() {
			if s.Pending != nil {
				u.saveMusePreferences(*s.Pending)
			}
		}))))
	}
	extra = append(extra, u.disabled(ready, u.button("muse-preferences:reload", u.tr("Reload settings", "Cargar ajustes"), func() {
		s.Pending = nil
		u.refreshMusePreferencesAfter(func(err error) {
			if err == nil {
				s.ProfileRequested = false
				u.refreshMuseProfile()
			}
		})
	})))
	return u.uxhKV(u.tr("Connection", "Conexión"), u.column(extra...))
}
func (u *nativeUI) museModelSummary() string {
	s := &u.clientState().Muse
	if s.Info.Preferences != s.Preferences || !s.ProfileLoaded {
		return u.tr("Shared models saved in Models", "Modelos compartidos guardados en Modelos")
	}
	models := []string{}
	for _, m := range s.Info.EffectiveModels {
		label := m.Name
		if label == "" {
			label = m.ID
		} else {
			label += " · " + m.ID
		}
		models = append(models, label)
	}
	if len(models) == 0 {
		return u.tr("No shared models prepared", "Sin modelos compartidos preparados")
	}
	return strings.Join(models, "\n")
}

func (u *nativeUI) uxhMuseKiloHarnessPage(widgets []layout.Widget, connection layout.Widget) layout.Widget {
	key := "muse"
	rows := []layout.Widget{u.uxhFolderRow(key), connection, u.uxhKV(u.tr("Account", "Cuenta"), u.column(u.uxhValue("Kilo Proxy · Muse Code"), u.note(u.tr("Uses your Kilo connection in a separate Muse profile. Your Meta account and chats stay separate.", "Usa tu conexión Kilo en un perfil Muse separado. Tu cuenta de Meta y sus chats siguen separados.")))), u.uxhKV(u.tr("Models", "Modelos"), u.column(u.uxhValue(u.museModelSummary()), u.note(u.tr("Choose and save models in Models. Kilo prepares that exact selection; it does not sign in to Meta.", "Elige y guarda modelos en Modelos. Kilo prepara esa selección exacta; no inicia sesión en Meta.")), u.button("agent:muse:models", u.tr("Open Models", "Abrir Modelos"), func() { u.page = "models" })))}
	if reason := u.museReadinessReason(); reason != "" {
		rows = append(rows, uxhCell(u.hint(reason)))
	}
	widgets = append(widgets, u.uxhBlock(u.tr("When opening", "Al abrir"), "", rows...))
	path := u.clientState().LaunchInfo.Clients[key].Path
	if path == "" {
		path = u.tr("Not detected", "Sin detectar")
	}
	widgets = append(widgets, u.uxhBlock(u.tr("Advanced", "Avanzado"), "", u.uxhCompatibilityRow(key), u.uxhAdvancedRow(u.tr("CLI installation", "Instalación de la CLI"), path, []layout.Widget{u.note(u.tr("Uses the same selected Muse executable, including custom versions. An unavailable selected path blocks opening.", "Usa el mismo ejecutable Muse seleccionado, incluidas las versiones personalizadas. Una ruta seleccionada no disponible bloquea la apertura."))}, u.button("agent:muse:cli-settings", u.tr("Open in Settings", "Abrir en Ajustes"), func() { u.page, u.settingsTab = "settings", "cli-tools" })), u.uxhAdvancedRow(u.tr("Terminal commands", "Comandos de terminal"), u.tr("kilo-muse follows this connection choice. Keep Kilo Proxy open when using Kilo models.", "kilo-muse sigue esta conexión. Mantén Kilo Proxy abierto al usar modelos de Kilo."), nil, u.button("agent:muse:terminal-commands", u.tr("Open in Settings", "Abrir en Ajustes"), func() { u.page, u.settingsTab = "settings", "terminal" }))))
	return u.column(widgets...)
}

func (u *nativeUI) museOpenedMessage() string {
	if u.museNative() {
		return u.tr("Muse Code opened with its own login and models.", "Muse Code abierto con su propio inicio de sesión y modelos.")
	}
	return u.tr("Muse Code opened with your Kilo models.", "Muse Code abierto con tus modelos de Kilo.")
}
