package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestT3CodeOptionalProvidersRequireOptInAndNativeSupport(t *testing.T) {
	rt := clientLaunchRuntime{home: t.TempDir(), platform: "linux", resolve: func(string, string) (string, error) {
		t.Fatal("disabled account resolved an executable")
		return "", errors.New("unexpected")
	}}
	providers, err := planT3CodeOptionalProviders(t3CodePreferences{}, t3CodeInstallation{}, rt, t.TempDir())
	if err != nil || len(providers) != 0 {
		t.Fatal("old builds must not receive unknown driver settings", providers, err)
	}
	providers, err = planT3CodeOptionalProviders(t3CodePreferences{}, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}, rt, t.TempDir())
	if err != nil || len(providers) != 6 {
		t.Fatal(providers, err)
	}
	for _, support := range t3CodeOptionalProviders {
		provider := providers[support.ID].(map[string]any)
		if provider["enabled"] != false || provider["driver"] != support.ID {
			t.Fatal(provider)
		}
	}
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.Providers = []string{"cursor", "grok", "antigravity", "muse", "pi"}
	if _, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{}, rt, t.TempDir()); err == nil {
		t.Fatal("unsupported native provider accepted")
	}
	t.Setenv("KILO_LOCAL_API_KEY", "must-not-copy")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "must-not-copy")
	t.Setenv("OPENAI_BASE_URL", "http://kilo.invalid")
	t.Setenv("XDG_DATA_HOME", filepath.Join(rt.home, "custom-data"))
	providers, err = planT3CodeOptionalProviders(prefs, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}, rt, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range prefs.Providers {
		provider := providers[id].(map[string]any)
		if provider["enabled"] != true {
			t.Fatal(provider)
		}
		env := t3CodeOptionalEnvironmentMap(provider)
		if env["HOME"] != rt.home || env["XDG_DATA_HOME"] != filepath.Join(rt.home, "custom-data") || env["KILO_LOCAL_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["OPENAI_BASE_URL"] != "" {
			t.Fatal(env)
		}
	}
}

func t3CodeOptionalEnvironmentMap(provider map[string]any) map[string]string {
	result := map[string]string{}
	for _, item := range provider["environment"].([]map[string]any) {
		result[item["name"].(string)] = item["value"].(string)
	}
	return result
}

func TestT3CodeOptionalOpenCodeGenerationIsVerifiedAndIsolated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX version fixture")
	}
	for _, tc := range []struct {
		version         string
		second, success bool
	}{{"1.14.19", false, true}, {"1.14.18", false, false}, {"2.0.18", false, false}, {"2.0.17", true, false}, {"1.19.0", true, false}, {"2.0.27", true, true}, {"3.0.0", true, false}} {
		t.Run(tc.version+map[bool]string{true: "/2", false: "/1"}[tc.second], func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			binary := filepath.Join(root, "opencode")
			script := "#!/bin/sh\n[ \"$1\" = --version ] && [ \"$HOME\" != " + helperShellQuote(home) + " ] && [ -z \"${KILO_LOCAL_API_KEY-}${NODE_OPTIONS-}\" ] || exit 2\nprintf 'opencode v" + tc.version + "\\n'\n"
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			prefs := normalizeT3CodePreferences(t3CodePreferences{})
			prefs.Providers, prefs.OpenCode2 = []string{"opencode"}, tc.second
			rt := clientLaunchRuntime{platform: "linux", home: home, resolve: func(client, custom string) (string, error) {
				want := "opencode"
				if tc.second {
					want = "opencode2"
				}
				if client != want || custom != "" {
					t.Fatal(client, custom)
				}
				return binary, nil
			}}
			t.Setenv("KILO_LOCAL_API_KEY", "must-not-copy")
			t.Setenv("NODE_OPTIONS", "--require=must-not-copy")
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "normal-database"))
			providers, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders, OpenCode2Supported: true}, rt, root)
			if (err == nil) != tc.success {
				t.Fatal(providers, err)
			}
			if !tc.success {
				return
			}
			provider := providers["opencode"].(map[string]any)
			if provider["config"].(map[string]any)["binaryPath"] != binary {
				t.Fatal(provider)
			}
			env := t3CodeOptionalEnvironmentMap(provider)
			wantHome := home
			if tc.second {
				wantHome = filepath.Join(root, "profiles", "opencode2")
			}
			if env["HOME"] != wantHome || (tc.second && (!strings.HasPrefix(env["XDG_DATA_HOME"], wantHome+string(filepath.Separator)) || env["XDG_RUNTIME_DIR"] != "")) {
				t.Fatal(env)
			}
		})
	}
}

func t3CodeOptionalCapabilityFixture() string {
	var source strings.Builder
	for _, spec := range []struct{ id, name string }{{"cursor", "Cursor"}, {"grok", "Grok"}, {"opencode", "OpenCode"}, {"antigravity", "Antigravity"}, {"muse", "Muse"}, {"pi", "Pi"}} {
		source.WriteString("const kind" + spec.name + " = ProviderDriverKind.make(\"" + spec.id + "\");\n")
		if spec.id != "pi" {
			source.WriteString("const " + spec.name + "Driver = {driverKind: kind" + spec.name + ", metadata: {displayName:\"" + spec.name + "\", supportsMultipleInstances:true}, configSchema: " + spec.name + "Settings, defaultConfig:()=>decode({}), create:()=>{} };\n")
		}
	}
	source.WriteString("const BUILT_IN_DRIVERS = [\nCursorDriver, GrokDriver, OpenCodeDriver, AntigravityDriver, MuseDriver,\n{driverKind:kindPi,metadata:{displayName:\"Pi\",supportsMultipleInstances:true},configSchema:PiSettings,defaultConfig:()=>decode({}),create:()=>{}}\n];\n")
	source.WriteString("classifyOpenCodeCliVersion selectOpenCodeRuntimeAdapter selectOpenCodeRuntimeTextGeneration decodeApiInfo \"/api/info\" \"@opencode/cli\"")
	return source.String()
}

func TestT3CodeOptionalProviderCapabilitiesRequireRegistryAndFactory(t *testing.T) {
	source := t3CodeOptionalCapabilityFixture()
	providers, second := t3CodeOptionalProviderCapabilities(source)
	if !reflect.DeepEqual(providers, t3CodeOptionalProviders) || !second {
		t.Fatal(providers, second)
	}
	for _, change := range []struct{ old, new string }{{"const BUILT_IN_DRIVERS", "const UNUSED_DRIVERS"}, {"ProviderDriverKind.make(\"cursor\")", "ProviderDriverKind.make(\"other\")"}, {"configSchema: GrokSettings", "configSchema: UnknownSettings"}, {"OpenCodeDriver, AntigravityDriver", "UnknownDriver, AntigravityDriver"}, {"create:()=>{}", "factory:()=>{}"}} {
		providers, _ := t3CodeOptionalProviderCapabilities(strings.ReplaceAll(source, change.old, change.new))
		if len(providers) == 6 {
			t.Fatal("absent server capability advertised", change)
		}
	}
	for _, token := range []string{"classifyOpenCodeCliVersion", "selectOpenCodeRuntimeAdapter", "decodeApiInfo", "\"/api/info\"", "\"@opencode/cli\""} {
		_, second := t3CodeOptionalProviderCapabilities(strings.ReplaceAll(source, token, "missing"))
		if second {
			t.Fatal("partial OpenCode2 runtime accepted", token)
		}
	}
}

func TestT3CodeOptionalReadinessPlanDoesNotExecuteVersionProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic POSIX executable fixture")
	}
	root := t.TempDir()
	marker, binary := filepath.Join(root, "was-executed"), filepath.Join(root, "opencode")
	script := "#!/bin/sh\nprintf executed > " + helperShellQuote(marker) + "\nprintf 'opencode v1.14.19\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	prefs := normalizeT3CodePreferences(t3CodePreferences{})
	prefs.Providers = []string{"opencode"}
	rt := clientLaunchRuntime{platform: "linux", home: t.TempDir(), resolve: func(client, custom string) (string, error) { return binary, nil }}
	if _, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}, rt, root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("readiness executed an external tool", err)
	}
	if _, err := planT3CodeOptionalProviders(prefs, t3CodeInstallation{SupportedProviders: t3CodeOptionalProviders}, rt, root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("prepare did not verify executable version", err)
	}
}
