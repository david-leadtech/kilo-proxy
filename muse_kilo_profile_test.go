package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func museKiloTestApp(t *testing.T) (*app, string, modelLibrary) {
	t.Helper()
	a := launchTestApp(t)
	a.config.Port = 18877 // Profile-only fixture: never binds this address.
	a.config.MuseCode = musePreferences{Version: 1, Connection: "kilo"}
	binary := filepath.Join(t.TempDir(), "selected Muse custom")
	if err := os.WriteFile(binary, []byte("synthetic Muse; test hook prevents execution"), 0700); err != nil {
		t.Fatal(err)
	}
	a.museVersionProbe = func(path string) (string, error) {
		if path != binary {
			t.Fatal("version probe substituted the selected binary", path)
		}
		return "1.4.6-R6000.1", nil
	}
	a.launcher.resolve = func(client, custom string) (string, error) { return binary, nil }
	library := cloneModelLibrary(museTestLibrary())
	if _, err := a.modelLibrary.save(library, 0, false); err != nil {
		t.Fatal(err)
	}
	return a, binary, library
}

func prepareMuseTestProfile(t *testing.T, a *app, binary string, library modelLibrary) museProfileInfo {
	t.Helper()
	body, _ := json.Marshal(museProfileRequest{Preferences: a.config.MuseCode, Library: &library, MuseExecutable: binary})
	w := adminRequest(a, "clients/muse", string(body))
	var info museProfileInfo
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &info) != nil || !info.Prepared || !info.KiloSupported || info.RequiresPreparation || !reflect.DeepEqual(info.Library, library) || info.ResolvedExecutable != binary {
		t.Fatal(w.Code, w.Body.String())
	}
	return info
}

func TestMuseKiloProfileIsPrivateAndRejectsStaleLibraryBinaryAndCredentials(t *testing.T) {
	t.Setenv("TBH_CREDENTIAL_BACKEND", "keychain")
	a, binary, library := museKiloTestApp(t)
	info := prepareMuseTestProfile(t, a, binary, library)
	if a.proxyListener != nil {
		t.Fatal("preparing a profile started inference")
	}
	for _, name := range []string{"settings.json", "auth.json"} {
		path := filepath.Join(info.ProfileDir, ".config", "muse", name)
		st, err := os.Stat(path)
		if err != nil || runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
			t.Fatal("unsafe private file", path, err)
		}
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), a.config.LocalKey) || strings.Contains(string(data), a.apiKey) {
			t.Fatal("upstream/root credential in Muse profile")
		}
	}
	plan, err := a.planClientLaunch(clientLaunchRequest{Client: "muse", Directory: a.launcher.home, MuseExecutable: binary, MuseConnection: "kilo"}, a.launchRuntime())
	if err != nil || plan.skipProxy || !reflect.DeepEqual(plan.Env, museKiloEnvironment(info.ProfileDir)) {
		t.Fatal("Kilo Muse bypassed proxy or used native HOME", plan, err)
	}
	if !helperContains(clientChildEnvironment(os.Environ(), plan.Env, plan.Unset, runtime.GOOS), "TBH_CREDENTIAL_BACKEND=file") {
		t.Fatal("private Muse inherited the native credential backend")
	}
	infoRuntime := terminalRuntime{MuseProfile: info.ProfileDir}
	plan.Executable = ""
	if err := validateTerminalProfile(plan, infoRuntime); err != nil {
		t.Fatal(err)
	}
	plan.Env["TBH_CREDENTIAL_BACKEND"] = "keychain"
	if validateTerminalProfile(plan, infoRuntime) == nil {
		t.Fatal("controller selected a native credential backend for private Muse")
	}
	plan.Env["TBH_CREDENTIAL_BACKEND"] = "file"
	plan.Env["META_API_KEY"] = "untrusted-controller-key"
	if validateTerminalProfile(plan, infoRuntime) == nil {
		t.Fatal("controller injected arbitrary Muse credentials")
	}
	for _, mutate := range []func(*app, string, *modelLibrary){
		func(a *app, binary string, library *modelLibrary) { library.Models[0].ReasoningEffort = "high" },
		func(a *app, binary string, library *modelLibrary) { a.config.LocalKey += "-rotated" },
		func(a *app, binary string, library *modelLibrary) {
			if err := os.WriteFile(binary, []byte("changed selected Muse"), 0700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		fresh, executable, selection := museKiloTestApp(t)
		prepareMuseTestProfile(t, fresh, executable, selection)
		mutate(fresh, executable, &selection)
		if fresh.museTerminalPrepared(executable, selection) == nil {
			t.Fatal("stale private profile accepted")
		}
	}
}

func TestMuseKiloProfileRejectsNativePathAliasesBeforeWriting(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(fmt.Sprint("alias=", alias), func(t *testing.T) {
			a, binary, library := museKiloTestApp(t)
			home := museTerminalHome(a.dir)
			normalConfig := filepath.Join(home, ".config")
			if alias {
				link := filepath.Join(t.TempDir(), "normal-config")
				if err := os.Symlink(normalConfig, link); err != nil {
					t.Skip("directory aliases are unavailable on this runner", err)
				}
				normalConfig = link
			}
			t.Setenv("XDG_CONFIG_HOME", normalConfig)
			body, _ := json.Marshal(museProfileRequest{Preferences: a.config.MuseCode, Library: &library, MuseExecutable: binary})
			w := adminRequest(a, "clients/muse", string(body))
			if w.Code != http.StatusConflict {
				t.Fatal("overlapping normal account accepted", w.Code, w.Body.String())
			}
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatal("overlap wrote private/normal files", err)
			}
		})
	}
}

func TestMuseKiloProxyRoutesExactModelsWithoutLeakingScopedCredentials(t *testing.T) {
	a, binary, library := museKiloTestApp(t)
	prepareMuseTestProfile(t, a, binary, library)
	calls := 0
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer synthetic-upstream" || r.Header.Get("Cookie") != "" || r.Header.Get("X-KiloCode-OrganizationId") != "synthetic-org" {
			t.Error("invalid or leaking upstream request", r.URL.Path, r.Header)
		}
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_synthetic","object":"response","status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	a.upstream, _ = url.Parse(upstream.URL)
	host := "127.0.0.1:18877"
	handler := a.inferenceHandler("synthetic-upstream", "synthetic-org", a.config.LocalKey, host)
	request := func(path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+host+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Cookie", "personal=must-not-leak")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	token := museProxyToken(a.config.LocalKey, "terminal")
	w := request("/muse/v1/responses", token, `{"model":"vendor/nested/reasoning","reasoning":{"effort":"minimal"},"input":"synthetic local only","stream":false}`)
	if w.Code != http.StatusOK || calls != 1 || !strings.Contains(string(upstreamBody), `"effort":"low"`) || strings.Contains(string(upstreamBody), token) {
		t.Fatal(w.Code, w.Body.String(), string(upstreamBody), calls)
	}
	for _, path := range []string{"/v1/responses", "/t3-code/muse/v1/responses", "/muse/v1/responses?other=1", "/muse/v1/messages"} {
		if w := request(path, token, `{"model":"vendor/nested/reasoning"}`); w.Code != http.StatusNotFound {
			t.Fatal("scoped credential escaped its route", path, w.Code)
		}
	}
	if w := request("/muse/v1/responses", token, `{"model":"other/unselected"}`); w.Code != http.StatusBadRequest || calls != 1 {
		t.Fatal("unselected model forwarded", w.Code, calls)
	}
	a.config.MuseCode.Connection = "native"
	if w := request("/muse/v1/responses", token, `{"model":"vendor/nested/reasoning"}`); w.Code != http.StatusConflict || calls != 1 {
		t.Fatal("disabled Kilo connection still forwarded", w.Code, calls)
	}
}

func TestMuseKiloTerminalKeepsPromptsAndProtectsConnectionOptions(t *testing.T) {
	for _, args := range [][]string{{"--base-url", "https://other.invalid"}, {"--provider=meta"}, {"-c", "endpoint_transport={}"}, {"--config=model_catalog=[]"}, {"login"}, {"auth"}} {
		if validateMuseKiloArguments(args) == nil {
			t.Fatal("unmanaged connection option accepted", args)
		}
	}
	for _, args := range [][]string{{"--model", "vendor/nested/reasoning", "--reasoning-effort", "low"}, {"resume", "--last"}, {"exec", "literal$(not executed)"}, {"--", "login", "--base-url"}} {
		if err := validateMuseKiloArguments(args); err != nil {
			t.Fatal("native prompt/session options blocked", args, err)
		}
	}
}

func TestMuseKiloTerminalRejectsShellAccountAliasBeforeRequestingPreparation(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	binary := cliPreferencesTestExecutable(t, "Muse-not-started")
	cfg, err := readSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MuseCodeExecutable = binary
	cfg.MuseCode = musePreferences{Version: 1, Connection: "kilo"}
	if err := writeSettings(dir, cfg); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected preparation", 500)
	}))
	defer controller.Close()
	u, _ := url.Parse(controller.URL)
	profile := museTerminalHome(dir)
	info := terminalRuntime{Version: 1, Host: u.Host, Token: strings.Repeat("a", 64), CodexProfile: filepath.Join(home, "codex"), ClaudeProfile: filepath.Join(home, "claude"), MuseProfile: profile}
	data, _ := json.Marshal(info)
	if err := atomicCatalogFile(filepath.Join(dir, terminalRuntimeFile), data); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, self, "-test.run=^TestTerminalAgentProcessHelper$", "--", "muse", "--config-dir", dir, "--", "--help")
	command.Dir = home
	command.Env = clientChildEnvironment(os.Environ(), map[string]string{"KILO_TERMINAL_PROCESS_TEST": "1", "HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(profile, ".config")}, nil, "linux")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err == nil || requests.Load() != 0 || !strings.Contains(stderr.String(), "separate from the normal Meta") {
		t.Fatal("terminal alias reached preparation or execution", err, requests.Load(), stderr.String())
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatal("terminal guard wrote the overlapping profile", err)
	}
}

func TestMuseProfileRejectsDuplicateConnectionAndSupportsFullLibrary(t *testing.T) {
	a, binary, library := museKiloTestApp(t)
	for _, body := range []string{
		`{"preferences":{"version":1,"connection":"kilo","connection":"native"},"museExecutable":"ignored"}`,
		`{"preferences":{"version":1,"connection":"kilo"},"library":null,"museExecutable":"ignored"}`,
		`{"preferences":{"version":1,"connection":"kilo"},"museExecutable":"ignored","environment":{"META_API_KEY":"forged"}}`,
	} {
		if w := adminRequest(a, "clients/muse", body); w.Code != http.StatusBadRequest {
			t.Fatal("ambiguous profile request accepted", w.Code, w.Body.String())
		}
	}
	for i := len(library.Models); i < 50; i++ {
		library.Models = append(library.Models, modelLibraryItem{ID: "vendor/" + strings.Repeat("long-model-id-", 13) + fmt.Sprintf("%02d", i), DisplayName: strings.Repeat("name", 20), ReasoningCustom: true, ReasoningLevels: append([]string{}, nativeReasoningLevels...), ReasoningEffort: "low", ContextPreset: "custom", ContextWindow: 32768, MaxOutputTokens: 2048})
	}
	if _, err := a.modelLibrary.save(library, a.modelLibrary.snapshot().Revision, false); err != nil {
		t.Fatal(err)
	}
	library = a.modelLibrary.snapshot().Library
	request, _ := json.Marshal(museProfileRequest{Preferences: a.config.MuseCode, Library: &library, MuseExecutable: binary})
	if len(request) <= 16<<10 {
		t.Fatal("fixture did not exercise full-library request budget", len(request))
	}
	prepareMuseTestProfile(t, a, binary, library)
}

func TestMusePreparationRejectsModelsChangedDuringVersionProbe(t *testing.T) {
	for _, endpoint := range []string{"clients/muse", "terminal/prepare"} {
		t.Run(endpoint, func(t *testing.T) {
			a, binary, library := museKiloTestApp(t)
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			a.museVersionProbe = func(string) (string, error) { close(entered); <-release; return "1.4.6-R6000.1", nil }
			var input any = museProfileRequest{Preferences: a.config.MuseCode, Library: &library, MuseExecutable: binary}
			if endpoint == "terminal/prepare" {
				input = terminalPrepareRequest{Client: "muse", Directory: a.launcher.home, MuseExecutable: binary, MuseConnection: "kilo"}
			}
			data, _ := json.Marshal(input)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- adminRequest(a, endpoint, string(data)) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("version probe was not reached")
			}
			changed := cloneModelLibrary(library)
			changed.Models[0].ReasoningEffort = "high"
			if _, err := a.modelLibrary.save(changed, a.modelLibrary.snapshot().Revision, false); err != nil {
				t.Fatal(err)
			}
			close(release)
			var response *httptest.ResponseRecorder
			select {
			case response = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("preparation did not finish")
			}
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), errModelLibraryConflict.Error()) || a.proxyListener != nil {
				t.Fatal("stale preparation was activated", response.Code, response.Body.String())
			}
			if _, err := os.Stat(museTerminalHome(a.dir)); !os.IsNotExist(err) {
				t.Fatal("stale preparation wrote a profile", err)
			}
		})
	}
}

func TestMuseExplicitPreparationRepairsOnlyItsPrivateFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions; Windows uses inherited directory ACLs.")
	} // Windows uses inherited directory ACLs, not POSIX mode bits.
	a, binary, library := museKiloTestApp(t)
	prepareMuseTestProfile(t, a, binary, library)
	_, auth := museKiloProfilePaths(museTerminalHome(a.dir))
	if err := os.Chmod(auth, 0644); err != nil {
		t.Fatal(err)
	}
	if a.museTerminalPrepared(binary, library) == nil {
		t.Fatal("broadly readable auth was accepted")
	}
	prepareMuseTestProfile(t, a, binary, library)
	st, err := os.Stat(auth)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("explicit preparation did not repair its private auth", err)
	}
}
