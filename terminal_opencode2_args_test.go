package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOpenCode2ArgumentsPlaceStandaloneForAcceptedCommands(t *testing.T) {
	dir := t.TempDir()
	// Folders named like subcommands must not turn the subcommand into a
	// project path: OpenCode 2 still parses them as subcommands.
	work := t.TempDir()
	for _, name := range []string{"models", "api", "project"} {
		if err := os.Mkdir(filepath.Join(work, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(work)
	for _, test := range []struct {
		name        string
		input       []string
		want        []string
		unsupported string
		known       bool
	}{
		{"no arguments", nil, []string{"--standalone"}, "", false},
		{"help", []string{"--help"}, []string{"--standalone", "--help"}, "", false},
		{"version", []string{"--version"}, []string{"--standalone", "--version"}, "", false},
		{"root flags", []string{"--continue", "--auto"}, []string{"--standalone", "--continue", "--auto"}, "", false},
		{"absolute directory", []string{dir}, []string{"--standalone", dir}, "", false},
		{"relative directory", []string{"project"}, []string{"--standalone", "project"}, "", false},
		{"path-like word", []string{"./missing"}, []string{"--standalone", "./missing"}, "", false},
		{"run prompt", []string{"run", "msg"}, []string{"run", "--standalone", "msg"}, "", false},
		{"run alone", []string{"run"}, []string{"run", "--standalone"}, "", false},
		{"models", []string{"models", "kilo-local"}, []string{"models", "--standalone", "kilo-local"}, "", false},
		{"models folder present", []string{"models"}, []string{"models", "--standalone"}, "", false},
		{"api folder present", []string{"api", "get"}, []string{"api", "--standalone", "get"}, "", false},
		{"stats", []string{"stats"}, []string{"stats", "--standalone"}, "", false},
		{"session subcommand", []string{"session", "list"}, []string{"session", "list", "--standalone"}, "", false},
		{"auth subcommand", []string{"auth", "login"}, []string{"auth", "login", "--standalone"}, "", false},
		{"bare session", []string{"session"}, []string{"session"}, "", false},
		{"bare auth", []string{"auth"}, []string{"auth"}, "", false},
		{"session help", []string{"session", "--help"}, []string{"session", "--help"}, "", false},
		{"global flag before models", []string{"--print-logs", "models"}, []string{"--print-logs", "models", "--standalone"}, "", false},
		{"log level before run", []string{"--log-level", "debug", "run", "hi"}, []string{"--log-level", "debug", "run", "--standalone", "hi"}, "", false},
		{"inline log level", []string{"--log-level=debug", "session", "list"}, []string{"--log-level=debug", "session", "list", "--standalone"}, "", false},
		{"global flag only", []string{"--print-logs"}, []string{"--standalone", "--print-logs"}, "", false},
		{"mcp", []string{"mcp", "list"}, []string{"mcp", "list"}, "mcp", true},
		{"debug", []string{"debug", "paths"}, []string{"debug", "paths"}, "debug", true},
		{"global flag before mcp", []string{"--print-logs", "mcp", "list"}, []string{"--print-logs", "mcp", "list"}, "mcp", true},
		{"existing standalone", []string{"--standalone", "models"}, []string{"--standalone", "models"}, "", false},
		{"unknown", []string{"frobnicate"}, []string{"frobnicate"}, "frobnicate", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, unsupported, known := openCode2Arguments(test.input)
			if !reflect.DeepEqual(got, test.want) || unsupported != test.unsupported || known != test.known {
				t.Fatalf("args=%q unsupported=%q known=%v, want %q, %q and %v", got, unsupported, known, test.want, test.unsupported, test.known)
			}
		})
	}
	for _, input := range [][]string{{"models"}, {"mcp", "list"}, {"session"}, {"frobnicate"}} {
		first := append([]string(nil), input...)
		got, _, _ := openCode2Arguments(first)
		got[0] = "changed"
		if first[0] != input[0] {
			t.Fatalf("%q: argument placement mutated the caller's arguments", input)
		}
	}
}
