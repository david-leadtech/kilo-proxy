//go:build desktop

package main

import (
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func nativeScrollImageSetting(h *nativePointerHarness, label string) {
	target := 260
	if h.size.X < 900 {
		target = 330
	}
	for attempt := 0; attempt < 10; attempt++ {
		y := 1000
		for _, text := range h.nodes() {
			if text.Desc.Label != label {
				continue
			}
			for _, node := range h.nodes() {
				if (node.Desc.Class == semantic.Button || node.Desc.Class == semantic.RadioButton) && text.Desc.Bounds.Min.In(node.Desc.Bounds) {
					y = node.Desc.Bounds.Min.Y
				}
			}
		}
		if y >= target-20 && y <= target+20 {
			return
		}
		nativeMenuWheel(h, image.Pt(h.size.X-100, h.size.Y-100), float32(y-target))
	}
}

func TestNativeImageTransportSettingsWaitForLauncherDiscovery(t *testing.T) {
	for _, lang := range []string{"en", "es"} {
		t.Run(lang, func(t *testing.T) {
			u := nativeTestUI(t)
			codexPath, claudePath := filepath.Join(t.TempDir(), "codex"), filepath.Join(t.TempDir(), "claude")
			u.owner.launcher.resolve = func(client, _ string) (string, error) {
				switch client {
				case "codex-cli":
					return codexPath, nil
				case "claude":
					return claudePath, nil
				}
				return "", errors.New("not installed in this fixture")
			}
			u.owner.mu.Lock()
			u.owner.imageDependencyLookup = func(string) string { return "" }
			u.owner.mu.Unlock()
			u.setLanguage(lang)
			nativeTestWait(t, u, func() bool { return u.languageTarget == "" && !u.busy["GET/api/state"] })
			u.refreshState()
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] && u.imageDependencyNoticeVisible() })
			u.showImageSettings()
			h := &nativePointerHarness{t: t, u: u, size: image.Pt(780, 700), now: time.Now()}
			h.frame()
			// Direct Settings does not start launcher discovery. The shared wait
			// must not require or invent an otherwise unnecessary request.
			nativeSettingsPreferencesReady(h)
			if u.clientState().LaunchDetectStarted {
				t.Fatal("opening Settings started unrelated launcher discovery")
			}
			upstream, err := url.Parse("http://" + u.owner.adminHost)
			if err != nil {
				t.Fatal(err)
			}
			entered, gate := make(chan struct{}), make(chan struct{})
			var release sync.Once
			var saves atomic.Int32
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == nativeLaunchEndpoint {
					close(entered)
					select {
					case <-gate:
					case <-r.Context().Done():
						return
					}
				}
				if r.Method == http.MethodPut && r.URL.Path == "/api/image-transport-settings" {
					saves.Add(1)
				}
				proxy.ServeHTTP(w, r)
			}))
			u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
			t.Cleanup(server.Close)
			t.Cleanup(func() { release.Do(func() { close(gate) }) })
			u.detectLaunchers()
			select {
			case <-entered:
			case <-time.After(8 * time.Second):
				t.Fatal("launcher discovery did not reach the held response")
			}
			if !u.busy["GET"+nativeLaunchEndpoint] || u.clientState().LaunchChecked {
				t.Fatal("held discovery was marked ready")
			}
			label := u.tr("Off", "Desactivado")
			nativeScrollImageSetting(h, label)
			pendingBounds := h.target(label, semantic.RadioButton).Desc.Bounds
			// The shared wait drains this release, then must wait for the real
			// discovery callback before laying out and returning coordinates.
			u.enqueue(func() { release.Do(func() { close(gate) }) })
			nativeSettingsPreferencesReady(h)
			if !u.clientState().LaunchChecked || u.busy["GET"+nativeLaunchEndpoint] {
				t.Fatal("Settings coordinates returned before launcher discovery finished")
			}
			if u.clientState().LaunchInfo.Clients["codex-cli"].Path != codexPath || u.clientState().LaunchInfo.Clients["claude"].Path != claudePath {
				t.Fatal("confirmed discovery did not retain both fixture CLI paths")
			}
			confirmedBounds := h.target(label, semantic.RadioButton).Desc.Bounds
			pendingCenter := image.Pt(pendingBounds.Min.X+pendingBounds.Dx()/2, pendingBounds.Min.Y+pendingBounds.Dy()/2)
			if pendingCenter.In(confirmedBounds) {
				t.Fatal("resolved CLI paths did not move Images away from its old pointer target")
			}
			t.Logf("%s moved after launcher discovery: %v -> %v", label, pendingBounds, confirmedBounds)
			nativeScrollImageSetting(h, label)
			h.click(label, semantic.RadioButton)
			nativeTestWait(t, u, func() bool {
				u.owner.mu.Lock()
				mode := u.owner.config.ImageTransport.Mode
				u.owner.mu.Unlock()
				return mode == "off" && !u.busy["PUT/api/image-transport-settings"]
			})
			saved, err := readSettings(u.owner.dir)
			if err != nil || saved.ImageTransport.Mode != "off" || saves.Load() != 1 {
				t.Fatalf("confirmed Off click did not persist exactly once: %+v, saves=%d, err=%v", saved.ImageTransport, saves.Load(), err)
			}
			u.refreshState()
			nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
			h.frame()
			if u.imageDependencyNoticeVisible() || nativeImageDependencyText(h, u.tr("Install cloudflared for large images", "Instala cloudflared para imágenes grandes"), false) {
				t.Fatal("confirmed Off click retained the cloudflared warning")
			}
		})
	}
}

func TestNativeImageTransportSettingsWaitForPrecedingPreferences(t *testing.T) {
	for _, action := range []string{"change", "off"} {
		for _, lang := range []string{"en", "es"} {
			t.Run(action+"/"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page, u.settingsTab = "settings", "images"
				if action == "off" {
					u.showImageSettings()
				}
				u.setLanguage(lang)
				nativeTestWait(t, u, func() bool {
					return u.language == lang && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
				})
				if action == "off" {
					u.owner.mu.Lock()
					u.owner.imageDependencyLookup = func(string) string { return "" }
					u.owner.mu.Unlock()
					u.refreshState()
					nativeTestWait(t, u, func() bool {
						return !u.busy["GET/api/state"] && u.imageDependency().Required && !u.imageDependency().Installed
					})
				}
				upstream, err := url.Parse("http://" + u.owner.adminHost)
				if err != nil {
					t.Fatal(err)
				}
				gate := make(chan struct{})
				var release sync.Once
				entered := make(chan string, 2)
				var t3Requests atomic.Int32
				proxy := httputil.NewSingleHostReverseProxy(upstream)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && r.URL.Path == nativeT3CodePreferencesEndpoint {
						t3Requests.Add(1)
					}
					if r.Method == http.MethodGet && r.URL.Path == nativeCLIPreferencesEndpoint {
						entered <- r.URL.Path
						select {
						case <-gate:
						case <-r.Context().Done():
							return
						}
					}
					proxy.ServeHTTP(w, r)
				}))
				u.owner.adminHost = strings.TrimPrefix(server.URL, "http://")
				t.Cleanup(server.Close)
				t.Cleanup(func() { release.Do(func() { close(gate) }) })
				h := &nativePointerHarness{t: t, u: u, size: image.Pt(780, 700), now: time.Now()}
				h.frame()
				seen := make(map[string]bool)
				nativeTestWait(t, u, func() bool {
					select {
					case path := <-entered:
						seen[path] = true
					default:
					}
					return seen[nativeCLIPreferencesEndpoint]
				})
				if u.cliPreferencesReady() {
					t.Fatal("held preferences were marked loaded")
				}
				if t3Requests.Load() != 0 || u.agentsState().T3Code.PreferencesState.Requested {
					t.Fatal("global Settings loaded preferences belonging to the T3 harness")
				}
				label, class := u.tr("Change", "Cambiar"), semantic.Button
				if action == "off" {
					label, class = u.tr("Off", "Desactivado"), semantic.RadioButton
				}
				nativeScrollImageSetting(h, label)
				pendingBounds := h.target(label, class).Desc.Bounds
				if action == "off" && !nativeImageDependencyText(h, u.tr("Install cloudflared for large images", "Instala cloudflared para imágenes grandes"), false) {
					t.Fatal("pending Off fixture did not show the missing cloudflared warning")
				}
				release.Do(func() { close(gate) })
				// This wait drains responses without laying out. The router still
				// contains coordinates from the pending Settings blocks.
				nativeTestWait(t, u, func() bool {
					return u.cliPreferencesReady()
				})
				nativeSettingsPreferencesReady(h)
				confirmedBounds := h.target(label, class).Desc.Bounds
				pendingCenter := image.Pt(pendingBounds.Min.X+pendingBounds.Dx()/2, pendingBounds.Min.Y+pendingBounds.Dy()/2)
				if pendingCenter.In(confirmedBounds) {
					t.Fatal("held Settings responses did not move the button away from its previous pointer target")
				}
				t.Logf("%s moved after preferences loaded: %v -> %v", label, pendingBounds, confirmedBounds)
				nativeScrollImageSetting(h, label)
				h.click(label, class)
				if action == "change" {
					if !u.expanded["images.change"] {
						t.Fatal("Change did not open after preceding Settings responses loaded")
					}
				} else {
					nativeTestWait(t, u, func() bool {
						u.owner.mu.Lock()
						mode := u.owner.config.ImageTransport.Mode
						u.owner.mu.Unlock()
						return mode == "off" && !u.busy["PUT/api/image-transport-settings"]
					})
					saved, err := readSettings(u.owner.dir)
					if err != nil || saved.ImageTransport.Mode != "off" {
						t.Fatalf("Off pointer did not persist after Settings loaded: %+v %v", saved.ImageTransport, err)
					}
					u.refreshState()
					nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
					h.frame()
					if u.imageDependencyNoticeVisible() || nativeImageDependencyText(h, u.tr("Install cloudflared for large images", "Instala cloudflared para imágenes grandes"), false) {
						t.Fatal("confirmed Off selection retained the cloudflared warning")
					}
				}
			})
		}
	}
}
func TestNativeImageTransportSettingsPointerAndPersistence(t *testing.T) {
	for _, size := range []image.Point{{1180, 820}, {780, 700}, {720, 700}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page = "settings"
				u.settingsTab = "images"
				// Persist the language through the real API. Assigning u.language
				// alone lets the periodic state refresh restore the fixture's English.
				u.setLanguage(lang)
				nativeTestWait(t, u, func() bool {
					return u.language == lang && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
				})
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				nativeSettingsPreferencesReady(h)
				// The method list opens from the row's Change link.
				change := u.tr("Change", "Cambiar")
				nativeScrollImageSetting(h, change)
				h.click(change, semantic.Button)
				if !u.expanded["images.change"] {
					t.Fatal("Change did not open the image methods")
				}
				capture := func(name string) {
					// Finish the pointer ripple before taking a review screenshot.
					h.now = h.now.Add(time.Second)
					h.frame()
					nativeGridCapture(t, h, name+"-"+fmtSize(size)+"-"+lang)
				}
				ttl := "1h"
				class := semantic.Button
				var choose func(label string, mode, profile string)
				method := func(label string, mode, profile string) {
					class = semantic.RadioButton
					defer func() { class = semantic.Button }()
					choose(label, mode, profile)
				}
				choose = func(label string, mode, profile string) {
					// Exercise the same refresh that the background poll performs,
					// without depending on the runner taking over one second.
					u.refreshState()
					nativeTestWait(t, u, func() bool { return !u.busy["GET/api/state"] })
					h.frame()
					if u.language != lang {
						t.Fatalf("state refresh changed the test language to %q, want %q", u.language, lang)
					}
					nativeScrollImageSetting(h, label)
					h.click(label, class)
					nativeTestWait(t, u, func() bool { return !u.busy["PUT/api/image-transport-settings"] })
					saved, err := readSettings(u.owner.dir)
					if err != nil || saved.ImageTransport != (imageTransportSettings{mode, profile, ttl}) {
						t.Fatalf("image preference did not persist: %+v %v", saved.ImageTransport, err)
					}
					if saved.Language != lang {
						t.Fatalf("saving image preferences changed the persisted language to %q, want %q", saved.Language, lang)
					}
					h.frame()
					if !h.selected(label, class) {
						t.Fatalf("image option %q is not semantically selected", label)
					}
				}
				method(u.tr("Compress here", "Comprimir aquí"), "compress", "high")
				choose(u.tr("Balanced", "Equilibrado"), "compress", "balanced")
				choose(u.tr("Small size", "Tamaño pequeño"), "compress", "small")
				choose(u.tr("High quality", "Alta calidad"), "compress", "high")
				nativeScrollImageSetting(h, u.tr("Compress here", "Comprimir aquí"))
				capture("image-compression")
				method("Kilo · experimental", "upload", "high")
				capture("experimental-image-uploads")
				method("Cloudflare", "cloudflare", "high")
				nativeScrollImageSetting(h, "Cloudflare")
				capture("image-cloudflare")
				method("Tailscale Funnel", "tailscale", "high")
				capture("image-tailscale")
				method("Litterbox · experimental", "litterbox", "high")
				capture("image-litterbox-experimental")
				for _, expiry := range []struct{ value, en, es string }{
					{"12h", "12 hours", "12 horas"}, {"24h", "24 hours", "24 horas"}, {"72h", "72 hours", "72 horas"}, {"1h", "1 hour", "1 hora"},
				} {
					ttl = expiry.value
					choose(u.tr(expiry.en, expiry.es), "litterbox", "high")
				}
				nativeScrollImageSetting(h, u.tr("1 hour", "1 hora"))
				capture("image-litterbox")
				method(u.tr("Off", "Desactivado"), "off", "high")
				// Layout, scrolling and changing language must never opt in by themselves.
				nextLanguage := u.tr("es", "en")
				// Language lives in the Preferences block; jumping there keeps image settings intact.
				u.settingsTab = "preferences"
				h.frame()
				nativeScrollImageSetting(h, u.tr("Español", "English"))
				h.click(u.tr("Español", "English"), semantic.Button)
				nativeTestWait(t, u, func() bool {
					return u.language == nextLanguage && u.languageTarget == "" && !u.busy["POST/api/language"] && !u.busy["GET/api/state"]
				})
				h.frame()
				saved, err := readSettings(u.owner.dir)
				if err != nil || saved.Language != nextLanguage || saved.ImageTransport != (imageTransportSettings{"off", "high", ttl}) {
					t.Fatalf("changing language lost its preference or changed image settings: language=%q imageTransport=%+v error=%v", saved.Language, saved.ImageTransport, err)
				}
			})
		}
	}
}
