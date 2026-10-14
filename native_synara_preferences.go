//go:build desktop

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"gioui.org/layout"
)

const nativeSynaraPreferencesEndpoint = "/api/clients/synara/preferences"

type nativeSynaraPreferences struct {
	Version            int      `json:"version"`
	Codex              bool     `json:"codex"`
	Claude             bool     `json:"claude"`
	KiloCodex          bool     `json:"kiloCodex"`
	KiloClaude         bool     `json:"kiloClaude"`
	CodexModels        string   `json:"codexModels"`
	ClaudeModels       string   `json:"claudeModels"`
	Providers          []string `json:"providers"`
	OpenCode2          bool     `json:"openCode2"`
	OpenCodeConnection string   `json:"openCodeConnection"`
}

// Missing OpenCode connection is the legacy native account; explicit null is not a
// valid choice and must never acknowledge a saved Kilo connection.
func (p *nativeSynaraPreferences) UnmarshalJSON(data []byte) error {
	type document nativeSynaraPreferences
	var next document
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, present := fields["openCodeConnection"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("Invalid OpenCode connection.")
	}
	*p = nativeSynaraPreferences(next)
	return nil
}

type nativeSynaraProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type nativeSynaraProviderChoice struct {
	nativeSynaraProvider
	Supported bool
}

func nativeSynaraProviderChoices(p nativeSynaraPreferences, supported []nativeSynaraProvider) []nativeSynaraProviderChoice {
	known := map[string]string{}
	for _, provider := range []nativeSynaraProvider{{ID: "cursor", Label: "Cursor"}, {ID: "grok", Label: "Grok"}, {ID: "opencode", Label: "OpenCode"}, {ID: "antigravity", Label: "Antigravity"}, {ID: "pi", Label: "Pi"}} {
		known[provider.ID] = provider.Label
	}
	result, seen := []nativeSynaraProviderChoice{}, map[string]bool{}
	for _, provider := range supported {
		if known[provider.ID] != "" && !seen[provider.ID] {
			result = append(result, nativeSynaraProviderChoice{nativeSynaraProvider: provider, Supported: true})
			seen[provider.ID] = true
		}
	}
	for _, id := range p.Providers {
		if known[id] != "" && !seen[id] {
			result = append(result, nativeSynaraProviderChoice{nativeSynaraProvider: nativeSynaraProvider{ID: id, Label: known[id]}})
			seen[id] = true
		}
	}
	return result
}

type nativeSynaraModelSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type nativeSynaraPreferencesResponse struct {
	Preferences           nativeSynaraPreferences `json:"preferences"`
	RequiresPreparation   bool                    `json:"requiresPreparation"`
	SupportedProviders    []nativeSynaraProvider  `json:"supportedProviders"`
	OpenCode2Supported    bool                    `json:"openCode2Supported"`
	OpenCodeKiloSupported bool                    `json:"openCodeKiloSupported"`
	ClaudePickerSupported bool                    `json:"claudePickerSupported"`
}
type nativeSynaraPreferencesState struct {
	nativeSynaraPreferencesResponse
	Requested, Loaded bool
	Generation        uint64
	Error             string
	Pending           *nativeSynaraPreferences
}

func nativeSynaraOpenCodeConnection(p nativeSynaraPreferences) string {
	if p.OpenCodeConnection == "" {
		return "native"
	}
	return p.OpenCodeConnection
}

func nativeSynaraOpenCodeUsesKilo(p nativeSynaraPreferences) bool {
	return helperContains(p.Providers, "opencode") && nativeSynaraOpenCodeConnection(p) == "kilo"
}

func nativeSynaraPreferencesEqual(a, b nativeSynaraPreferences) bool {
	a.OpenCodeConnection, b.OpenCodeConnection = nativeSynaraOpenCodeConnection(a), nativeSynaraOpenCodeConnection(b)
	a.Providers = append([]string(nil), a.Providers...)
	b.Providers = append([]string(nil), b.Providers...)
	sort.Strings(a.Providers)
	sort.Strings(b.Providers)
	if len(a.Providers) == 0 {
		a.Providers = nil
	}
	if len(b.Providers) == 0 {
		b.Providers = nil
	}
	return reflect.DeepEqual(a, b)
}

func validNativeSynaraPreferences(p nativeSynaraPreferences) bool {
	seen := map[string]bool{}
	for _, id := range p.Providers {
		if seen[id] || !helperContains([]string{"cursor", "grok", "opencode", "antigravity", "pi"}, id) {
			return false
		}
		seen[id] = true
	}
	return p.Version == 1 && (p.CodexModels == "official" || p.CodexModels == "shared") && (p.ClaudeModels == "official" || p.ClaudeModels == "shared") && (!p.OpenCode2 || helperContains(p.Providers, "opencode")) && (nativeSynaraOpenCodeConnection(p) == "native" || nativeSynaraOpenCodeConnection(p) == "kilo")
}

func nativeSynaraPreferenceLibraryError(p nativeSynaraPreferences, library modelLibrary) error {
	if len(library.Models) > 32 && (p.KiloCodex && p.CodexModels == "shared" || p.KiloClaude && p.ClaudeModels == "shared") {
		return errors.New("Synara supports up to 32 shared models per account. Reduce your selection in Models or choose official models.")
	}
	return nil
}

func (u *nativeUI) synaraOpenCodeConnectionError() error {
	s := &u.agentsState().Synara.PreferencesState
	if s.Loaded && nativeSynaraOpenCodeUsesKilo(s.Preferences) && !s.OpenCodeKiloSupported {
		return errors.New(u.tr("This Synara build cannot use the Kilo OpenCode connection. Choose your own OpenCode account, disable OpenCode or install a compatible macOS build.", "Esta compilación de Synara no admite la conexión Kilo de OpenCode. Elige tu cuenta propia de OpenCode, desactiva OpenCode o instala una compilación compatible de macOS."))
	}
	return nil
}

func (u *nativeUI) synaraProviderSelectionError() error {
	s := &u.agentsState().Synara.PreferencesState
	if !s.Loaded {
		return nil
	}
	if s.Preferences.KiloClaude && !s.ClaudePickerSupported {
		return errors.New(u.tr("This Synara installation cannot restrict the Kilo Claude picker to its prepared models. Choose the native Claude account or a compatible macOS build.", "Esta instalación de Synara no puede limitar el selector Claude Kilo a sus modelos preparados. Elige la cuenta propia de Claude o una compilación compatible de macOS."))
	}
	if err := u.synaraOpenCodeConnectionError(); err != nil {
		return err
	}
	for _, provider := range nativeSynaraProviderChoices(s.Preferences, s.SupportedProviders) {
		if helperContains(s.Preferences.Providers, provider.ID) && !provider.Supported {
			return errors.New(u.tr("Turn off unavailable saved choices or install a compatible Synara build before preparing.", "Desactiva las selecciones guardadas no disponibles o instala una compilación Synara compatible antes de preparar."))
		}
	}
	if s.Preferences.OpenCode2 && !s.OpenCode2Supported {
		return errors.New(u.tr("This Synara build cannot use the saved OpenCode 2 choice. Choose OpenCode 1, disable OpenCode or install a compatible Synara build.", "Esta compilación de Synara no admite la selección guardada de OpenCode 2. Elige OpenCode 1, desactiva OpenCode o instala una compilación Synara compatible."))
	}
	return nil
}

func (u *nativeUI) synaraPreferencesBusy() bool {
	return u.busy["POST"+nativeSynaraPreferencesEndpoint]
}

func (u *nativeUI) refreshSynaraPreferences() {
	u.refreshSynaraPreferencesAfter(nil)
}

func (u *nativeUI) refreshSynaraPreferencesAfter(done func(error)) {
	finish := func(err error) {
		if done != nil {
			done(err)
		}
	}
	s := &u.agentsState().Synara.PreferencesState
	if u.busy["GET"+nativeSynaraPreferencesEndpoint] || u.synaraPreferencesBusy() {
		finish(errors.New(u.tr("Wait for Synara settings to finish loading or saving.", "Espera a que terminen de cargarse o guardarse los ajustes de Synara.")))
		return
	}
	s.Requested, s.Error = true, ""
	s.Generation++
	generation := s.Generation
	u.clientRequest("GET", nativeSynaraPreferencesEndpoint, nil, func(raw json.RawMessage, err error) {
		if s.Generation != generation {
			finish(errors.New(u.launchSettingsChangedMessage()))
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			finish(err)
			return
		}
		var response nativeSynaraPreferencesResponse
		if json.Unmarshal(raw, &response) != nil || !validNativeSynaraPreferences(response.Preferences) {
			s.Error = u.tr("Could not read Synara preferences. Refresh to try again.", "No se pudieron leer las preferencias de Synara. Actualiza para reintentarlo.")
			finish(errors.New(s.Error))
			return
		}
		changed := s.Loaded && !nativeSynaraPreferencesEqual(s.Preferences, response.Preferences)
		s.nativeSynaraPreferencesResponse, s.Loaded = response, true
		if changed {
			u.refreshSynaraLaunchDiscovery()
		}
		finish(nil)
	})
}

func (u *nativeUI) saveSynaraPreferences(next nativeSynaraPreferences) {
	agent := &u.agentsState().Synara
	s := &agent.PreferencesState
	if !s.Loaded || u.synaraPreferencesBusy() || !validNativeSynaraPreferences(next) {
		return
	}
	next.Providers = append([]string{}, next.Providers...)
	next.OpenCodeConnection = nativeSynaraOpenCodeConnection(next)
	s.Pending, s.Error = &next, ""
	s.Generation++
	generation := s.Generation
	// Invalidate any profile read started before this settings operation.
	agent.Generation++
	agent.Info.Prepared = false
	u.clientRequest("POST", nativeSynaraPreferencesEndpoint, next, func(raw json.RawMessage, err error) {
		if s.Generation != generation {
			return
		}
		if err != nil {
			s.Error = nativeMessage(err.Error(), u.language)
			return
		}
		var response nativeSynaraPreferencesResponse
		if json.Unmarshal(raw, &response) != nil || !validNativeSynaraPreferences(response.Preferences) || !nativeSynaraPreferencesEqual(response.Preferences, next) {
			s.Error = u.tr("Synara did not confirm the saved preferences. Refresh before preparing.", "Synara no confirmó las preferencias guardadas. Actualiza antes de preparar.")
			return
		}
		s.nativeSynaraPreferencesResponse, s.Pending, s.Loaded = response, nil, true
		s.RequiresPreparation = true
		u.refreshSynaraLaunchDiscovery()
		u.refreshSynaraProfile()
	})
}

func nativeSynaraToggleProvider(p nativeSynaraPreferences, id string, enabled bool) nativeSynaraPreferences {
	providers := []string{}
	for _, existing := range p.Providers {
		if existing != id {
			providers = append(providers, existing)
		}
	}
	if enabled {
		providers = append(providers, id)
	}
	p.Providers = providers
	if id == "opencode" && !enabled {
		p.OpenCode2 = false
	}
	return p
}

func (u *nativeUI) synaraPreferencesPanel() layout.Widget {
	s := &u.agentsState().Synara.PreferencesState
	if !s.Requested {
		u.refreshSynaraPreferences()
	}
	checking, saving := u.busy["GET"+nativeSynaraPreferencesEndpoint], u.synaraPreferencesBusy()
	ready := s.Loaded && !checking && !saving && s.Pending == nil
	status := u.tr("Choose the accounts Synara offers. Changes save automatically.", "Elige las cuentas que ofrece Synara. Los cambios se guardan solos.")
	if checking || !s.Loaded {
		status = u.tr("Loading Synara preferences…", "Cargando preferencias de Synara…")
	}
	if !checking && !s.Loaded && s.Error != "" {
		status = u.tr("Synara preferences are unavailable.", "Las preferencias de Synara no están disponibles.")
	}
	if saving {
		status = u.tr("Saving Synara preferences…", "Guardando preferencias de Synara…")
	}
	statusWidgets := []layout.Widget{u.note(status), u.note(u.tr("Kilo Proxy names and green account indicators identify Kilo accounts.", "Los nombres Kilo Proxy y los indicadores verdes identifican las cuentas Kilo."))}
	if s.Error != "" {
		statusWidgets = append(statusWidgets, u.message(nativeToneError, s.Error))
		if s.Pending != nil {
			statusWidgets = append(statusWidgets, u.note(u.tr("The change is not confirmed. Retry saving or reload the saved settings.", "El cambio no está confirmado. Reintenta el guardado o carga los ajustes guardados.")), u.disabled(!saving, u.button("synara-preferences:retry", u.tr("Retry saving", "Reintentar guardado"), func() {
				if s.Pending != nil {
					u.saveSynaraPreferences(*s.Pending)
				}
			})))
		}
	}
	if s.RequiresPreparation {
		statusWidgets = append(statusWidgets, u.hint(u.tr("Close Synara · Kilo and prepare it again to apply these settings. Your chats are kept.", "Cierra Synara · Kilo y prepara de nuevo para aplicar estos ajustes. Tus chats se conservan.")))
	}
	statusWidgets = append(statusWidgets, u.note(u.tr("These settings apply only to the separate Kilo workspace. Existing CLI logins and your regular Synara workspace are kept.", "Estos ajustes solo afectan al espacio Kilo separado. Se conservan las sesiones CLI y tu espacio Synara habitual.")))
	rows := []layout.Widget{u.uxsRow(u.tr("Private workspace", "Espacio privado"), "", u.column(statusWidgets...), u.disabled(!saving && !checking, u.button("synara-preferences:refresh", u.tr("Reload settings", "Cargar ajustes"), func() { s.Pending = nil; u.refreshSynaraPreferences() })), nil)}
	p := s.Preferences
	if saving && s.Pending != nil {
		p = *s.Pending
	}
	account := func(id, label, help string, on bool, set func(*nativeSynaraPreferences, bool)) layout.Widget {
		return u.uxsRow(label, "", u.disabled(ready, u.uxsToggle("synara-preferences:"+id, label, help, on, func(value bool) { next := p; set(&next, value); u.saveSynaraPreferences(next) })), nil, nil)
	}
	rows = append(rows,
		account("codex", "Codex", u.tr("Your existing Codex CLI session", "Tu sesión existente de Codex CLI"), p.Codex, func(p *nativeSynaraPreferences, v bool) { p.Codex = v }),
		account("claude", "Claude", u.tr("Your existing Claude Code session", "Tu sesión existente de Claude Code"), p.Claude, func(p *nativeSynaraPreferences, v bool) { p.Claude = v }),
		account("kilo-codex", "Kilo Proxy · Codex", u.tr("Models through Kilo Proxy", "Modelos a través de Kilo Proxy"), p.KiloCodex, func(p *nativeSynaraPreferences, v bool) { p.KiloCodex = v }),
		u.uxsRow(u.tr("Codex Kilo models", "Modelos de Codex Kilo"), "", u.uxsRadioList("synara-preferences:codex-models:", []nativeChoice{{Value: "official", Label: u.tr("OpenAI and ChatGPT", "OpenAI y ChatGPT")}, {Value: "shared", Label: u.tr("All shared models", "Todos los modelos compartidos")}}, p.CodexModels, ready && p.KiloCodex, func(value string) { next := p; next.CodexModels = value; u.saveSynaraPreferences(next) }), nil, nil),
		account("kilo-claude", "Kilo Proxy · Claude", u.tr("Models through Kilo Proxy", "Modelos a través de Kilo Proxy"), p.KiloClaude, func(p *nativeSynaraPreferences, v bool) { p.KiloClaude = v }),
		u.uxsRow(u.tr("Claude Kilo models", "Modelos de Claude Kilo"), "", u.uxsRadioList("synara-preferences:claude-models:", []nativeChoice{{Value: "official", Label: u.tr("Opus 5.5, Sonnet 5.5 and Fable 5.1", "Opus 5.5, Sonnet 5.5 y Fable 5.1")}, {Value: "shared", Label: u.tr("All shared models", "Todos los modelos compartidos")}}, p.ClaudeModels, ready && p.KiloClaude, func(value string) { next := p; next.ClaudeModels = value; u.saveSynaraPreferences(next) }), nil, u.note(u.tr("Compatible native Claude models use the reasoning levels selected in Kilo. Other shared models keep their compatible defaults. Kilo Claude's filtered model list requires a compatible private macOS runtime.", "Los modelos Claude nativos compatibles usan sus niveles de razonamiento seleccionados en Kilo. Los otros modelos compartidos conservan sus valores iniciales compatibles. La lista filtrada de Claude Kilo requiere un runtime privado compatible de macOS."))),
	)
	for _, provider := range nativeSynaraProviderChoices(p, s.SupportedProviders) {
		id, label := provider.ID, strings.TrimSpace(provider.Label)
		if id == "" || label == "" {
			continue
		}
		help := u.tr("Uses this provider's own installation and login", "Usa la instalación y sesión propias de este proveedor")
		if id == "opencode" && nativeSynaraOpenCodeUsesKilo(p) {
			help = u.tr("Uses your saved shared models through Kilo Proxy", "Usa tus modelos compartidos guardados a través de Kilo Proxy")
		}
		if !provider.Supported {
			label += u.tr(" · unavailable in this build", " · no disponible en esta compilación")
			help = u.tr("Turn off this saved choice or install a compatible Synara build before preparing.", "Desactiva esta selección guardada o instala una compilación Synara compatible antes de preparar.")
		}
		rows = append(rows, account("provider:"+id, label, help, helperContains(p.Providers, id), func(p *nativeSynaraPreferences, enabled bool) {
			if provider.Supported || !enabled {
				*p = nativeSynaraToggleProvider(*p, id, enabled)
			}
		}))
		if id == "opencode" {
			connection := nativeSynaraOpenCodeConnection(p)
			choices := []nativeChoice{{Value: "native", Label: u.tr("Own OpenCode account", "Cuenta propia de OpenCode")}, {Value: "kilo", Label: u.tr("Kilo Proxy · shared models", "Kilo Proxy · modelos compartidos")}}
			choose := func(value string) {
				if value == "kilo" && !s.OpenCodeKiloSupported {
					return
				}
				next := p
				next.OpenCodeConnection = value
				u.saveSynaraPreferences(next)
			}
			editable := ready && helperContains(p.Providers, id) && provider.Supported
			connectionChoices := u.uxsRadioList("synara-preferences:opencode-connection:", choices, connection, editable, choose)
			if !s.OpenCodeKiloSupported {
				connectionChoices = u.column(u.uxsRadio("synara-preferences:opencode-connection:native", choices[0], connection == "native", editable, choose), u.uxsRadio("synara-preferences:opencode-connection:kilo", choices[1], connection == "kilo", false, nil))
			}
			help := u.tr("Your own account uses OpenCode's login and catalog. Kilo uses the models saved in Models through the proxy, with a separate profile. Its prepared model list requires a compatible private macOS runtime. Close Synara · Kilo and prepare again after changing this connection.", "Tu cuenta propia usa la sesión y el catálogo de OpenCode. Kilo usa los modelos guardados en Modelos a través del proxy, con un perfil separado. Su lista de modelos preparados requiere un runtime privado compatible de macOS. Cierra Synara · Kilo y prepara de nuevo tras cambiar esta conexión.")
			if !s.OpenCodeKiloSupported {
				help = u.tr("This Synara build cannot use the Kilo OpenCode connection. Choose your own OpenCode account, disable OpenCode or install a compatible macOS build.", "Esta compilación de Synara no admite la conexión Kilo de OpenCode. Elige tu cuenta propia de OpenCode, desactiva OpenCode o instala una compilación compatible de macOS.")
			}
			rows = append(rows, u.uxsRow(u.tr("OpenCode connection", "Conexión de OpenCode"), "", connectionChoices, nil, u.note(help)))
			if nativeSynaraOpenCodeUsesKilo(p) && nativeSynaraPreferencesEqual(p, u.agentsState().Synara.Info.Preferences) {
				models := []string{}
				for _, model := range u.agentsState().Synara.Info.EffectiveOpenCodeModels {
					label := model.ID
					if model.Name != "" {
						label = model.Name + " · " + model.ID
					}
					models = append(models, label)
				}
				if len(models) > 0 {
					rows = append(rows, u.uxsRow(u.tr("OpenCode Kilo models", "Modelos de OpenCode Kilo"), "", u.note(strings.Join(models, "\n")), nil, nil))
				}
			}
		}
		if id == "opencode" && (s.OpenCode2Supported || p.OpenCode2) {
			choose := func(value string) {
				if value == "2" && !s.OpenCode2Supported {
					return
				}
				next := p
				next.OpenCode2 = value == "2"
				u.saveSynaraPreferences(next)
			}
			version := u.uxsRadioList("synara-preferences:opencode-version:", []nativeChoice{{Value: "1", Label: "OpenCode 1"}, {Value: "2", Label: "OpenCode 2"}}, map[bool]string{false: "1", true: "2"}[p.OpenCode2], ready && helperContains(p.Providers, id), choose)
			versionHelp := u.tr("Choose one version for this account. OpenCode 2 requires 2.0.18 or newer; both versions are never started together with the same profile.", "Elige una versión para esta cuenta. OpenCode 2 requiere 2.0.18 o posterior; nunca se abren ambas versiones a la vez con el mismo perfil.")
			if nativeSynaraOpenCodeConnection(p) == "native" {
				versionHelp += u.tr(" OpenCode 2 has a separate profile that may need its own login.", " OpenCode 2 tiene un perfil separado que puede requerir su propio inicio de sesión.")
			}
			if !s.OpenCode2Supported {
				version = u.column(u.uxsRadio("synara-preferences:opencode-version:1", nativeChoice{Value: "1", Label: "OpenCode 1"}, false, ready, choose), u.uxsRadio("synara-preferences:opencode-version:2", nativeChoice{Value: "2", Label: "OpenCode 2"}, true, false, nil))
				versionHelp = u.tr("This Synara build cannot use the saved OpenCode 2 choice. Choose OpenCode 1, disable OpenCode or install a compatible Synara build.", "Esta compilación de Synara no admite la selección guardada de OpenCode 2. Elige OpenCode 1, desactiva OpenCode o instala una compilación Synara compatible.")
			}
			rows = append(rows, u.uxsRow(u.tr("OpenCode version", "Versión de OpenCode"), "", version, nil, u.note(versionHelp)))
		}
	}
	if s.Loaded && len(s.SupportedProviders) == 0 {
		rows = append(rows, u.uxsRow(u.tr("Other providers", "Otros proveedores"), "", u.note(u.tr("This Synara build does not expose additional compatible providers.", "Esta compilación de Synara no ofrece proveedores compatibles adicionales.")), nil, nil))
	}
	return u.uxsBlock(u.tr("Accounts and models", "Cuentas y modelos"), "", rows...)
}
