//go:build desktop

package main

import "testing"

func TestNativeHarnessInstalledStateDoesNotImplyLaunchAvailability(t *testing.T) {
	u := nativeTestUI(t)
	c := u.clientState()
	c.LaunchChecked = true
	c.LaunchInfo.Clients = map[string]nativeLaunchClient{}
	for _, key := range []string{"synara", "t3-code", "open-design", "claude-desktop", "codex-cli"} {
		for _, info := range []nativeLaunchClient{{Installed: true, Reason: "synthetic requirement missing"}, {Path: "/synthetic/installed", Reason: "synthetic compatibility error"}} {
			c.LaunchInfo.Clients[key] = info
			if u.uxhInstallState(key) != uxhInstalled {
				t.Fatal("installed tool was put in missing group", key, info)
			}
			if u.nativeLaunchAvailable(key) {
				t.Fatal("installation classification bypassed readiness", key)
			}
		}
		c.LaunchInfo.Clients[key] = nativeLaunchClient{}
		if u.uxhInstallState(key) != uxhMissing {
			t.Fatal("missing installation was called installed", key)
		}
	}
}
