//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"gioui.org/layout"
)

const nativeOpenCodePreferencesEndpoint = "/api/clients/opencode/preferences"

type nativeOpenCodePreferencesState struct {
	Preferences       openCodePreferences
	Requested, Loaded bool
	Generation        uint64
	Pending           *openCodePreferences
	Error             string
}

func (u *nativeUI) openCodePreferencesBusy() bool {
	return u.busy["GET"+nativeOpenCodePreferencesEndpoint] || u.busy["POST"+nativeOpenCodePreferencesEndpoint]
}

func (u *nativeUI) openCodeClientKey() string {
	if u.clientState().OpenCodePreferences.Preferences.Major == 2 {
		return "opencode2"
	}
	return "opencode"
}

func (u *nativeUI) openCodePreferencesReady() bool {
	s := &u.clientState().OpenCodePreferences
	return s.Loaded && s.Pending == nil && !u.openCodePreferencesBusy() && !u.openCodePathDirty() && s.Preferences.Version == 1 && (s.Preferences.Major == 1 || s.Preferences.Major == 2)
}

func nativeOpenCodeExecutable(p openCodePreferences, major int) string {
	if major == 2 {
		return p.Executable2
	}
	return p.Executable1
}

func nativeOpenCodePathField(major int) string {
	return "client-opencode-executable-" + strconv.Itoa(major)
}

func (u *nativeUI) openCodePathDirty() bool {
	s := &u.clientState().OpenCodePreferences
	return s.Loaded && (strings.TrimSpace(u.value(nativeOpenCodePathField(1))) != s.Preferences.Executable1 || strings.TrimSpace(u.value(nativeOpenCodePathField(2))) != s.Preferences.Executable2)
}

func (u *nativeUI) acceptOpenCodePreferences(next openCodePreferences, discardDraft bool) {
	s := &u.clientState().OpenCodePreferences
	changed := s.Loaded && (s.Preferences.Executable1 != next.Executable1 || s.Preferences.Executable2 != next.Executable2)
	for _, major := range []int{1, 2} {
		id := nativeOpenCodePathField(major)
		if discardDraft || !s.Loaded || strings.TrimSpace(u.value(id)) == nativeOpenCodeExecutable(s.Preferences, major) {
			u.setValue(id, nativeOpenCodeExecutable(next, major))
		}
	}
	s.Preferences, s.Loaded = next, true
	if changed {
		u.agentsState().T3Code.Info.Prepared = false
		u.clientState().OpenDesignChecked, u.clientState().OpenDesignDetectStarted = false, false
		u.detectOpenDesign()
	}
}

func (u *nativeUI) refreshOpenCodePreferencesAfter(done func(error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	s := &u.clientState().OpenCodePreferences
	if u.openCodePreferencesBusy() {
		finish(errors.New(u.tr("Wait for OpenCode settings to finish.", "Espera a que terminen los ajustes de OpenCode.")))
		return
	}
	s.Requested, s.Error = true, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("GET", nativeOpenCodePreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		var response openCodePreferences
		if json.Unmarshal(raw, &response) != nil || response.Version != 1 || (response.Major != 1 && response.Major != 2) {
			s.Error = u.tr("Could not read the OpenCode version choice. Try again.", "No se pudo leer la versión elegida de OpenCode. Reinténtalo.")
			finish(errors.New(s.Error))
			return
		}
		u.acceptOpenCodePreferences(response, true)
		s.Pending = nil
		finish(nil)
	})
}

// A second Kilo window can change this preference without changing our local
// form. Check the saved choice before writing a profile and again before Open.
func (u *nativeUI) verifyOpenCodePreferencesAfter(key string, done func(error)) {
	s := &u.clientState().OpenCodePreferences
	if u.openCodePreferencesBusy() || s.Pending != nil || u.openCodePathDirty() {
		done(errors.New(u.tr("Wait for OpenCode settings to finish.", "Espera a que terminen los ajustes de OpenCode.")))
		return
	}
	generation := s.Generation
	u.clientRequest("GET", nativeOpenCodePreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation || s.Pending != nil {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			done(err)
			return
		}
		var current openCodePreferences
		if json.Unmarshal(raw, &current) != nil || current.Version != 1 || (current.Major != 1 && current.Major != 2) {
			s.Error = u.tr("Could not read the OpenCode version choice. Try again.", "No se pudo leer la versión elegida de OpenCode. Reinténtalo.")
			done(errors.New(s.Error))
			return
		}
		changed := s.Loaded && s.Preferences != current
		u.acceptOpenCodePreferences(current, false)
		s.Requested, s.Error = true, ""
		if changed {
			s.Generation++
			u.detectLaunchers()
		}
		if changed || key != u.openCodeClientKey() {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		done(nil)
	})
}

// T3 chooses its own OpenCode major, but shares the two executable paths.
func (u *nativeUI) verifyT3OpenCodePathsAfter(expected openCodePreferences, done func(error)) {
	s := &u.clientState().OpenCodePreferences
	if u.openCodePreferencesBusy() || s.Pending != nil || u.openCodePathDirty() {
		done(errors.New(u.launchSettingsChangedMessage()))
		return
	}
	generation := s.Generation
	u.clientRequest("GET", nativeOpenCodePreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if generation != s.Generation || s.Pending != nil || u.openCodePathDirty() {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			done(err)
			return
		}
		var current openCodePreferences
		if json.Unmarshal(raw, &current) != nil || current.Version != 1 || (current.Major != 1 && current.Major != 2) {
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		changed := current.Executable1 != expected.Executable1 || current.Executable2 != expected.Executable2
		u.acceptOpenCodePreferences(current, false)
		if changed || u.openCodePathDirty() {
			s.Generation++
			u.detectLaunchers()
			done(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		done(nil)
	})
}

func (u *nativeUI) saveOpenCodePreferences(next openCodePreferences) {
	s := &u.clientState().OpenCodePreferences
	if !s.Loaded || u.openCodePreferencesBusy() || u.clientState().Launching != "" || next.Version != 1 || (next.Major != 1 && next.Major != 2) {
		return
	}
	s.Pending, s.Error = &next, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("POST", nativeOpenCodePreferencesEndpoint, next, func(raw json.RawMessage, err error) {
		if generation != s.Generation {
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			return
		}
		var response openCodePreferences
		if json.Unmarshal(raw, &response) != nil || response != next {
			s.Error = u.tr("OpenCode did not confirm the saved version. Reload before opening.", "OpenCode no confirmó la versión guardada. Carga los ajustes antes de abrir.")
			return
		}
		u.acceptOpenCodePreferences(response, false)
		s.Pending = nil
		u.detectLaunchers()
	})
}

func (u *nativeUI) saveOpenCodeExecutable(major int, automatic bool) {
	s := &u.clientState().OpenCodePreferences
	next := s.Preferences
	path := strings.TrimSpace(u.value(nativeOpenCodePathField(major)))
	if automatic {
		path = ""
		u.setValue(nativeOpenCodePathField(major), "")
	}
	if path != "" && !filepath.IsAbs(path) {
		s.Error = u.tr("Enter an absolute path to the OpenCode executable, or leave it empty for automatic detection.", "Introduce una ruta absoluta al ejecutable de OpenCode, o déjala vacía para la detección automática.")
		return
	}
	if major == 2 {
		next.Executable2 = path
	} else {
		next.Executable1 = path
	}
	u.saveOpenCodePreferences(next)
}

func (u *nativeUI) openCodeVersionPanel() layout.Widget {
	s := &u.clientState().OpenCodePreferences
	if !s.Requested {
		u.refreshOpenCodePreferencesAfter(nil)
	}
	major := s.Preferences.Major
	if major == 0 {
		major = 1
	}
	if s.Pending != nil && u.busy["POST"+nativeOpenCodePreferencesEndpoint] {
		major = s.Pending.Major
	}
	editable := u.openCodePreferencesReady() && u.clientState().Launching == "" && !u.busy["POST"+nativeClientEndpoint("opencode")] && !u.busy["POST"+nativeClientEndpoint("opencode2")]
	pathEditable := s.Loaded && s.Pending == nil && !u.openCodePreferencesBusy() && u.clientState().Launching == "" && !u.busy["POST"+nativeClientEndpoint("opencode")] && !u.busy["POST"+nativeClientEndpoint("opencode2")]
	widgets := []layout.Widget{
		u.segmented("client:opencode:major:", []nativeChoice{{Value: "1", Label: "OpenCode 1"}, {Value: "2", Label: "OpenCode 2"}}, strconv.Itoa(major), editable, func(value string) {
			selected, _ := strconv.Atoi(value)
			next := s.Preferences
			next.Major = selected
			u.saveOpenCodePreferences(next)
		}),
		u.note(u.tr("Choose the version to open. OpenCode 2 uses a separate Kilo profile; existing OpenCode 1 accounts and chats are kept.", "Elige la versión que quieres abrir. OpenCode 2 usa un perfil Kilo separado; se conservan las cuentas y chats de OpenCode 1.")),
		u.disabled(pathEditable, u.field(nativeOpenCodePathField(major), u.tr("OpenCode executable path (optional)", "Ruta del ejecutable de OpenCode (opcional)"), u.tr("Absolute path; empty = automatic detection", "Ruta absoluta; vacía = detección automática"), false)),
		u.note(u.tr("This path applies only to the selected version. T3 Code uses the same paths, with its own choice of OpenCode 1 or 2.", "Esta ruta solo se aplica a la versión seleccionada. T3 Code usa las mismas rutas y elige OpenCode 1 o 2 por separado.")),
		u.pills(
			u.disabled(pathEditable && u.openCodePathDirty(), u.button("client:opencode:executable-save", u.tr("Save executable path", "Guardar ruta del ejecutable"), func() { u.saveOpenCodeExecutable(major, false) })),
			u.disabled(pathEditable && (nativeOpenCodeExecutable(s.Preferences, major) != "" || u.openCodePathDirty()), u.button("client:opencode:executable-auto", u.tr("Use automatic detection", "Usar detección automática"), func() { u.saveOpenCodeExecutable(major, true) })),
		),
	}
	info := u.clientState().LaunchInfo.Clients[u.openCodeClientKey()]
	if info.Path != "" {
		widgets = append(widgets, u.note(u.tr("Resolved executable: ", "Ejecutable detectado: ")+info.Path))
	} else if u.clientState().LaunchChecked {
		widgets = append(widgets, u.note(u.tr("No compatible executable detected for this version.", "No se ha detectado un ejecutable compatible con esta versión.")))
	}
	if u.openCodePathDirty() && s.Pending == nil {
		widgets = append(widgets, u.hint(u.tr("Save the executable path before opening or changing version.", "Guarda la ruta del ejecutable antes de abrir o cambiar de versión.")))
	}
	if major == 2 {
		widgets = append(widgets, u.note(u.tr("Install OpenCode 2.0.18 or newer in major version 2 manually, then check detection again. Automatic installation is not available for this version.", "Instala manualmente OpenCode 2.0.18 o posterior de la versión 2 y vuelve a comprobar la detección. Esta versión no tiene instalación automática.")), u.button("client:opencode2:install-guide", u.tr("OpenCode 2 installation guide", "Guía de instalación de OpenCode 2"), func() { u.open("https://dev.opencode.ai/v2/docs/") }))
	}
	switch {
	case s.Error != "":
		widgets = append(widgets, u.message(nativeToneError, s.Error))
		if s.Pending != nil {
			next := *s.Pending
			widgets = append(widgets, u.button("client:opencode:preferences-retry", u.tr("Retry saving", "Reintentar guardado"), func() { u.saveOpenCodePreferences(next) }))
		}
		widgets = append(widgets, u.button("client:opencode:preferences-reload", u.tr("Reload settings", "Cargar ajustes"), func() { u.refreshOpenCodePreferencesAfter(nil) }))
	case u.busy["POST"+nativeOpenCodePreferencesEndpoint]:
		widgets = append(widgets, u.note(u.tr("Saving version…", "Guardando versión…")))
	case !s.Loaded:
		widgets = append(widgets, u.note(u.tr("Loading version…", "Cargando versión…")))
	}
	return u.column(widgets...)
}

func (u *nativeUI) openCodeDetail(key string, s *nativeClientSelection) uxhDetail {
	_, validation := nativeClientPayload(key, s)
	libraryStatus, libraryReady := u.libraryStatus()
	can := u.openCodePreferencesReady() && libraryReady && u.agentConnectionReady() && !u.setupConnectionNeeded() && !u.connectionWorking() && len(s.Models) > 0 && validation == nil && !u.uxhProfileWorking(key)
	d := uxhDetail{canPrepare: &can}
	d.blocks = append(d.blocks, u.uxhBlock(u.tr("OpenCode version", "Versión de OpenCode"), "", uxhCell(u.openCodeVersionPanel())))
	d.status = append(d.status, u.note(u.tr("OpenCode opens a terminal in your project; use /models there.", "OpenCode abre un terminal en tu proyecto; usa /models allí.")))
	if !libraryReady {
		d.status = append(d.status, u.hint(libraryStatus))
	}
	if validation != nil {
		d.status = append(d.status, u.hint(nativeMessage(validation.Error(), u.language)))
	}
	return d
}
