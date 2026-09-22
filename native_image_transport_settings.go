//go:build desktop

package main

import (
	"encoding/json"
	"gioui.org/layout"
)

func (u *nativeUI) saveImageTransportSettings(value imageTransportSettings) {
	u.call("PUT", "/api/image-transport-settings", value, func(raw json.RawMessage) {
		var saved imageTransportSettings
		if json.Unmarshal(raw, &saved) == nil {
			u.state["imageTransport"] = saved
		}
	})
}

func (u *nativeUI) imageTransportPanel() layout.Widget {
	saving := u.busy["PUT/api/image-transport-settings"]
	u.owner.mu.Lock()
	settings := normalizeImageTransportSettings(u.owner.config.ImageTransport)
	u.owner.mu.Unlock()
	choices := []layout.Widget{}
	for _, choice := range []struct{ value, en, es string }{
		{"off", "Off", "Desactivado"},
		{"compress", "Compress locally", "Comprimir en local"},
		{"upload", "Upload to Kilo · Experimental", "Subir a Kilo · Experimental"},
	} {
		label := "○ " + u.tr(choice.en, choice.es)
		if settings.Mode == choice.value {
			label = "● " + u.tr(choice.en, choice.es)
		}
		choices = append(choices, u.disabled(!saving, u.button("images.mode."+choice.value, label, func() { next := settings; next.Mode = choice.value; u.saveImageTransportSettings(next) })))
	}
	children := []layout.Widget{
		u.heading(u.tr("Large images", "Imágenes grandes")),
		u.note(u.tr("Choose how to handle inline images when a Responses request exceeds 4.4 MB, just below Kilo's limit. Smaller requests and your original files stay unchanged.", "Elige cómo tratar las imágenes cuando una petición Responses supera 4,4 MB, justo por debajo del límite de Kilo. Las peticiones pequeñas y tus archivos originales no cambian.")),
		u.pills(choices...),
	}
	switch settings.Mode {
	case "compress":
		children = append(children, u.label(u.tr("Compression profile", "Perfil de compresión")))
		profiles := []layout.Widget{}
		for _, profile := range []struct{ value, en, es string }{
			{"high", "High quality", "Alta calidad"},
			{"balanced", "Balanced", "Equilibrado"},
			{"small", "Small size", "Tamaño pequeño"},
		} {
			label := "○ " + u.tr(profile.en, profile.es)
			if settings.Profile == profile.value {
				label = "● " + u.tr(profile.en, profile.es)
			}
			profiles = append(profiles, u.disabled(!saving, u.button("images.profile."+profile.value, label, func() { next := settings; next.Profile = profile.value; u.saveImageTransportSettings(next) })))
		}
		children = append(children, u.pills(profiles...), u.note(u.tr("High quality: up to 3072 px / quality 92. Balanced: 2048 px / quality 85. Small size: 1280 px / quality 75. Aspect ratio is preserved.", "Alta calidad: hasta 3072 px / calidad 92. Equilibrado: 2048 px / calidad 85. Tamaño pequeño: 1280 px / calidad 75. Se conserva la proporción.")), u.note(u.tr("Tries lossless optimization first, then your selected profile if needed. Only outbound copies change. If the request still does not fit, it stops with an explanation; it never lowers quality further or uploads images automatically.", "Primero intenta optimizar sin pérdidas y después aplica el perfil elegido si hace falta. Solo cambian las copias enviadas. Si la petición sigue sin caber, se detiene y lo explica; nunca reduce más la calidad ni sube imágenes automáticamente.")))
	case "upload":
		children = append(children, u.note(u.tr("Uploads original image bytes to Kilo and sends temporary links. No resizing, tunnel, or storage setup is needed. Up to 5 unique images uploaded per request, 20 MiB each.", "Sube las imágenes originales a Kilo y envía enlaces temporales. No cambia su tamaño ni requiere configurar un túnel o almacenamiento. Hasta 5 imágenes únicas subidas por petición y 20 MiB por imagen.")), u.note(u.tr("Experimental: uses Kilo's Cloud Agent attachment storage with your account. This is not a documented Gateway integration and may stop working.", "Experimental: usa el almacenamiento de adjuntos de Cloud Agent de Kilo con tu cuenta. No es una integración documentada del Gateway y puede dejar de funcionar.")), u.note(u.tr("Deletion is requested after completion or cancellation. Network failures or an app crash can leave remote copies behind; an expired link does not mean the image was deleted. Any unconfirmed deletion is shown here.", "Se solicita el borrado al terminar o cancelar. Un fallo de red o el cierre inesperado de la app puede dejar copias remotas; que un enlace caduque no significa que la imagen se haya borrado. Los borrados sin confirmar se muestran aquí.")))
	default:
		children = append(children, u.note(u.tr("Images pass through unchanged. Large requests can still exceed Kilo's limit and need conversation compaction or fewer attachments.", "Las imágenes se envían sin cambios. Las peticiones grandes pueden superar el límite de Kilo y requerir compactar la conversación o reducir los adjuntos.")))
	}
	children = append(children, u.note(u.tr("Off by default. Changes save automatically for new requests, without restarting. Switching mode still allows cleanup of earlier uploads.", "Desactivado por defecto. Los cambios se guardan automáticamente para nuevas peticiones, sin reiniciar. Cambiar de modo permite que continúe la limpieza de subidas anteriores.")))
	if warning := nativeString(u.state, "imageUploadWarning"); warning != "" {
		children = append(children, u.label(u.tr("Image cleanup needs attention", "Revisa la limpieza de imágenes")), u.note(nativeMessage(warning, u.language)))
	}
	if saving {
		children = append(children, u.note(u.tr("Saving image preference…", "Guardando preferencia de imágenes…")))
	}
	return u.card(children...)
}
