package main

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
