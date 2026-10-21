//go:build desktop

package main

import (
	"encoding/json"
	"gioui.org/io/semantic"
	"image"
	"strconv"
	"testing"
)

func TestNativeT3MuseModelPolicyLegacyDefaultsAndStrictResponse(t *testing.T) {
	legacy := nativeT3CodePreferences{Version: 1, CodexModels: "official", ClaudeModels: "official", MuseConnection: "kilo", Providers: []string{"muse"}}
	explicit := legacy
	explicit.MuseModels = "shared"
	if !nativeT3CodePreferencesEqual(legacy, explicit) || nativeT3MuseModels(legacy) != "shared" {
		t.Fatal("legacy Kilo models were narrowed")
	}
	legacy.MuseConnection = "native"
	if nativeT3MuseModels(legacy) != "official" {
		t.Fatal("new Muse policy did not default to Meta")
	}
	for _, value := range []string{`null`, `""`, `"all"`} {
		var prefs nativeT3CodePreferences
		if json.Unmarshal([]byte(`{"version":1,"museModels":`+value+`}`), &prefs) == nil {
			t.Fatal("invalid model policy response accepted", value)
		}
	}
}

func TestNativeT3MuseModelPolicyControlSavesWithoutChangingConnection(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		t.Run(language, func(t *testing.T) {
			f := newNativeT3PreferencesFixture(t, language)
			f.mu.Lock()
			f.response.Preferences.KiloCodex, f.response.Preferences.KiloClaude = false, false
			f.response.Preferences.Providers = []string{"muse"}
			f.response.Preferences.MuseConnection = "kilo"
			f.response.Preferences.MuseModels = "shared"
			f.response.MuseKiloSupported = true
			f.mu.Unlock()
			u := f.u
			u.refreshT3CodePreferences()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeT3CodePreferencesEndpoint] })
			label := "Meta models"
			if language == "es" {
				label = "Modelos de Meta"
			}
			f.h.frame()
			f.h.reveal(label, semantic.RadioButton)
			f.h.click(label, semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 1 && !u.t3CodePreferencesBusy() })
			state := u.agentsState().T3Code.PreferencesState
			if state.Preferences.MuseModels != "official" || state.Preferences.MuseConnection != "kilo" || state.Pending != nil {
				t.Fatal("Meta model policy changed account or was not confirmed", state)
			}
			for _, width := range []int{1120, 780} {
				f.h.size = image.Pt(width, 1400)
				f.h.frame()
				f.h.reveal(label, semantic.RadioButton)
				f.h.target(label, semantic.RadioButton)
				nativeGridCapture(t, f.h, "native-t3-muse-meta-policy-"+strconv.Itoa(width)+"-"+language)
				t.Logf("captured %s width %d with Muse connection=%s policy=%s", language, width, state.Preferences.MuseConnection, state.Preferences.MuseModels)
			}
			label = "All shared models"
			if language == "es" {
				label = "Todos los modelos compartidos"
			}
			f.h.frame()
			f.h.reveal(label, semantic.RadioButton)
			f.h.click(label, semantic.RadioButton)
			nativeTestWait(t, u, func() bool { return f.postCount() == 2 && !u.t3CodePreferencesBusy() })
			state = u.agentsState().T3Code.PreferencesState
			if state.Preferences.MuseModels != "shared" || state.Preferences.MuseConnection != "kilo" {
				t.Fatal("explicit all-shared not retained", state)
			}
			for _, width := range []int{1120, 780} {
				f.h.size = image.Pt(width, 1400)
				f.h.frame()
				f.h.reveal(label, semantic.RadioButton)
				nativeGridCapture(t, f.h, "native-t3-muse-shared-policy-"+strconv.Itoa(width)+"-"+language)
				t.Logf("captured %s width %d with Muse connection=%s policy=%s", language, width, state.Preferences.MuseConnection, state.Preferences.MuseModels)
			}

		})
	}
}
