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
	for _, choice := range []struct{ value, en, es, detailEN, detailES string }{
		{"off", "Off", "Desactivado", "Send images unchanged", "Enviar imágenes sin cambios"},
		{"compress", "Compress locally", "Comprimir en local", "Smaller outbound copies", "Copias de envío más pequeñas"},
		{"cloudflare", "Cloudflare", "Cloudflare", "Local files · public tunnel", "Archivos locales · túnel público"},
		{"tailscale", "Tailscale Funnel", "Tailscale Funnel", "Use your existing Tailscale", "Usa tu instalación de Tailscale"},
		{"litterbox", "Litterbox · Experimental", "Litterbox · Experimental", "Temporary third-party hosting", "Alojamiento temporal externo"},
		{"upload", "Kilo · Experimental", "Kilo · Experimental", "Kilo attachment storage", "Almacenamiento de adjuntos de Kilo"},
	} {
		label := "○ " + u.tr(choice.en, choice.es)
		if settings.Mode == choice.value {
			label = "● " + u.tr(choice.en, choice.es)
		}
		choices = append(choices, u.modelCard(settings.Mode == choice.value,
			u.disabled(!saving, u.button("images.mode."+choice.value, label, func() { next := settings; next.Mode = choice.value; u.saveImageTransportSettings(next) })),
			u.note(u.tr(choice.detailEN, choice.detailES)),
		))
	}
	children := []layout.Widget{
		u.heading(u.tr("Large images", "Imágenes grandes")),
		u.note(u.tr("Choose one method for inline images when a request exceeds 4.4 MB. Works with Responses, Chat Completions, and Anthropic Messages. Smaller requests and your original files stay unchanged.", "Elige un método para las imágenes cuando una petición supera 4,4 MB. Funciona con Responses, Chat Completions y Anthropic Messages. Las peticiones pequeñas y tus archivos originales no cambian.")),
		u.topRow(choices...),
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
	case "cloudflare":
		children = append(children,
			u.label(u.tr("Cloudflare quick tunnel", "Túnel rápido de Cloudflare")),
			u.note(u.tr("Requires cloudflared installed on this computer and available on PATH. No Cloudflare account, S3 bucket, or ngrok setup is needed. The tunnel starts when a large request needs it.", "Requiere cloudflared instalado en este equipo y disponible en PATH. No necesita cuenta de Cloudflare, un bucket S3 ni configurar ngrok. El túnel se inicia cuando lo necesita una petición grande.")),
			u.note(u.tr("Serves original image bytes from this computer through public, unguessable links. Links are removed after the request finishes or is cancelled. Keep Kilo Proxy running while images are in use.", "Sirve las imágenes originales desde este equipo mediante enlaces públicos difíciles de adivinar. Los enlaces se retiran al terminar o cancelar la petición. Mantén Kilo Proxy abierto mientras se usan las imágenes.")),
		)
	case "tailscale":
		children = append(children,
			u.label("Tailscale Funnel"),
			u.note(u.tr("Requires the tailscale command, a signed-in account, and Funnel enabled for this device. Uses a dedicated HTTPS port 8443; leave it free for Kilo Proxy.", "Requiere el comando tailscale, una cuenta con sesión iniciada y Funnel habilitado para este dispositivo. Usa el puerto HTTPS 8443; déjalo libre para Kilo Proxy.")),
			u.note(u.tr("Funnel makes image links publicly reachable, including outside your tailnet. Images stay on this computer and links are removed after the request finishes or is cancelled.", "Funnel permite acceder a los enlaces de imágenes desde Internet, incluso fuera de tu tailnet. Las imágenes permanecen en este equipo y los enlaces se retiran al terminar o cancelar la petición.")),
		)
	case "litterbox":
		children = append(children,
			u.label(u.tr("Temporary public hosting", "Alojamiento público temporal")),
			u.note(u.tr("Experimental: live availability could not be confirmed from this network. If the service rejects uploads, choose Cloudflare or local compression.", "Experimental: no se ha podido confirmar la disponibilidad real desde esta red. Si el servicio rechaza las subidas, elige Cloudflare o la compresión local.")),
			u.note(u.tr("Uploads original images to Litterbox, a third-party service. Anyone with the link can access them until expiry. No account or extra executable is required. Choose this only for images you can share with that service.", "Sube las imágenes originales a Litterbox, un servicio externo. Cualquiera con el enlace puede acceder hasta que caduque. No requiere cuenta ni ejecutables adicionales. Elígelo solo para imágenes que puedas compartir con ese servicio.")),
			u.note(u.tr("Litterbox requires prior approval for commercial service use; see its FAQ before using it for your team.", "Litterbox requiere autorización previa para uso en servicios comerciales; consulta sus preguntas frecuentes antes de usarlo con tu equipo.")),
			u.button("images.litterbox.faq", u.tr("Litterbox FAQ", "Preguntas frecuentes de Litterbox"), func() { u.open("https://litterbox.catbox.moe/faq.php") }),
			u.label(u.tr("Link expiry", "Caducidad del enlace")),
		)
		expiries := []layout.Widget{}
		for _, ttl := range []struct{ value, en, es string }{
			{"1h", "1 hour", "1 hora"}, {"12h", "12 hours", "12 horas"}, {"24h", "24 hours", "24 horas"}, {"72h", "72 hours", "72 horas"},
		} {
			label := "○ " + u.tr(ttl.en, ttl.es)
			if settings.LitterboxTTL == ttl.value {
				label = "● " + u.tr(ttl.en, ttl.es)
			}
			expiries = append(expiries, u.disabled(!saving, u.button("images.litterboxTTL."+ttl.value, label, func() { next := settings; next.LitterboxTTL = ttl.value; u.saveImageTransportSettings(next) })))
		}
		children = append(children, u.pills(expiries...), u.note(u.tr("Litterbox handles expiry. Kilo Proxy cannot delete these uploads early, even after you switch modes or close the app.", "Litterbox gestiona la caducidad. Kilo Proxy no puede borrar estas subidas antes, aunque cambies de modo o cierres la app.")))
	case "upload":
		children = append(children, u.note(u.tr("Uploads original image bytes to Kilo and sends temporary links. No resizing, tunnel, or storage setup is needed. Up to 5 unique images uploaded per request, 20 MiB each.", "Sube las imágenes originales a Kilo y envía enlaces temporales. No cambia su tamaño ni requiere configurar un túnel o almacenamiento. Hasta 5 imágenes únicas subidas por petición y 20 MiB por imagen.")), u.note(u.tr("Experimental: uses Kilo's Cloud Agent attachment storage with your account. This is not a documented Gateway integration and may stop working.", "Experimental: usa el almacenamiento de adjuntos de Cloud Agent de Kilo con tu cuenta. No es una integración documentada del Gateway y puede dejar de funcionar.")), u.note(u.tr("Deletion is requested after completion or cancellation. Network failures or an app crash can leave remote copies behind; an expired link does not mean the image was deleted. Any unconfirmed deletion is shown here.", "Se solicita el borrado al terminar o cancelar. Un fallo de red o el cierre inesperado de la app puede dejar copias remotas; que un enlace caduque no significa que la imagen se haya borrado. Los borrados sin confirmar se muestran aquí.")))
	default:
		children = append(children, u.note(u.tr("Images pass through unchanged. Large requests can still exceed Kilo's limit and need conversation compaction or fewer attachments.", "Las imágenes se envían sin cambios. Las peticiones grandes pueden superar el límite de Kilo y requerir compactar la conversación o reducir los adjuntos.")))
	}
	children = append(children, u.note(u.tr("Off by default. Changes save automatically for new requests, without restarting. Only the selected method is used; failures never switch to another backend or upload service. Earlier uploads still receive their scheduled cleanup.", "Desactivado por defecto. Los cambios se guardan automáticamente para nuevas peticiones, sin reiniciar. Solo se usa el método elegido; los fallos nunca cambian a otro backend o servicio de subida. Las subidas anteriores conservan su limpieza programada.")))
	if warning := nativeString(u.state, "imageUploadWarning"); warning != "" {
		children = append(children, u.label(u.tr("Image cleanup needs attention", "Revisa la limpieza de imágenes")), u.note(nativeMessage(warning, u.language)))
	}
	if saving {
		children = append(children, u.note(u.tr("Saving image preference…", "Guardando preferencia de imágenes…")))
	}
	return u.card(children...)
}
