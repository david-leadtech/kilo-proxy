package main

import (
	"os"
	"strings"
)

// OpenCode 2 parses --standalone per command, not globally: "opencode models
// --standalone" works while "opencode --standalone models" is rejected.
var (
	openCode2StandaloneCommands    = map[string]bool{"run": true, "models": true, "stats": true, "mini": true, "reload": true, "api": true}
	openCode2StandaloneSubcommands = map[string]bool{"session": true, "auth": true}
	openCode2LocalCommands         = map[string]bool{"mcp": true, "debug": true, "plugin": true, "acp": true, "serve": true, "service": true, "pair": true, "upgrade": true, "uninstall": true}
)

// openCode2Arguments places --standalone where OpenCode 2 accepts it. When a
// command cannot receive the flag it returns the arguments unchanged together
// with the command name, and known reports whether OpenCode 2 is known to
// reject the flag for it (false means an unrecognised word).
func openCode2Arguments(userArgs []string) (args []string, unsupported string, known bool) {
	for _, arg := range userArgs {
		if arg == "--standalone" {
			return append([]string(nil), userArgs...), "", false
		}
	}
	// Global logging flags may precede a subcommand; the subcommand still owns
	// --standalone.
	index := 0
	for index < len(userArgs) {
		arg := userArgs[index]
		if arg == "--print-logs" || strings.HasPrefix(arg, "--log-level=") {
			index++
		} else if arg == "--log-level" {
			index += 2
		} else {
			break
		}
	}
	if index >= len(userArgs) {
		return openCode2InsertStandalone(userArgs, 0), "", false
	}
	word := userArgs[index]
	// Match subcommands before anything else: a project folder named "models"
	// or "api" is still parsed by OpenCode 2 as that subcommand.
	switch {
	case openCode2StandaloneCommands[word]:
		return openCode2InsertStandalone(userArgs, index+1), "", false
	case openCode2StandaloneSubcommands[word]:
		// Only "session <sub>" and "auth <sub>" accept the flag; the bare
		// command or its own flags print help.
		if index+1 < len(userArgs) && !strings.HasPrefix(userArgs[index+1], "-") {
			return openCode2InsertStandalone(userArgs, index+2), "", false
		}
		return append([]string(nil), userArgs...), "", false
	case openCode2LocalCommands[word]:
		return append([]string(nil), userArgs...), word, true
	case strings.HasPrefix(word, "-") || openCode2RootArgument(word):
		// Root flags and a project folder belong to the TUI command.
		return openCode2InsertStandalone(userArgs, 0), "", false
	}
	return append([]string(nil), userArgs...), word, false
}

func openCode2RootArgument(arg string) bool {
	if arg == "." || arg == ".." || strings.ContainsAny(arg, `/\`) || len(arg) >= 2 && arg[1] == ':' {
		return true
	}
	info, err := os.Stat(arg)
	return err == nil && info.IsDir()
}

func openCode2InsertStandalone(userArgs []string, index int) []string {
	args := make([]string, 0, len(userArgs)+1)
	args = append(args, userArgs[:index]...)
	args = append(args, "--standalone")
	args = append(args, userArgs[index:]...)
	return args
}
