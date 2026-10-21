package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestT3CodeKiloContractAcceptsTracedDriverFactories(t *testing.T) {
	for _, provider := range []struct {
		name    string
		source  string
		factory string
		check   func(string) bool
	}{
		{"OpenCode", t3CodeOpenCodeKiloContractFixture(), "create: ({ instanceId, environment, config }) => Effect.gen(function* () {", t3CodeOpenCodeKiloSupported},
		{"Muse", t3CodeMuseKiloContractFixture(), "create: ({ instanceId, environment, config }) => Effect.gen(function* () {", t3CodeMuseKiloSupported},
	} {
		t.Run(provider.name, func(t *testing.T) {
			for _, wrapper := range []string{"Effect.fn", "fn", "fn$2"} {
				t.Run(wrapper, func(t *testing.T) {
					factory := "create: " + wrapper + "(\"" + provider.name + "Driver.create\")(function* ({ instanceId, environment, config }) {"
					traced := strings.Replace(provider.source, provider.factory, factory, 1)
					if traced == provider.source {
						t.Fatal("invalid traced factory fixture")
					}
					if !provider.check(traced) {
						t.Fatal("complete traced native instance contract rejected")
					}
					for _, change := range []struct{ name, old, next string }{
						{"factory-environment", "function* ({ instanceId, environment, config })", "function* ({ instanceId, config })"},
						{"aliased-environment", "function* ({ instanceId, environment, config })", "function* ({ instanceId, environment: inherited, config })"},
						{"rest-environment", "function* ({ instanceId, environment, config })", "function* ({ instanceId, ...environment })"},
						{"factory-generator", "\")(function* ({", "\")(function ({"},
						{"factory-wrapper", wrapper + "(\"", "unknownWrapper(\""},
						{"multiple-instances", "supportsMultipleInstances: true", "supportsMultipleInstances: false"},
						{"commented-schema", "configSchema: " + provider.name + "Settings,", "configSchema: OtherSettings, /* configSchema: " + provider.name + "Settings, defaultConfig:()=>decode({}), create: */"},
						{"instance-merge", "mergeProviderInstanceEnvironment(environment", "mergeProviderInstanceEnvironment([]"},
						{"copied-environment", "const next = { ...baseEnv }", "const next = {}"},
						{"duplicate-factory", "defaultConfig:", "create: ({ environment }) => environment,\n defaultConfig:"},
					} {
						t.Run(change.name, func(t *testing.T) {
							changed := strings.Replace(traced, change.old, change.next, 1)
							if changed == traced {
								t.Fatal("invalid negative traced fixture")
							}
							if provider.check(changed) {
								t.Fatal("incomplete traced instance contract accepted")
							}
						})
					}
					for _, nonLive := range []string{"/*\n" + traced + "\n*/", "`\n" + traced + "\n`"} {
						if provider.check(nonLive) {
							t.Fatal("commented or quoted traced contract accepted")
						}
					}
				})
			}
			if !provider.check(provider.source) {
				t.Fatal("legacy arrow factory rejected")
			}
		})
	}
}

// This opt-in check reads an explicit desktop package without launching T3.
func TestT3CodeKiloInstalledPackageContract(t *testing.T) {
	executable := os.Getenv("KILO_TEST_T3_PACKAGE")
	if executable == "" {
		t.Skip("set an explicit official T3 desktop path for package acceptance")
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("official T3 desktop path must be absolute")
	}
	installation, err := inspectT3CodeInstallation(executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if !installation.OpenCodeKiloSupported || !installation.MuseKiloSupported {
		t.Fatalf("official T3 package lacks scoped accounts: OpenCode=%v Muse=%v", installation.OpenCodeKiloSupported, installation.MuseKiloSupported)
	}
}
