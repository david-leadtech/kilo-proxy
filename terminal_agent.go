package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

func runTerminalAgentMode(args []string) int {
	cleanup, err := prepareTerminalAgentConsole()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot connect to the current terminal.")
		return 1
	}
	defer cleanup()
	if !terminalPlatformSupported(runtime.GOOS) || len(args) == 0 || !terminalClientSupported(args[0]) {
		fmt.Fprintln(os.Stderr, "Use kilo-codex, kilo-claude, kilo-omp, kilo-opencode or kilo-muse on macOS, Linux or Windows.")
		return 1
	}
	flags := flag.NewFlagSet(terminalAgentFlag, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("config-dir", "", "Kilo Proxy configuration directory")
	encoded := flags.String("encoded-args", "", "Encoded argument array for PowerShell compatibility")
	if flags.Parse(args[1:]) != nil {
		fmt.Fprintln(os.Stderr, "Invalid terminal launcher arguments.")
		return 1
	}
	arguments := flags.Args()
	encodedSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "encoded-args" {
			encodedSet = true
		}
	})
	if encodedSet {
		if len(arguments) != 0 {
			fmt.Fprintln(os.Stderr, "Encoded terminal arguments cannot be combined with trailing arguments.")
			return 1
		}
		arguments, err = decodeTerminalArguments(*encoded)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Invalid encoded terminal arguments.")
			return 1
		}
	}
	if *dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Cannot locate Kilo Proxy settings.")
			return 1
		}
		*dir = filepath.Join(base, "kilo-proxy")
	}
	if !filepath.IsAbs(*dir) {
		fmt.Fprintln(os.Stderr, "The Kilo Proxy configuration directory must be absolute.")
		return 1
	}
	client := args[0]
	if client == "opencode" {
		config, configErr := readSettings(*dir)
		if configErr != nil {
			fmt.Fprintln(os.Stderr, "Cannot read Kilo Proxy settings.")
			return 1
		}
		if config.OpenCodeMajor == 2 {
			client = "opencode2"
		}
	}
	executable, err := resolveTerminalExecutable(*dir, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	directory, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Cannot read the current project directory.")
		return 1
	}
	input := terminalPrepareRequest{Client: client, Directory: directory}
	if input.Client == "claude" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		installedVersion, _, err := managedToolInstalledVersion(ctx, "claude", executable, runtime.GOOS, runtime.GOARCH)
		cancel()
		if err == nil {
			input.ClaudeVersion = claudeCaps(installedVersion).Version
		}
		if len(input.ClaudeVersion) > 64 {
			input.ClaudeVersion = ""
		}
	}
	var plan clientLaunchPlan
	if input.Client == "muse" {
		cfg, configErr := readSettings(*dir)
		if configErr != nil {
			fmt.Fprintln(os.Stderr, "Cannot read Kilo Proxy settings.")
			return 1
		}
		input.MuseConnection = normalizeMusePreferences(cfg.MuseCode).Connection
		input.MuseExecutable = executable
		if input.MuseConnection == "native" {
			plan = clientLaunchPlan{Client: "muse", Name: "Muse Code", Kind: "terminal", Directory: directory, Env: map[string]string{}}
			applyMuseNativeLaunch(&plan)
		} else {
			if err := validateMuseKiloArguments(arguments); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			info, runtimeErr := readTerminalRuntime(*dir)
			if runtimeErr != nil || info.MuseProfile == "" {
				fmt.Fprintln(os.Stderr, errTerminalAppUnavailable)
				return 1
			}
			home, homeErr := os.UserHomeDir()
			if homeErr != nil {
				fmt.Fprintln(os.Stderr, "Cannot resolve the normal Muse Code profile.")
				return 1
			}
			if _, isolationErr := museKiloIsolation(info.MuseProfile, museNormalPaths(home, os.Environ(), runtime.GOOS)); isolationErr != nil {
				fmt.Fprintln(os.Stderr, isolationErr)
				return 1
			}
			plan, err = requestTerminalPlan(context.Background(), *dir, input)
		}
	} else {
		plan, err = requestTerminalPlan(context.Background(), *dir, input)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if input.Client == "muse" && input.MuseConnection == "kilo" {
		// The invoking terminal may have additional provider variables which
		// were absent from the app's environment when the plan was produced.
		plan.Unset = museKiloInheritedEnvironment(os.Environ())
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			fmt.Fprintln(os.Stderr, "Cannot resolve the normal Muse Code profile.")
			return 1
		}
		if _, isolationErr := museKiloIsolation(plan.Env["HOME"], museNormalPaths(home, os.Environ(), runtime.GOOS)); isolationErr != nil {
			fmt.Fprintln(os.Stderr, isolationErr)
			return 1
		}
	}
	plan.Executable = executable
	if input.Client == "opencode2" {
		// The wrapper may run in a terminal whose OPENCODE_* overrides were not
		// present when the app produced its plan.
		plan.Unset = openCode2UnsetEnvironment(os.Environ())
		args, unsupported := openCode2Arguments(arguments)
		plan.Args = args
		if unsupported != "" {
			fmt.Fprintf(os.Stderr, "kilo-opencode: OpenCode 2 does not accept --standalone for %q; it may wait for or conflict with your regular OpenCode background service.\n", unsupported)
		}
	}
	if err := validateClientProcessPlan(plan); err != nil {
		fmt.Fprintln(os.Stderr, "Invalid terminal launch parameters.")
		return 1
	}
	if input.Client != "opencode2" {
		// The user's original arguments are already an argv vector. Preserve them
		// without applying the GUI launch-ticket size limits to prompts or lists.
		plan.Args = append(plan.Args, arguments...)
	}
	if err := execTerminalAgent(plan); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "Could not execute the installed agent:", err)
		return 1
	}
	return 0
}

// The controller returns only a private profile, never executable paths. Resolve
// the invoking terminal's real installation using the same saved selections as
// GUI/T3 launches, then check OpenCode's major before preparing or executing it.
func resolveTerminalExecutable(dir, client string) (string, error) {
	config, err := readSettings(dir)
	if err != nil {
		return "", errors.New("Cannot read Kilo Proxy settings.")
	}
	a := &app{dir: dir, cliExecutables: [2]string{config.CodexCLIExecutable, config.ClaudeCodeExecutable}, museExecutable: config.MuseCodeExecutable,
		openCodeExecutables: [2]string{config.OpenCode1Executable, config.OpenCode2Executable}}
	resolved, major := client, 0
	if client == "opencode" || client == "opencode2" {
		// The installed kilo-opencode wrapper always passes the OpenCode 1 client
		// identity. The saved major decides which installation and validation
		// apply, while the wrapper keeps its original name.
		resolved, major = "opencode", 1
		if client == "opencode2" || config.OpenCodeMajor == 2 {
			resolved, major = "opencode2", 2
		}
	}
	executable, err := a.launchRuntime().resolve(resolved, "")
	if err != nil {
		return "", err
	}
	if major != 0 {
		if err := a.checkOpenCodeVersion(executable, major); err != nil {
			return "", err
		}
	}
	return executable, nil
}
