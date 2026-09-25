//go:build desktop

package main

import (
	"image"
	"strings"
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
				if node.Desc.Class == semantic.Button && text.Desc.Bounds.Min.In(node.Desc.Bounds) {
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

func TestNativeImageTransportSettingsPointerAndPersistence(t *testing.T) {
	for _, size := range []image.Point{{1180, 820}, {780, 700}, {720, 700}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page, u.language = "settings", lang
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				capture := func(name string) {
					// Finish the pointer ripple before taking a review screenshot.
					h.now = h.now.Add(time.Second)
					h.frame()
					nativeGridCapture(t, h, name+"-"+fmtSize(size)+"-"+lang)
				}
				ttl := "1h"
				choose := func(label string, mode, profile string) {
					nativeScrollImageSetting(h, label)
					h.click(label, semantic.Button)
					nativeTestWait(t, u, func() bool { return !u.busy["PUT/api/image-transport-settings"] })
					saved, err := readSettings(u.owner.dir)
					if err != nil || saved.ImageTransport != (imageTransportSettings{mode, profile, ttl}) {
						t.Fatalf("image preference did not persist: %+v %v", saved.ImageTransport, err)
					}
					h.frame()
				}
				choose("○ "+u.tr("Compress locally", "Comprimir en local"), "compress", "high")
				choose("○ "+u.tr("Balanced", "Equilibrado"), "compress", "balanced")
				choose("○ "+u.tr("Small size", "Tamaño pequeño"), "compress", "small")
				choose("○ "+u.tr("High quality", "Alta calidad"), "compress", "high")
				nativeScrollImageSetting(h, "● "+u.tr("Compress locally", "Comprimir en local"))
				capture("image-compression")
				choose("○ "+u.tr("Kilo · Experimental", "Kilo · Experimental"), "upload", "high")
				capture("experimental-image-uploads")
				choose("○ Cloudflare", "cloudflare", "high")
				assertNativeImageDescription(t, h, u.tr("Requires cloudflared", "Requiere cloudflared"))
				nativeScrollImageSetting(h, "● Cloudflare")
				capture("image-cloudflare")
				choose("○ Tailscale Funnel", "tailscale", "high")
				assertNativeImageDescription(t, h, "8443")
				capture("image-tailscale")
				choose("○ Litterbox · Experimental", "litterbox", "high")
				assertNativeImageDescription(t, h, u.tr("third-party service", "servicio externo"))
				assertNativeImageDescription(t, h, u.tr("Experimental: live availability could not be confirmed from this network. If the service rejects uploads, choose Cloudflare or local compression.", "Experimental: no se ha podido confirmar la disponibilidad real desde esta red. Si el servicio rechaza las subidas, elige Cloudflare o la compresión local."))
				capture("image-litterbox-experimental")
				for _, expiry := range []struct{ value, en, es string }{
					{"12h", "12 hours", "12 horas"}, {"24h", "24 hours", "24 horas"}, {"72h", "72 hours", "72 horas"}, {"1h", "1 hour", "1 hora"},
				} {
					ttl = expiry.value
					choose("○ "+u.tr(expiry.en, expiry.es), "litterbox", "high")
				}
				nativeScrollImageSetting(h, "● "+u.tr("1 hour", "1 hora"))
				capture("image-litterbox")
				choose("○ "+u.tr("Off", "Desactivado"), "off", "high")
				// Layout, scrolling and changing language must never opt in by themselves.
				u.language = u.tr("es", "en")
				h.frame()
				h.frame()
				if u.owner.config.ImageTransport.Mode != "off" {
					t.Fatal("redrawing or translating enabled image uploads")
				}
			})
		}
	}
}

func assertNativeImageDescription(t *testing.T, h *nativePointerHarness, contains string) {
	t.Helper()
	for _, node := range h.nodes() {
		if strings.Contains(node.Desc.Label, contains) {
			return
		}
	}
	t.Fatalf("active backend is missing its description %q", contains)
}
