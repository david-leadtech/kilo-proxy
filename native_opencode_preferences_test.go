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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeOpenCodeVersionSaveFailureRetryAndSeparateProfile(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "opencode2", false, false)
	u.client = "opencode"
	version2 := syntheticOpenCodeExecutable(t, filepath.Join(u.owner.editorTestRoot, "version-two"))
	// The native test dispatcher never executes a CLI. Real version parsing is
	// tested by the backend; this boundary reports only our synthetic binaries.
	u.owner.openCodeVersionProbe = func(binary string) (string, error) {
		if binary == version2 {
			return "2.0.27", nil
		}
		return "1.19.0", nil
	}
	previous := u.owner.launcher.resolve
	u.owner.launcher.resolve = func(client, custom string) (string, error) {
		if client == "opencode2" {
			return version2, nil
		}
		return previous(client, custom)
	}
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	var mu sync.Mutex
	fail, gate, posts := true, make(chan struct{}), []openCodePreferences{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nativeOpenCodePreferencesEndpoint && r.Method == http.MethodPost {
			var next openCodePreferences
			if json.NewDecoder(r.Body).Decode(&next) != nil {
				jsonError(w, 400, "bad preferences")
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
				jsonError(w, 503, "Synthetic version settings unavailable")
				return
			}
			body, _ := json.Marshal(next)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
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
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
	h.frame()
	h.reveal("OpenCode 2", semantic.Button)
	h.click("OpenCode 2", semantic.Button)
	nativeTestWait(t, u, func() bool { mu.Lock(); defer mu.Unlock(); return len(posts) == 1 })
	state := &u.clientState().OpenCodePreferences
	if state.Preferences.Major != 1 || state.Pending == nil || u.openCodePreferencesReady() {
		t.Fatal("pending version was confirmed or allowed opening")
	}
	u.launchAgent("opencode")
	if recorder.count() != 0 {
		t.Fatal("opened while version was unconfirmed")
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return !u.openCodePreferencesBusy() })
	if state.Error == "" || state.Preferences.Major != 1 || state.Pending == nil {
		t.Fatal("failed save lost the confirmed version or pending choice")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	h.frame()
	h.reveal("Retry saving", semantic.Button)
	h.click("Retry saving", semantic.Button)
	nativeTestWait(t, u, func() bool {
		return u.openCodePreferencesReady() && state.Preferences.Major == 2 && !u.busy["GET"+nativeLaunchEndpoint]
	})
	u.owner.mu.Lock()
	major := u.owner.config.OpenCodeMajor
	u.owner.mu.Unlock()
	if major != 2 || u.openCodeClientKey() != "opencode2" {
		t.Fatal("selected version was not persisted or routed")
	}
	mu.Lock()
	if len(posts) != 2 || posts[0] != posts[1] {
		t.Fatal("retry changed the requested version")
	}
	mu.Unlock()
	projectRoot := ""
	if runtime.GOOS == "darwin" {
		projectRoot = "/private/tmp"
	}
	project, err := os.MkdirTemp(projectRoot, "kp612-ui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(project) })
	u.agentsState().Preferences.useFolder(project, time.Now())
	u.notice = ""
	for _, lang := range []string{"en", "es"} {
		u.setLanguage(lang)
		nativeTestWait(t, u, func() bool {
			return u.language == lang && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
		})
		list := u.list("page." + u.page)
		list.Position.First, list.Position.Offset = 0, 0
		h.frame()
		nativeGridCapture(t, h, "native-opencode-version-"+lang)
	}
	u.launchAgent("opencode")
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if recorder.count() != 1 {
		t.Fatalf("selected OpenCode 2 did not launch: %s", u.notice)
	}
	recorder.mu.Lock()
	plan := recorder.plans[0]
	recorder.mu.Unlock()
	if plan.Client != "opencode2" || plan.Executable != version2 || !strings.Contains(plan.Env["HOME"], string(filepath.Separator)+"opencode2"+string(filepath.Separator)) {
		t.Fatalf("version choice reached the wrong runtime: %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(u.owner.editorTestRoot, "opencode.json")); err == nil {
		t.Fatal("OpenCode 2 prepared the version 1 configuration")
	}
}

func TestNativeOpenCodeVersionExternalChoiceBlocksPreparation(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "opencode", false, false)
	u.owner.mu.Lock()
	u.owner.config.OpenCodeMajor = 2 // A different settings window confirmed this choice.
	u.owner.publishUIProjectionLocked()
	u.owner.mu.Unlock()
	before := u.sharedClientSelection("opencode").Path
	finished := false
	var failure error
	u.prepareClientAfter("opencode", func(err error) { failure, finished = err, true })
	nativeTestWait(t, u, func() bool { return finished })
	if failure == nil || !strings.Contains(failure.Error(), "settings changed") || u.openCodeClientKey() != "opencode2" {
		t.Fatalf("external version choice was ignored: %v, %+v", failure, u.clientState().OpenCodePreferences)
	}
	if recorder.count() != 0 || u.sharedClientSelection("opencode").Path != before {
		t.Fatal("stale version prepared or opened a profile")
	}
}

func TestNativeOpenCodeExecutablePathsSavePerVersionAndReturnToDetection(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "opencode", false, false)
	first := syntheticOpenCodeExecutable(t, filepath.Join(u.owner.editorTestRoot, "custom-one"))
	second := syntheticOpenCodeExecutable(t, filepath.Join(u.owner.editorTestRoot, "custom-two"))
	u.owner.openCodeVersionProbe = func(binary string) (string, error) {
		if binary == second {
			return "2.0.27", nil
		}
		return "1.19.0", nil
	}
	previous := u.owner.launcher.resolve
	u.owner.launcher.resolve = func(client, custom string) (string, error) {
		if custom != "" {
			return custom, nil
		}
		return previous(client, custom)
	}
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
	h.frame()
	u.setValue(nativeOpenCodePathField(1), "relative/opencode")
	h.frame()
	if u.openCodePreferencesReady() {
		t.Fatal("unsaved executable path allowed launch")
	}
	u.saveOpenCodeExecutable(1, false)
	if !strings.Contains(u.clientState().OpenCodePreferences.Error, "absolute path") || u.openCodePreferencesBusy() {
		t.Fatal("relative path was sent for saving")
	}
	u.setValue(nativeOpenCodePathField(1), first)
	h.frame()
	h.reveal("Save executable path", semantic.Button)
	h.click("Save executable path", semantic.Button)
	nativeTestWait(t, u, func() bool { return u.openCodePreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
	if p := u.clientState().OpenCodePreferences.Preferences; p.Executable1 != first || p.Executable2 != "" {
		t.Fatalf("first version path was not saved separately: %+v", p)
	}
	h.frame()
	h.reveal("OpenCode 2", semantic.Button)
	h.click("OpenCode 2", semantic.Button)
	nativeTestWait(t, u, func() bool { return u.openCodePreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
	u.setValue(nativeOpenCodePathField(2), second)
	h.frame()
	h.reveal("Save executable path", semantic.Button)
	h.click("Save executable path", semantic.Button)
	nativeTestWait(t, u, func() bool { return u.openCodePreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
	if p := u.clientState().OpenCodePreferences.Preferences; p.Major != 2 || p.Executable1 != first || p.Executable2 != second {
		t.Fatalf("saving second version lost first path: %+v", p)
	}
	for _, lang := range []string{"en", "es"} {
		u.setLanguage(lang)
		nativeTestWait(t, u, func() bool {
			return u.language == lang && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
		})
		u.list("page." + u.page).Position.Offset = 0
		h.frame()
		nativeGridCapture(t, h, "native-opencode-path-"+lang)
	}
	u.launchAgent("opencode")
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if recorder.count() != 1 {
		t.Fatalf("custom second version did not launch: %s", u.notice)
	}
	recorder.mu.Lock()
	plan := recorder.plans[0]
	recorder.mu.Unlock()
	if plan.Executable != second || plan.Client != "opencode2" {
		t.Fatalf("wrong executable reached the launch boundary: %+v", plan)
	}
	u.setLanguage("en")
	nativeTestWait(t, u, func() bool {
		return u.language == "en" && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
	})
	h.frame()
	h.reveal("Use automatic detection", semantic.Button)
	h.click("Use automatic detection", semantic.Button)
	nativeTestWait(t, u, func() bool { return u.openCodePreferencesReady() && !u.busy["GET"+nativeLaunchEndpoint] })
	if p := u.clientState().OpenCodePreferences.Preferences; p.Executable1 != first || p.Executable2 != "" || u.value(nativeOpenCodePathField(2)) != "" {
		t.Fatalf("automatic detection did not clear only version two: %+v", p)
	}
}

func TestNativeOpenCodeExternalPathChangeBlocksSameVersionPreparation(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "opencode", false, false)
	path := syntheticOpenCodeExecutable(t, filepath.Join(u.owner.editorTestRoot, "external-path"))
	if _, err := nativeRequest(u.owner, "POST", nativeOpenCodePreferencesEndpoint, openCodePreferences{Version: 1, Major: 1, Executable1: path}); err != nil {
		t.Fatal(err)
	}
	finished := false
	var failure error
	u.prepareClientAfter("opencode", func(err error) { failure, finished = err, true })
	nativeTestWait(t, u, func() bool { return finished })
	if failure == nil || !strings.Contains(failure.Error(), "settings changed") || recorder.count() != 0 || u.clientState().OpenCodePreferences.Preferences.Executable1 != path || u.value(nativeOpenCodePathField(1)) != path {
		t.Fatalf("same-version path change was ignored: %v, %+v", failure, u.clientState().OpenCodePreferences)
	}
}

func TestNativeOpenCodeVersionChangeDuringPreparationNeverOpens(t *testing.T) {
	u, recorder := nativeLaunchTestUI(t, "opencode", false, false)
	upstream, _ := url.Parse("http://" + u.owner.adminHost)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	entered, gate := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nativeClientEndpoint("opencode") {
			proxy.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, r)
		entered <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
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
	u.launchAgent("opencode")
	nativeTestWait(t, u, func() bool { return len(entered) == 1 })
	if _, err := nativeRequest(u.owner, "POST", nativeOpenCodePreferencesEndpoint, openCodePreferences{Version: 1, Major: 2}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	nativeTestWait(t, u, func() bool { return u.clientState().Launching == "" })
	if recorder.count() != 0 || u.openCodeClientKey() != "opencode2" || !strings.Contains(u.notice, "settings changed") {
		t.Fatalf("stale version opened or reported success: plans=%d, version=%s, notice=%q", recorder.count(), u.openCodeClientKey(), u.notice)
	}
}
