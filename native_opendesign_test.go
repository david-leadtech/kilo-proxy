//go:build desktop

package main

import (
	"encoding/json"
	"gioui.org/io/semantic"
	"image"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// Keep the normal launch deadline and process boundary. Capture the outstanding
// stage if a platform stalls, and save completed timings with CI screenshots.
func nativeOpenDesignLaunchDiagnostics(t *testing.T, u *nativeUI, r *nativeLaunchRecorder, engine string, started time.Time) {
	t.Helper()
	busy := make([]string, 0, len(u.busy))
	for request, active := range u.busy {
		if active {
			busy = append(busy, request)
		}
	}
	sort.Strings(busy)
	report := struct {
		Engine         string                      `json:"engine"`
		Elapsed        string                      `json:"elapsed"`
		Failed         bool                        `json:"failed"`
		Phase          string                      `json:"phase"`
		Launching      string                      `json:"launching"`
		Busy           []string                    `json:"busy"`
		Prepares       int32                       `json:"prepares"`
		LaunchRequests int32                       `json:"launchRequests"`
		Requests       []nativeLaunchRequestTiming `json:"requests"`
	}{engine, time.Since(started).String(), t.Failed(), u.agentsState().Phase, u.clientState().Launching, busy, r.prepares.Load(), r.launchRequests.Load(), r.requestTimings()}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if t.Failed() {
		t.Logf("OpenDesign launch diagnostics: %s", data)
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		t.Logf("OpenDesign goroutines at launch failure (truncated=%v):\n%s", n == len(stack), stack[:n])
	}
	if directory := os.Getenv("KILO_NATIVE_SCREENSHOTS"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Error(err)
			return
		}
		path := filepath.Join(directory, "native-open-design-launch-"+engine+"-timing.json")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}
}

func TestNativeOpenDesignLaunchPreparesSelectedEngineAndStartsProxy(t *testing.T) {
	for _, engine := range []string{"codex-cli", "claude", "opencode"} {
		t.Run(engine, func(t *testing.T) {
			u, r := nativeLaunchTestUI(t, "open-design", false, false)
			defer nativeOpenDesignLaunchDiagnostics(t, u, r, engine, time.Now())
			label := "Codex CLI"
			if engine == "claude" {
				label = "Claude Code"
			} else if engine == "opencode" {
				label = "OpenCode"
			}
			u.setValue("open-design-engine", label)
			u.agentsState().Preferences.Folder = "/ignored/project"
			before := u.owner.launcher.start
			u.owner.launcher.start = func(plan clientLaunchPlan) error {
				u.owner.mu.Lock()
				listener := u.owner.proxyListener
				u.owner.mu.Unlock()
				if listener == nil {
					t.Error("proxy missing before Open Design launch")
				} else {
					conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
					if err != nil {
						t.Error(err)
					} else {
						conn.Close()
					}
				}
				return before(plan)
			}
			nativeTestFrame(t, u)
			u.clickable("client:open-design:launch").Click()
			nativeTestFrame(t, u)
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
			if r.count() != 1 || r.prepares.Load() != 1 {
				t.Fatalf("launch didn't prepare and open once: %s", u.notice)
			}
			saved, err := u.owner.openDesignReadPrepared()
			if err != nil || saved.Engine != engine || saved.Library.DefaultModel != "vendor/one" {
				t.Fatalf("wrong engine/library: %+v %v", saved, err)
			}
			r.mu.Lock()
			plan := r.plans[0]
			r.mu.Unlock()
			if len(plan.Args) != 0 || plan.Env["KILO_LOCAL_API_KEY"] != u.owner.config.LocalKey || plan.Env["OD_PACKAGED_NAMESPACE"] != openDesignNamespace(u.owner.dir) {
				t.Fatal("launch lost engine profile isolation or local authentication")
			}
			bridge := u.owner.desktop.(*nativeRecordingBridge)
			bridge.mu.Lock()
			copied := bridge.Text
			bridge.mu.Unlock()
			if copied != "" {
				t.Fatal("automatic flow copied credentials")
			}
			prefs, err := readAgentPreferences(u.owner.dir)
			if err != nil || prefs.Projects["open-design"] != "" {
				t.Fatal("remembered unsupported project handoff")
			}
		})
	}
}

func TestNativeOpenDesignLaunchGates(t *testing.T) {
	for _, blocked := range []string{"credentials", "installation", "engine", "models", "unsaved-connection", "busy-connection", "port-in-use"} {
		t.Run(blocked, func(t *testing.T) {
			u, r := nativeLaunchTestUI(t, "open-design", false, false)
			switch blocked {
			case "credentials":
				u.owner.mu.Lock()
				u.owner.apiKey = ""
				u.owner.publishUIProjectionLocked()
				u.owner.mu.Unlock()
			case "installation":
				info := u.clientState().LaunchInfo.Clients["open-design"]
				info.Available = false
				u.clientState().LaunchInfo.Clients["open-design"] = info
			case "engine":
				info := u.clientState().OpenDesign.Engines["codex-cli"]
				info.Available = false
				u.clientState().OpenDesign.Engines["codex-cli"] = info
			case "models":
				nativeSeedSharedForTest(t, u)
			case "unsaved-connection":
				u.owner.mu.Lock()
				u.owner.connectionNeedsSave = true
				u.owner.publishUIProjectionLocked()
				u.owner.mu.Unlock()
			case "busy-connection":
				u.busy["POST/api/config"] = true
			case "port-in-use":
				listener, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { listener.Close() })
				u.owner.mu.Lock()
				u.owner.config.Port = listener.Addr().(*net.TCPAddr).Port
				u.owner.publishUIProjectionLocked()
				u.owner.mu.Unlock()
			}
			nativeTestFrame(t, u)
			u.clickable("client:open-design:launch").Click()
			nativeTestFrame(t, u)
			nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
			if r.count() != 0 {
				t.Fatalf("launch escaped %s gate", blocked)
			}
		})
	}
}

func TestNativeOpenDesignChangedEngineDuringPreparationDoesNotLaunch(t *testing.T) {
	u, r := nativeLaunchTestUI(t, "open-design", true, false)
	u.launchAgent("open-design")
	r.waitForPrepare(t, u)
	u.setValue("open-design-engine", "Claude Code")
	r.unblock()
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if r.count() != 0 || !strings.Contains(u.notice, "changed while preparing") {
		t.Fatalf("stale engine launched: %s", u.notice)
	}
}

func TestNativeOpenDesignPointerLayoutAndNoManualSecrets(t *testing.T) {
	for _, size := range []image.Point{{1180, 820}, {720, 700}} {
		for _, language := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+language, func(t *testing.T) {
				u, r := nativeLaunchTestUI(t, "open-design", false, false)
				u.language = language
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				nativeGridCapture(t, h, "native-open-design-cli-"+fmtSize(size)+"-"+language)
				label := u.tr("Launch Open Design", "Abrir Open Design")
				if !h.target(label, semantic.Button).Desc.Bounds.In(image.Rectangle{Max: size}) {
					t.Fatal("launch clipped")
				}
				for _, node := range h.nodes() {
					if strings.Contains(node.Desc.Label, u.owner.config.LocalKey) || strings.Contains(node.Desc.Label, "Copy local") || strings.Contains(node.Desc.Label, "Project folder") {
						t.Fatal("manual key or unsupported folder shown")
					}
				}
				h.click(label, semantic.Button)
				nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
				if r.count() != 1 || r.prepares.Load() != 1 {
					t.Fatalf("pointer launch failed: %s", u.notice)
				}
			})
		}
	}
}

func TestNativeOpenDesignPreparedProfilesUseSharedUpdates(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "open-design", false, false)
	nativeSeedSharedForTest(t, u, nativeClientModelsForTest()...)
	u.setValue(nativeClientField(sharedModelKey, "vendor/one", "name"), "Friendly model")
	u.persistLibraryEdits()
	u.flushModelLibrary()
	u.prepareClient("open-design")
	nativeTestWait(t, u, func() bool {
		return recorder.prepares.Load() == 1 && !u.busy["POST"+openDesignProfileEndpoint] && !u.cliPreferencesBusy()
	})
	paths := openDesignProfilePaths(u.owner.dir)
	data, err := os.ReadFile(filepath.Join(paths.Profiles, "codex-cli", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Friendly model") {
		t.Fatal("shared display name missing")
	}
	var prefs map[string]json.RawMessage
	data, err = os.ReadFile(paths.Config)
	if err != nil || json.Unmarshal(data, &prefs) != nil {
		t.Fatal("missing Open Design preferences")
	}
	if string(prefs["agentId"]) != "\"codex\"" {
		t.Fatal("wrong selected runtime")
	}
}
