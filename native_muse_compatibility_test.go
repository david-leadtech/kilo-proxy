//go:build desktop

package main

import (
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
)

func nativeMuseCompatibilityProxy(t *testing.T, u *nativeUI, intercept func(http.ResponseWriter, *http.Request) bool, release func()) {
	t.Helper()
	upstream, err := url.Parse("http://" + u.owner.adminHost)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !intercept(w, r) {
			proxy.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(func() {
		if release != nil {
			release()
		}
		server.Close()
	})
	u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
}

func nativeMuseCompatibilityCapture(t *testing.T, u *nativeUI, name string) {
	t.Helper()
	if os.Getenv("KILO_NATIVE_SCREENSHOTS") == "" {
		return
	}
	h := &nativePointerHarness{t: t, u: u, size: image.Pt(1000, 200), now: time.Now()}
	gtx := layout.Context{Ops: &h.ops, Source: h.router.Source(), Constraints: layout.Exact(h.size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: h.now}
	u.uxhRow("muse")(gtx)
	h.router.Frame(&h.ops)
	nativeGridCapture(t, h, name)
}

func TestNativeMuseCompatibilityRecoversAfterDiscoveryCompletes(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		for _, completion := range []string{"callback", "layout"} {
			t.Run(language+"/"+completion, func(t *testing.T) {
				u, recorder, binary, probes := nativeMuseKiloTestUI(t, language)
				c := u.clientState()
				c.Muse.ProfileRequested, c.Muse.ProfileLoaded = false, false
				c.Muse.Info.Prepared = false
				initial := c.LaunchInfo.Clients["muse"]
				initial.Path = ""
				c.LaunchInfo.Clients["muse"], c.LaunchChecked = initial, false
				profileGate, discoveryGate := make(chan struct{}), make(chan struct{})
				profileEntered, discoveryEntered := make(chan struct{}, 1), make(chan struct{}, 1)
				var profileRelease, discoveryRelease sync.Once
				releaseProfile := func() { profileRelease.Do(func() { close(profileGate) }) }
				releaseDiscovery := func() { discoveryRelease.Do(func() { close(discoveryGate) }) }
				var requests atomic.Int32
				nativeMuseCompatibilityProxy(t, u, func(w http.ResponseWriter, r *http.Request) bool {
					var gate <-chan struct{}
					switch {
					case r.Method == http.MethodGet && r.URL.Path == nativeLaunchEndpoint:
						discoveryEntered <- struct{}{}
						gate = discoveryGate
					case r.Method == http.MethodGet && r.URL.Path == nativeMuseEndpoint:
						if requests.Add(1) == 1 {
							profileEntered <- struct{}{}
							gate = profileGate
						}
					}
					if gate != nil {
						select {
						case <-gate:
						case <-r.Context().Done():
							return true
						}
					}
					return false
				}, func() { releaseProfile(); releaseDiscovery() })
				u.detectLaunchers()
				if state := u.uxhLaunchState("muse"); state.canOpen {
					t.Fatal("Muse opened before installation discovery completed")
				}
				nativeTestWait(t, u, func() bool {
					select {
					case <-discoveryEntered:
						return true
					default:
						return false
					}
				})
				nativeTestWait(t, u, func() bool {
					select {
					case <-profileEntered:
						return true
					default:
						return false
					}
				})
				releaseDiscovery()
				nativeTestWait(t, u, func() bool {
					return c.LaunchChecked && c.LaunchInfo.Clients["muse"].Path == binary && !u.busy["GET"+nativeLaunchEndpoint]
				})
				if !u.busy["GET"+nativeMuseEndpoint] {
					t.Fatal("profile request was not held until after discovery changed the path")
				}
				if completion == "layout" {
					if state := u.uxhLaunchState("muse"); state.canOpen {
						t.Fatal("Muse opened with the obsolete compatibility request in flight")
					}
				}
				releaseProfile()
				nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeMuseEndpoint] })
				if c.Muse.ProfileRequested || c.Muse.ProfileLoaded {
					nativeMuseCompatibilityCapture(t, u, "muse-compatibility-stuck-"+language+"-"+completion)
					t.Fatalf("obsolete compatibility response left the request latched: requested=%v loaded=%v", c.Muse.ProfileRequested, c.Muse.ProfileLoaded)
				}
				u.uxhLaunchState("muse")
				nativeTestWait(t, u, func() bool { return c.Muse.ProfileLoaded && !u.busy["GET"+nativeMuseEndpoint] })
				if state := u.uxhLaunchState("muse"); !state.canOpen || state.reason != "" {
					t.Fatalf("Muse did not recover after checking the discovered executable: %+v", state)
				}
				nativeMuseCompatibilityCapture(t, u, "muse-compatibility-recovered-"+language+"-"+completion)
				if requests.Load() != 2 || probes.Load() != 0 || recorder.count() != 0 {
					t.Fatalf("compatibility recovery retried, probed or launched unexpectedly: requests=%d probes=%d launches=%d", requests.Load(), probes.Load(), recorder.count())
				}
			})
		}
	}
}

func TestNativeMuseCompatibilityPreservesNewerGeneration(t *testing.T) {
	u, _, binary, _ := nativeMuseKiloTestUI(t, "en")
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	nativeMuseCompatibilityProxy(t, u, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == nativeMuseEndpoint {
			entered <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return true
			}
		}
		return false
	}, release)
	u.refreshMuseProfile()
	nativeTestWait(t, u, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	c := u.clientState()
	c.Muse.ProfileGeneration++
	c.Muse.ProfileRequested, c.Muse.ProfileLoaded, c.Muse.Info.Prepared = true, true, true
	c.Muse.Error = "newer compatibility status"
	newer := c.LaunchInfo.Clients["muse"]
	newer.Path = binary + "-newer-generation"
	c.LaunchInfo.Clients["muse"] = newer
	c.Muse.Info.ResolvedExecutable = newer.Path
	want := c.Muse
	release()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeMuseEndpoint] })
	if !reflect.DeepEqual(c.Muse, want) {
		t.Fatalf("obsolete response changed newer Muse state: got=%+v want=%+v", c.Muse, want)
	}
}

func TestNativeMuseCompatibilityResolutionErrorIsStableAndRecoversOnPathChange(t *testing.T) {
	u, recorder, binary, probes := nativeMuseKiloTestUI(t, "en")
	originalResolve := u.owner.launcher.resolve
	var failed atomic.Bool
	failed.Store(true)
	var selected atomic.Value
	selected.Store(binary)
	u.owner.launcher.resolve = func(client, custom string) (string, error) {
		if client != "muse" {
			return originalResolve(client, custom)
		}
		if failed.Load() {
			return "", errors.New("synthetic Muse resolution failed")
		}
		return selected.Load().(string), nil
	}
	var requests atomic.Int32
	nativeMuseCompatibilityProxy(t, u, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == nativeMuseEndpoint {
			requests.Add(1)
		}
		return false
	}, nil)
	u.refreshMuseProfile()
	nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeMuseEndpoint] })
	generation := u.clientState().Muse.ProfileGeneration
	for i := 0; i < 32; i++ {
		state := u.uxhLaunchState("muse")
		if state.canOpen || !strings.Contains(state.reason, "synthetic Muse resolution failed") || u.busy["GET"+nativeMuseEndpoint] || u.clientState().Muse.ProfileGeneration != generation {
			t.Fatalf("resolution error was hidden or caused another compatibility request on render %d: %+v", i, state)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("unchanged failed executable was checked %d times", requests.Load())
	}
	replacement := cliPreferencesTestExecutable(t, "replacement Muse Code")
	selected.Store(replacement)
	failed.Store(false)
	u.detectLaunchers()
	nativeTestWait(t, u, func() bool {
		return !u.busy["GET"+nativeLaunchEndpoint] && u.clientState().LaunchInfo.Clients["muse"].Path == replacement
	})
	u.uxhLaunchState("muse")
	nativeTestWait(t, u, func() bool { return u.clientState().Muse.ProfileLoaded && !u.busy["GET"+nativeMuseEndpoint] })
	if state := u.uxhLaunchState("muse"); !state.canOpen || state.reason != "" {
		t.Fatalf("changed executable did not recover from the old resolution error: %+v", state)
	}
	if requests.Load() != 2 || probes.Load() != 0 || recorder.count() != 0 {
		t.Fatalf("resolution recovery retried, probed or launched unexpectedly: requests=%d probes=%d launches=%d", requests.Load(), probes.Load(), recorder.count())
	}
}

func TestNativeMuseCompatibilityRequestErrorDoesNotRetryOnRender(t *testing.T) {
	for _, response := range []string{"http-error", "invalid-profile"} {
		t.Run(response, func(t *testing.T) {
			u, recorder, _, probes := nativeMuseKiloTestUI(t, "en")
			var requests atomic.Int32
			nativeMuseCompatibilityProxy(t, u, func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodGet || r.URL.Path != nativeMuseEndpoint {
					return false
				}
				requests.Add(1)
				if response == "http-error" {
					jsonError(w, http.StatusServiceUnavailable, "synthetic Muse profile unavailable")
				} else {
					jsonResponse(w, http.StatusOK, map[string]any{"preferences": map[string]any{"version": 99, "connection": "kilo"}})
				}
				return true
			}, nil)
			u.refreshMuseProfile()
			nativeTestWait(t, u, func() bool { return !u.busy["GET"+nativeMuseEndpoint] })
			c := u.clientState()
			if c.Muse.Error == "" {
				t.Fatal("compatibility request failure was not surfaced")
			}
			wantError, generation := c.Muse.Error, c.Muse.ProfileGeneration
			for i := 0; i < 32; i++ {
				state := u.uxhLaunchState("muse")
				if state.canOpen || state.reason != wantError || u.busy["GET"+nativeMuseEndpoint] || c.Muse.ProfileGeneration != generation {
					t.Fatalf("request error was hidden or retried on render %d: %+v", i, state)
				}
			}
			if requests.Load() != 1 || probes.Load() != 0 || recorder.count() != 0 {
				t.Fatalf("request error retried, probed or launched unexpectedly: requests=%d probes=%d launches=%d", requests.Load(), probes.Load(), recorder.count())
			}
		})
	}
}
