package main

import (
	"os"
	"path/filepath"
	"strings"
)

// openCode2Arguments places --standalone where OpenCode 2 accepts it.
// It returns the final argv and, when the command does not accept the flag,
// its name so the caller can warn.
func openCode2Arguments(userArgs []string) (args []string, unsupported string) {
	for _, arg := range userArgs {
		if arg == "--standalone" {
			return append([]string(nil), userArgs...), ""
		}
	}
	if len(userArgs) == 0 {
		return []string{"--standalone"}, ""
	}
	first := userArgs[0]
	// Root and global flags, directories and path-like words are read before a
	// subcommand, so --standalone must precede them.
	if strings.HasPrefix(first, "-") || openCode2RootArgument(first) {
		return append([]string{"--standalone"}, userArgs...), ""
	}
	switch first {
	case "run", "models", "stats", "mini", "reload", "api":
		return openCode2InsertStandalone(userArgs, 1), ""
	case "session", "auth":
		// These accept --standalone only after their own subcommand, e.g.
		// "session list --standalone" or "auth login --standalone".
		if len(userArgs) > 1 {
			return openCode2InsertStandalone(userArgs, 2), ""
		}
		return openCode2InsertStandalone(userArgs, 1), ""
	case "mcp", "debug", "plugin", "acp", "serve", "service", "pair", "upgrade", "uninstall":
		return userArgs, first
	default:
		// An unknown subcommand may or may not accept the flag; leave the
		// invocation untouched and let the caller warn.
		return userArgs, first
	}
}

func openCode2RootArgument(arg string) bool {
	if arg == "." || arg == ".." || filepath.IsAbs(arg) || strings.ContainsRune(arg, filepath.Separator) {
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
