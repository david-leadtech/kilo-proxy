package main

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestManagedToolLinuxSandboxPreflight(t *testing.T) {
	const apparmor = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	const clone = "/proc/sys/kernel/unprivileged_userns_clone"
	const max = "/proc/sys/user/max_user_namespaces"
	for _, test := range []struct {
		name   string
		values map[string]string
		want   string
	}{
		{"allowed", map[string]string{apparmor: "0\n", clone: "1\n", max: "515749\n"}, ""},
		{"optional controls absent", nil, ""},
		{"AppArmor restriction", map[string]string{apparmor: "1\n"}, managedToolLinuxSandboxRequirement},
		{"user namespace cloning disabled", map[string]string{clone: "0\n"}, managedToolLinuxSandboxRequirement},
		{"no user namespaces", map[string]string{max: "0\n"}, managedToolLinuxSandboxRequirement},
		{"invalid policy", map[string]string{clone: "unknown\n"}, managedToolLinuxSandboxUnknown},
		{"empty policy", map[string]string{max: "\n"}, managedToolLinuxSandboxUnknown},
		{"signed policy", map[string]string{max: "-1\n"}, managedToolLinuxSandboxUnknown},
		{"overflow", map[string]string{max: "18446744073709551616\n"}, managedToolLinuxSandboxUnknown},
		{"bounded policy", map[string]string{max: strings.Repeat("1", 65)}, managedToolLinuxSandboxUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				value, ok := test.values[path]
				if !ok {
					return nil, os.ErrNotExist
				}
				return []byte(value), nil
			}
			for _, client := range []string{"codex", "claude-desktop", "openmausbot", "t3-code", "synara"} {
				if got := managedToolSystemRequirementFrom(client, "linux", read); got != test.want {
					t.Errorf("%s requirement = %q, want %q", client, got, test.want)
				}
			}
		})
	}
}

func TestManagedToolLinuxSandboxPreflightDoesNotInspectOtherTools(t *testing.T) {
	read := func(string) ([]byte, error) {
		t.Fatal("system policy must not be inspected for this client/platform")
		return nil, nil
	}
	for _, client := range []string{"codex-cli", "claude", "opencode", "omp", "zed", "open-design", "xcode-chat", "unknown"} {
		if got := managedToolSystemRequirementFrom(client, "linux", read); got != "" {
			t.Errorf("%s unexpectedly blocked: %s", client, got)
		}
	}
	for _, platform := range []string{"darwin", "windows", "freebsd", ""} {
		if got := managedToolSystemRequirementFrom("synara", platform, read); got != "" {
			t.Errorf("%s unexpectedly blocked: %s", platform, got)
		}
	}
}

func TestManagedToolLinuxSandboxPreflightUnreadableIsExplicit(t *testing.T) {
	got := managedToolSystemRequirementFrom("t3-code", "linux", func(string) ([]byte, error) {
		return nil, errors.New("permission denied")
	})
	if got != managedToolLinuxSandboxUnknown {
		t.Fatalf("requirement = %q", got)
	}
}
