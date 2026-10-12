//go:build desktop

package main

import (
	"bytes"
	"encoding/json"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeCLIPathsSavePendingRetryCapabilitiesAndCustomLaunch(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "codex-cli", false, false)
	codex, claude := cliPreferencesTestExecutable(t, "custom Codex"), cliPreferencesTestExecutable(t, "custom Claude")
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var mu sync.Mutex
	posts := []cliPreferences{}
	fail := true
	gate := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nativeCLIPreferencesEndpoint && r.Method == http.MethodPost {
			var next cliPreferences
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				jsonError(w, 400, "bad paths")
				return
			}
			mu.Lock()
			posts = append(posts, next)
			failing := fail
			mu.Unlock()
			if failing {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				jsonError(w, 503, "Synthetic CLI persistence unavailable")
				return
			}
			data, _ := json.Marshal(next)
			r.Body = io.NopCloser(bytes.NewReader(data))
			r.ContentLength = int64(len(data))
		}
		if r.URL.Path == "/api/claude/info" {
			jsonResponse(w, 200, claudeCaps("2.1.288"))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		server.Close()
	})
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	u.page, u.settingsTab = "settings", "cli-tools"
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
	h.frame()
	u.setValue(nativeCLIPathField("codex-cli"), "relative/codex")
	u.saveCLIExecutable("codex-cli", false)
	if !strings.Contains(u.clientState().CLIPreferences.Error, "absolute path") || u.cliPreferencesBusy() {
		t.Fatal("relative CLI path was submitted")
	}
	u.setValue(nativeCLIPathField("codex-cli"), codex)
	u.setValue(nativeCLIPathField("claude"), claude)
	h.frame()
	h.reveal("Save path · Codex CLI", semantic.Button)
	h.click("Save path · Codex CLI", semantic.Button)
	nativeTestWait(t, u, func() bool { mu.Lock(); defer mu.Unlock(); return len(posts) == 1 })
	if u.cliPreferencesReady() || u.clientState().CLIPreferences.Pending == nil {
		t.Fatal("pending paths allowed preparation")
	}
	u.launchAgent("codex-cli")
	if recorder.count() != 0 {
		t.Fatal("opened with unconfirmed CLI path")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.cliPreferencesBusy() })
	if u.clientState().CLIPreferences.Pending == nil || !strings.Contains(u.clientState().CLIPreferences.Error, "persistence unavailable") {
		t.Fatal("failed save lost pending path")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	h.frame()
	h.reveal("Retry saving", semantic.Button)
	h.click("Retry saving", semantic.Button)
	nativeTestWait(t, u, func() bool {
		return !u.cliPreferencesBusy() && u.clientState().CLIPreferences.Pending == nil && !u.busy["GET"+nativeLaunchEndpoint]
	})
	if u.value(nativeCLIPathField("claude")) != claude || !u.cliPathDirty() {
		t.Fatal("saving Codex discarded unsaved Claude path")
	}
	mu.Lock()
	if len(posts) != 2 || posts[0] != posts[1] {
		t.Fatal("retry changed the requested paths")
	}
	mu.Unlock()
	h.frame()
	h.reveal("Save path · Claude Code", semantic.Button)
	h.click("Save path · Claude Code", semantic.Button)
	nativeTestWait(t, u, func() bool {
		return u.cliPreferencesReady() && u.clientState().ClaudeChecked && !u.busy["GET"+nativeLaunchEndpoint]
	})
	if u.clientState().Claude.Version != "2.1.288" {
		t.Fatal("Claude capabilities were not refreshed for its new executable")
	}
	if got := u.clientState().CLIPreferences.Preferences; got.CodexExecutable != codex || got.ClaudeExecutable != claude {
		t.Fatal("saving one CLI changed the other", got)
	}
	u.notice = "" // Earlier pending-save launch was intentionally blocked.
	for _, lang := range []string{"en", "es"} {
		u.setLanguage(lang)
		nativeTestWait(t, u, func() bool {
			return u.language == lang && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
		})
		u.settingsTab = "cli-tools"
		u.uxs().jump = "cli-tools"
		h.frame()
		h.frame()
		nativeGridCapture(t, h, "native-cli-paths-"+lang)
	}
	u.launchAgent("codex-cli")
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if recorder.count() != 1 {
		t.Fatal("custom Codex CLI did not launch", u.notice)
	}
	recorder.mu.Lock()
	plan := recorder.plans[0]
	recorder.mu.Unlock()
	if plan.Executable != codex || plan.Client != "codex-cli" {
		t.Fatal("wrong executable reached the process boundary", plan.Executable)
	}
	u.saveCLIExecutable("codex-cli", true)
	nativeTestWait(t, u, func() bool { return u.cliPreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
	if got := u.clientState().CLIPreferences.Preferences; got.CodexExecutable != "" || got.ClaudeExecutable != claude {
		t.Fatal("automatic Codex detection changed Claude", got)
	}
}

func TestNativeCLIExternalChangesBlockPreparationBeforeAndAfter(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "during"}[during], func(t *testing.T) {
			u, r := nativeLaunchTestUI(t, "codex-cli", during, false)
			custom := cliPreferencesTestExecutable(t, "selected Codex")
			if !during {
				postCLISelection(t, u.owner, cliPreferences{Version: 1, CodexExecutable: custom}, 200)
			}
			finished := false
			var failure error
			u.notice = ""
			u.prepareClientAfter("codex-cli", func(err error) { failure, finished = err, true })
			if during {
				nativeTestWait(t, u, func() bool { return r.prepares.Load() == 1 })
				postCLISelection(t, u.owner, cliPreferences{Version: 1, CodexExecutable: custom}, 200)
				r.unblock()
			}
			nativeTestWait(t, u, func() bool { return finished })
			if failure == nil || !strings.Contains(failure.Error(), "settings changed") {
				t.Fatal("external executable change was accepted", failure)
			}
			if r.count() != 0 || u.sharedClientSelection("codex-cli").Saved != "" || u.noticeTone == nativeToneSuccess {
				t.Fatal("stale profile was marked ready or opened", u.notice)
			}
			if u.value(nativeCLIPathField("codex-cli")) != custom {
				t.Fatal("external saved path not shown")
			}
		})
	}
}

func TestNativeCLICapabilityProbeCannotRestoreOldExecutableCapabilities(t *testing.T) {
	u, _ := nativeLaunchTestUI(t, "claude", false, false)
	custom := cliPreferencesTestExecutable(t, "selected Claude")
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	gate := make(chan struct{})
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/claude/info" {
			n := probes.Add(1)
			if n == 1 {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				jsonResponse(w, 200, claudeCaps("2.1.251"))
			} else {
				jsonResponse(w, 200, claudeCaps("2.1.288"))
			}
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		server.Close()
	})
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
	u.clientState().ClaudeDetectStarted = false
	u.detectAgentCapabilities()
	nativeTestWait(t, u, func() bool { return probes.Load() == 1 })
	u.setValue(nativeCLIPathField("claude"), custom)
	u.saveCLIExecutable("claude", false)
	nativeTestWait(t, u, func() bool {
		return !u.cliPreferencesBusy() && u.clientState().CLIPreferences.Preferences.ClaudeExecutable == custom
	})
	if u.clientState().ClaudeChecked {
		t.Fatal("capabilities for old executable remained usable")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return probes.Load() == 2 && u.clientState().ClaudeChecked })
	if u.clientState().Claude.Version != "2.1.288" {
		t.Fatal("in-flight old probe overwrote new executable capabilities")
	}
}
