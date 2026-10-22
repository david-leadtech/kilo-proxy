package main

// trayLabels is independent of the native toolkit so every visible menu label,
// tooltip and status message follows the same persisted language as the window.
type trayLabels struct {
	tooltip, loading, teamEmpty, teamPrefix                                                                string
	stopped, running, pending, unconfigured, activity                                                      string
	open, start, setup, stop, hint, exit, openError, startError                                            string
	spend, subtotal, coverage                                                                              string
	balance, balanceTeam, balancePersonal, balanceUnavailable, balanceStale, balanceLoading, balanceSignIn string
}

func trayText(language string) trayLabels {
	if language == "es" {
		return trayLabels{
			tooltip: "Kilo Proxy — conexión de tu equipo", loading: "Cargando…",
			teamEmpty: "Equipo: sin seleccionar", teamPrefix: "Equipo: ",
			stopped: "○ Lista · se activa al abrir una app", running: "● Conexión con Kilo activa", pending: "○ Esperando login de Kilo…",
			unconfigured: "○ Falta terminar la configuración",
			activity:     "%d en curso · %d peticiones · %d errores",
			spend:        "Coste desde que abriste Kilo Proxy", subtotal: "Subtotal informado de esta sesión", coverage: "Coste informado: %d/%d peticiones",
			balance: "Saldo de Kilo", balanceTeam: "Saldo del equipo", balancePersonal: "Saldo personal", balanceUnavailable: "Saldo no disponible", balanceStale: "Saldo desactualizado; actualízalo en Actividad", balanceLoading: "Actualizando saldo…", balanceSignIn: "Inicia sesión para consultar el saldo",
			open: "Abrir Kilo Proxy…", start: "Activar ahora", setup: "Terminar la configuración…",
			stop: "Pausar (corta lo que esté en curso)", hint: "Kilo Proxy sigue funcionando aquí aunque cierres la ventana", exit: "Salir de Kilo Proxy",
			openError: "No se pudo abrir Kilo Proxy", startError: "No se pudo activar; abre Kilo Proxy para ver qué ha pasado",
		}
	}
	return trayLabels{
		tooltip: "Kilo Proxy — your team connection", loading: "Loading…",
		teamEmpty: "Team: not selected", teamPrefix: "Team: ",
		stopped: "○ Ready · turns on when you open an app", running: "● Kilo connection active", pending: "○ Waiting for Kilo login…",
		unconfigured: "○ Setup not finished",
		activity:     "%d active · %d requests · %d errors",
		spend:        "Cost since opening Kilo Proxy", subtotal: "Reported subtotal this session", coverage: "Cost reported: %d/%d requests",
		balance: "Kilo balance", balanceTeam: "Team balance", balancePersonal: "Personal balance", balanceUnavailable: "Balance unavailable", balanceStale: "Balance out of date; refresh in Activity", balanceLoading: "Refreshing balance…", balanceSignIn: "Sign in to check your balance",
		open: "Open Kilo Proxy…", start: "Turn on now", setup: "Finish setup…",
		stop: "Pause (stops anything in progress)", hint: "Kilo Proxy keeps running here when you close the window", exit: "Quit Kilo Proxy",
		openError: "Could not open Kilo Proxy", startError: "Could not turn on; open Kilo Proxy to see what happened",
	}
}

func (labels trayLabels) message(kind string) string {
	switch kind {
	case "open-error":
		return labels.openError
	case "start-error":
		return labels.startError
	default:
		return labels.hint
	}
}
