//go:build desktop

package main

import (
	"gioui.org/io/semantic"
	"image"
	"testing"
	"time"
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
	for _, size := range []image.Point{{1180, 820}, {780, 700}} {
		for _, lang := range []string{"en", "es"} {
			t.Run(fmtSize(size)+"-"+lang, func(t *testing.T) {
				u := nativeTestUI(t)
				u.page, u.language = "settings", lang
				h := &nativePointerHarness{t: t, u: u, size: size, now: time.Now()}
				h.frame()
				choose := func(label string, mode, profile string) {
					nativeScrollImageSetting(h, label)
					h.click(label, semantic.Button)
					nativeTestWait(t, u, func() bool { return !u.busy["PUT/api/image-transport-settings"] })
					saved, err := readSettings(u.owner.dir)
					if err != nil || saved.ImageTransport != (imageTransportSettings{mode, profile}) {
						t.Fatalf("image preference did not persist: %+v %v", saved.ImageTransport, err)
					}
					h.frame()
				}
				choose("○ "+u.tr("Compress locally", "Comprimir en local"), "compress", "high")
				choose("○ "+u.tr("Balanced", "Equilibrado"), "compress", "balanced")
				choose("○ "+u.tr("Small size", "Tamaño pequeño"), "compress", "small")
				choose("○ "+u.tr("High quality", "Alta calidad"), "compress", "high")
				nativeScrollImageSetting(h, "● "+u.tr("Compress locally", "Comprimir en local"))
				nativeGridCapture(t, h, "image-compression-"+fmtSize(size)+"-"+lang)
				choose("○ "+u.tr("Upload to Kilo · Experimental", "Subir a Kilo · Experimental"), "upload", "high")
				nativeGridCapture(t, h, "experimental-image-uploads-"+fmtSize(size)+"-"+lang)
				choose("○ "+u.tr("Off", "Desactivado"), "off", "high")
			})
		}
	}
}
