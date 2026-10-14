package main

import (
	"errors"
	"os"
	"strings"
)

// Muse uses its own Meta authentication, model catalog, approvals and sandbox.
// Never assign Kilo gateway credentials or reinterpret native Muse arguments.
// Pin the chosen installation: its launcher must not update it behind Settings.
func applyMuseNativeLaunch(plan *clientLaunchPlan) {
	plan.skipProxy = true
	if plan.Env == nil {
		plan.Env = map[string]string{}
	}
	plan.Env["MUSE_NO_AUTO_UPDATE"] = "1"
	plan.Unset = append(plan.Unset, "KILO_LOCAL_API_KEY")
}

func applyMuseKiloLaunch(plan *clientLaunchPlan, home string) {
	plan.Env = museKiloEnvironment(home)
	plan.Unset = museKiloInheritedEnvironment(os.Environ())
}

func validateMuseKiloArguments(args []string) error {
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "auth" || arg == "login" || arg == "logout" || arg == "--provider" || strings.HasPrefix(arg, "--provider=") || arg == "--base-url" || strings.HasPrefix(arg, "--base-url=") || arg == "--config" || strings.HasPrefix(arg, "--config=") || strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--") {
			return errors.New("The Kilo Muse connection manages its provider, transport and credentials. Change its connection in the Muse Code settings in Kilo Proxy to use Meta login or custom transport options.")
		}
	}
	return nil
}
