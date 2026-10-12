package main

import (
	"os"
	"strconv"
	"strings"
)

const managedToolLinuxSandboxRequirement = "Linux security policy does not allow this app's sandbox in a private installation. Use the vendor's official system package instead."
const managedToolLinuxSandboxUnknown = "Cannot verify the Linux sandbox requirements for a private installation. Use the vendor's official system package instead."

// Private Electron copies cannot install a root-owned setuid helper or attach a
// system AppArmor profile to their changing versioned paths. Reject known host
// restrictions before offering a download. This is a preflight, not a promise
// that every remaining host has all the libraries and policies the app needs.
func managedToolSystemRequirement(client, platform string) string {
	return managedToolSystemRequirementFrom(client, platform, os.ReadFile)
}

func managedToolSystemRequirementFrom(client, platform string, read func(string) ([]byte, error)) string {
	if platform != "linux" {
		return ""
	}
	switch client {
	case "codex", "claude-desktop", "openmausbot", "t3-code", "synara":
	default:
		return ""
	}
	policies := []struct {
		path       string
		restrictOn uint64
	}{
		{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns", 1},
		{"/proc/sys/kernel/unprivileged_userns_clone", 0},
		{"/proc/sys/user/max_user_namespaces", 0},
	}
	for _, policy := range policies {
		data, err := read(policy.path)
		// Not every Linux kernel exports these optional controls. Their absence
		// does not mean user namespaces are disabled.
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || len(data) > 64 {
			return managedToolLinuxSandboxUnknown
		}
		value := strings.TrimSpace(string(data))
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return managedToolLinuxSandboxUnknown
		}
		number, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return managedToolLinuxSandboxUnknown
		}
		if number == policy.restrictOn {
			return managedToolLinuxSandboxRequirement
		}
	}
	return ""
}
