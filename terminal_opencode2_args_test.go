package main

import (
	"reflect"
	"testing"
)

func TestOpenCode2ArgumentsPlaceStandaloneForAcceptedCommands(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name        string
		input       []string
		want        []string
		unsupported string
	}{
		{"no arguments", nil, []string{"--standalone"}, ""},
		{"help", []string{"--help"}, []string{"--standalone", "--help"}, ""},
		{"version", []string{"--version"}, []string{"--standalone", "--version"}, ""},
		{"directory", []string{dir}, []string{"--standalone", dir}, ""},
		{"run prompt", []string{"run", "msg"}, []string{"run", "--standalone", "msg"}, ""},
		{"models", []string{"models", "kilo-local"}, []string{"models", "--standalone", "kilo-local"}, ""},
		{"stats", []string{"stats"}, []string{"stats", "--standalone"}, ""},
		{"session subcommand", []string{"session", "list"}, []string{"session", "list", "--standalone"}, ""},
		{"auth subcommand", []string{"auth", "login"}, []string{"auth", "login", "--standalone"}, ""},
		{"mcp", []string{"mcp", "list"}, []string{"mcp", "list"}, "mcp"},
		{"debug", []string{"debug", "paths"}, []string{"debug", "paths"}, "debug"},
		{"existing standalone", []string{"--standalone", "models"}, []string{"--standalone", "models"}, ""},
		{"unknown", []string{"frobnicate"}, []string{"frobnicate"}, "frobnicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, unsupported := openCode2Arguments(test.input)
			if !reflect.DeepEqual(got, test.want) || unsupported != test.unsupported {
				t.Fatalf("args=%q unsupported=%q, want %q and %q", got, unsupported, test.want, test.unsupported)
			}
		})
	}
	first := []string{"models"}
	got, _ := openCode2Arguments(first)
	got[0] = "changed"
	if first[0] != "models" {
		t.Fatal("standalone insertion mutated the caller's arguments")
	}
}
