//go:build desktop

package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"gioui.org/layout"
)

// Installation state belongs to the window event goroutine. Rendering never
// starts version checks or installers; only explicit actions contact vendors.
type nativeManagedTools struct {
	snapshot      managedToolsSnapshot
	requestError  string
	confirmClient string
	confirmPlan   string
	installClient string
	completed     map[string]bool
	revision      uint64
	pollScheduled bool
}

func (u *nativeUI) managedToolsState() *nativeManagedTools {
	c := u.clientState()
	if c.ManagedTools == nil {
		c.ManagedTools = &nativeManagedTools{}
	}
	return c.ManagedTools
}

func (u *nativeUI) managedToolsPlatform() string {
	if platform := u.managedToolsState().snapshot.Platform; platform != "" {
		return platform
	}
	if platform := u.clientState().LaunchInfo.Platform; platform != "" {
		return platform
	}
	return runtime.GOOS
}

func (u *nativeUI) managedToolsSupported() bool {
	platform := u.managedToolsPlatform()
	return platform == "darwin" || platform == "macos" || platform == "linux"
}

func (u *nativeUI) managedToolsWorking() bool {
	if u.busy["POST"+managedToolsCheckEndpoint] || u.busy["POST"+managedToolsInstallEndpoint] {
		return true
	}
	for _, status := range u.managedToolsState().snapshot.Clients {
		if status.Busy {
			return true
		}
	}
	return false
}

func (u *nativeUI) applyManagedTools(raw json.RawMessage) bool {
	m := u.managedToolsState()
	var snapshot managedToolsSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Clients == nil {
		m.requestError = u.tr("Could not read the installation status. Check again.", "No se pudo leer el estado de instalación. Vuelve a comprobarlo.")
		return false
	}
	m.snapshot = snapshot
	m.requestError = ""
	for key := range snapshot.Clients {
		if len(managedToolChannels(key)) <= 1 {
			continue
		}
		field := "tool:" + key + ":channel"
		if u.value(field) == "installed" && snapshot.Clients[key].Channel != "" {
			u.setValue(field, snapshot.Clients[key].Channel)
		}
	}
	if status := snapshot.Clients[m.confirmClient]; m.confirmClient != "" && (status.Busy || status.PlanID != m.confirmPlan || !status.CanInstall && !status.CanUpdate) {
		m.confirmClient, m.confirmPlan = "", ""
	}
	if key := m.installClient; key != "" {
		status := snapshot.Clients[key]
		if !status.Busy && status.Phase == "complete" {
			m.installClient = ""
			if m.completed == nil {
				m.completed = map[string]bool{}
			}
			m.completed[key] = true
			u.refreshAgentInstallation(key)
			switch key {
			case "synara":
				u.refreshSynaraProfile()
			case "t3-code":
				u.refreshT3CodeProfile()
			case "openmausbot":
				u.refreshOpenMausBotProfile()
			case "open-design":
				u.detectOpenDesign()
			}
		} else if !status.Busy && status.Phase == "failed" {
			m.installClient = ""
		}
	}
	return true
}

func (u *nativeUI) cancelManagedTool(key string) {
	status := u.managedToolsState().snapshot.Clients[key]
	const endpoint = "/api/tools/cancel"
	if !status.Busy || status.Phase == "checking" || u.busy["POST"+endpoint] {
		return
	}
	m := u.managedToolsState()
	m.revision++
	revision := m.revision
	u.clientRequest("POST", endpoint, map[string]string{"client": key}, func(raw json.RawMessage, err error) {
		if revision != m.revision {
			return
		}
		if err != nil {
			m.requestError = nativeMessage(err.Error(), u.language)
			return
		}
		if u.applyManagedTools(raw) {
			u.pollManagedTools()
		}
	})
}

func (u *nativeUI) checkManagedTool(key string) {
	if m := u.managedToolsState(); m.requestError != "" && u.managedToolsWorking() {
		u.refreshManagedTools()
		return
	}
	if !u.managedToolsSupported() || u.managedToolsWorking() {
		return
	}
	m := u.managedToolsState()
	m.revision++
	revision := m.revision
	m.requestError, m.confirmClient, m.confirmPlan = "", "", ""
	payload := map[string]string{"client": key}
	if channel := u.managedToolChannel(key); channel != "" {
		payload["channel"] = channel
	}
	u.clientRequest("POST", managedToolsCheckEndpoint, payload, func(raw json.RawMessage, err error) {
		if revision != m.revision {
			return
		}
		if err != nil {
			m.requestError = nativeMessage(err.Error(), u.language)
			u.invalidateManagedToolPlan(key)
			return
		}
		if u.applyManagedTools(raw) {
			u.pollManagedTools()
		}
	})
}

func (u *nativeUI) refreshManagedTools() {
	if u.busy["GET"+managedToolsStatusEndpoint] {
		return
	}
	m := u.managedToolsState()
	revision := m.revision
	u.clientRequest("GET", managedToolsStatusEndpoint, nil, func(raw json.RawMessage, err error) {
		if revision != m.revision {
			return
		}
		if err != nil {
			m.requestError = u.tr("Could not refresh the installation status. Check again to reconnect.", "No se pudo actualizar el estado de instalación. Vuelve a comprobarlo para reconectar.")
			return
		}
		if u.applyManagedTools(raw) {
			u.pollManagedTools()
		}
	})
}

func (u *nativeUI) pollManagedTools() {
	m := u.managedToolsState()
	if m.pollScheduled || !u.managedToolsWorking() {
		return
	}
	m.pollScheduled = true
	go func() {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-u.owner.quit:
			return
		case <-timer.C:
			u.enqueue(func() {
				m.pollScheduled = false
				if !u.managedToolsWorking() || u.busy["GET"+managedToolsStatusEndpoint] {
					return
				}
				u.refreshManagedTools()
			})
		}
	}()
}

func (u *nativeUI) prepareManagedTool(key string) {
	status := u.managedToolsState().snapshot.Clients[key]
	if !u.managedToolPlanCurrent(key, status) {
		return
	}
	m := u.managedToolsState()
	m.confirmClient, m.confirmPlan = key, status.PlanID
}

func (u *nativeUI) installManagedTool(key string) {
	m := u.managedToolsState()
	status := m.snapshot.Clients[key]
	if !u.managedToolPlanCurrent(key, status) || m.confirmClient != key || m.confirmPlan != status.PlanID {
		m.confirmClient, m.confirmPlan = "", ""
		return
	}
	plan := m.confirmPlan
	m.confirmClient, m.confirmPlan, m.requestError = "", "", ""
	m.installClient = key
	m.revision++
	revision := m.revision
	u.clientRequest("POST", managedToolsInstallEndpoint, map[string]any{"client": key, "planID": plan, "confirm": true}, func(raw json.RawMessage, err error) {
		if revision != m.revision {
			return
		}
		if err != nil {
			m.installClient = ""
			m.requestError = nativeMessage(err.Error(), u.language)
			u.invalidateManagedToolPlan(key)
			return
		}
		if u.applyManagedTools(raw) {
			u.pollManagedTools()
		}
	})
}

func (u *nativeUI) invalidateManagedToolPlan(key string) {
	m := u.managedToolsState()
	status := m.snapshot.Clients[key]
	status.PlanID, status.CanInstall, status.CanUpdate = "", false, false
	if m.snapshot.Clients != nil {
		m.snapshot.Clients[key] = status
	}
}

func (u *nativeUI) managedToolChannel(key string) string {
	if len(managedToolChannels(key)) <= 1 {
		return ""
	}
	field := "tool:" + key + ":channel"
	if channel := u.value(field); channel != "" {
		if channel == "installed" {
			return ""
		}
		return channel
	}
	status := u.managedToolsState().snapshot.Clients[key]
	// Claude's native executable reports a version, not which independently
	// published update track installed it. Keep that initial choice explicit.
	if key != "claude" && !nativeManagedToolDetected(status) && u.uxhInstallState(key) == uxhInstalled {
		u.setValue(field, "installed")
		return ""
	}
	channel := status.InstalledChannel
	if channel == "" {
		channel = status.Channel
	}
	if channel == "" {
		channel = "stable"
		if key == "synara" {
			channel = "beta"
		}
	}
	u.setValue(field, channel)
	return channel
}

func nativeManagedToolDetected(status managedToolStatus) bool {
	return status.CheckedAt > 0 || status.Installed || status.Path != "" || status.Managed
}

func (u *nativeUI) managedToolPlanCurrent(key string, status managedToolStatus) bool {
	channel := u.managedToolChannel(key)
	return u.managedToolsSupported() && !u.managedToolsWorking() && status.PlanID != "" &&
		(status.CanInstall || status.CanUpdate) && (channel == "" || status.Channel == channel)
}

func (u *nativeUI) managedToolChannelRow(key string, status managedToolStatus) layout.Widget {
	channels := status.Channels
	if len(channels) == 0 {
		channels = managedToolChannels(key)
	}
	choices := make([]nativeChoice, 0, len(channels))
	selected := u.managedToolChannel(key)
	if selected == "" {
		selected = "installed"
		choices = append(choices, nativeChoice{Value: "installed", Label: u.tr("Installed channel", "Canal instalado")})
	}
	for _, channel := range channels {
		choices = append(choices, nativeChoice{Value: channel, Label: channel})
	}
	return u.uxhKV(u.tr("Release channel", "Canal de versiones"),
		u.segmented("tool:"+key+":channel.", choices, selected, !u.managedToolsWorking(), func(channel string) {
			u.setValue("tool:"+key+":channel", channel)
			m := u.managedToolsState()
			m.confirmClient, m.confirmPlan = "", ""
			status.PlanID, status.LatestVersion = "", ""
			status.CanInstall, status.CanUpdate = false, false
			status.Channel = channel
			if m.snapshot.Clients == nil {
				m.snapshot.Clients = map[string]managedToolStatus{}
			}
			m.snapshot.Clients[key] = status
		}))
}

func (u *nativeUI) managedToolPhase(phase string) string {
	switch phase {
	case "checking":
		return u.tr("Checking versions…", "Comprobando versiones…")
	case "downloading":
		return u.tr("Downloading…", "Descargando…")
	case "verifying":
		return u.tr("Verifying the download…", "Verificando la descarga…")
	case "installing":
		return u.tr("Installing the Kilo copy…", "Instalando la copia Kilo…")
	case "complete":
		return u.tr("Kilo copy ready", "Copia Kilo lista")
	case "failed":
		return u.tr("Installation failed", "La instalación ha fallado")
	}
	return ""
}

func (u *nativeUI) managedToolAction(status managedToolStatus) string {
	if status.Managed && status.CanUpdate {
		return u.tr("Update", "Actualizar")
	}
	if status.Installed && !status.Managed {
		return u.tr("Install Kilo copy", "Instalar copia Kilo")
	}
	return u.tr("Install", "Instalar")
}

func (u *nativeUI) managedToolCustomApplication(key string) bool {
	if key != "codex" {
		return false
	}
	u.agentsState()
	return strings.TrimSpace(u.value("clients-launch-app-path")) != ""
}

// Installing a copy must not silently replace a deliberately chosen app path.
// Persist the user's explicit choice before changing the in-memory launch path.
func (u *nativeUI) useManagedToolCopy(key string) {
	status := u.managedToolsState().snapshot.Clients[key]
	a := u.agentsState()
	if !status.Managed || !status.Installed || !u.managedToolCustomApplication(key) || u.managedToolsWorking() || u.uxhProfileWorking(key) || a.FolderBusy != "" {
		return
	}
	prefs := a.Preferences
	prefs.CodexAppPath = ""
	if err := writeAgentPreferences(u.owner.dir, prefs); err != nil {
		u.managedToolsState().requestError = u.tr("Could not change the selected app. Your custom application path is kept.", "No se pudo cambiar la app seleccionada. Se conserva tu ruta de aplicación personalizada.")
		return
	}
	a.Preferences = prefs
	u.setValue("clients-launch-app-path", "")
	u.managedToolsState().requestError = ""
	u.refreshAgentInstallation(key)
}

func (u *nativeUI) managedToolInstallationPanel(key string) layout.Widget {
	m := u.managedToolsState()
	status := m.snapshot.Clients[key]
	rows := []layout.Widget{}
	if !u.managedToolsSupported() {
		rows = append(rows, uxhCell(u.note(u.tr("Install and update this tool manually on Windows.", "Instala y actualiza esta herramienta manualmente en Windows."))))
	} else {
		installed := u.tr("Not detected", "No detectada")
		if status.Installed || !nativeManagedToolDetected(status) && u.uxhInstallState(key) == uxhInstalled {
			installed = u.tr("Installed · version not checked", "Instalada · versión sin comprobar")
		}
		if status.InstalledVersion != "" {
			installed = status.InstalledVersion
		}
		if status.Managed {
			installed += u.tr(" · Kilo copy", " · copia Kilo")
		}
		if status.InstalledChannel != "" {
			installed += " · " + status.InstalledChannel
		}
		rows = append(rows, u.uxhKV(u.tr("Installed version", "Versión instalada"), u.uxhValue(installed)))
		if len(managedToolChannels(key)) > 1 {
			rows = append(rows, u.managedToolChannelRow(key, status))
		}
		latest := u.tr("Not checked yet", "Sin comprobar")
		if status.LatestVersion != "" {
			latest = status.LatestVersion
			if status.Channel != "" {
				latest += " · " + status.Channel
			}
		}
		checking := u.busy["POST"+managedToolsCheckEndpoint] || status.Busy && status.Phase == "checking"
		checkLabel := u.tr("Check versions", "Comprobar versiones")
		if checking {
			checkLabel = u.tr("Checking…", "Comprobando…")
		}
		canCheck := (!u.managedToolsWorking() || m.requestError != "") && !u.busy["GET"+managedToolsStatusEndpoint] && !u.busy["POST"+managedToolsCheckEndpoint] && !u.busy["POST"+managedToolsInstallEndpoint]
		rows = append(rows, u.uxhKV(u.tr("Latest version", "Última versión"), u.uxhValue(latest),
			u.disabled(canCheck, u.button("tool:"+key+":check", checkLabel, func() { u.checkManagedTool(key) }))))
		if m.requestError != "" {
			rows = append(rows, uxhCell(u.message(nativeToneError, m.requestError)))
		}
		if status.Error != "" {
			rows = append(rows, uxhCell(u.message(nativeToneError, nativeMessage(status.Error, u.language))))
		}
		if status.UnsupportedReason != "" && status.UnsupportedReason != status.Error {
			rows = append(rows, uxhCell(u.note(nativeMessage(status.UnsupportedReason, u.language))))
		}
		if status.Busy || u.busy["POST"+managedToolsInstallEndpoint] {
			phase := u.managedToolPhase(status.Phase)
			if phase == "" {
				phase = u.tr("Starting installation…", "Iniciando instalación…")
			}
			rows = append(rows, uxhCell(u.hint(phase)))
			if status.Busy && status.Phase != "checking" {
				rows = append(rows, uxhCell(u.pills(u.disabled(!u.busy["POST/api/tools/cancel"], u.button("tool:"+key+":cancel-install", u.tr("Cancel installation", "Cancelar instalación"), func() { u.cancelManagedTool(key) })))))
			}
		} else if status.Phase == "complete" {
			message := u.tr("Versions checked", "Versiones comprobadas")
			if status.Managed && !status.CanUpdate {
				message = u.tr("The Kilo copy is up to date on this channel.", "La copia Kilo está al día en este canal.")
			}
			if m.completed[key] {
				message = u.tr("Kilo copy ready. Reopen the tool to use it.", "Copia Kilo lista. Vuelve a abrir la herramienta para usarla.")
				if u.managedToolCustomApplication(key) {
					message = u.tr("Kilo copy ready. Your custom application path is still selected.", "Copia Kilo lista. Tu ruta de aplicación personalizada sigue seleccionada.")
				}
				if key == "synara" || key == "t3-code" {
					message = u.tr("Kilo copy ready. Close the Kilo workspace, prepare it again, then reopen it. Your chats are kept.", "Copia Kilo lista. Cierra el espacio Kilo, prepáralo de nuevo y vuelve a abrirlo. Tus chats se conservan.")
				}
			}
			rows = append(rows, uxhCell(u.message(nativeToneSuccess, message)))
		} else if status.Phase == "failed" && status.Error == "" {
			rows = append(rows, uxhCell(u.message(nativeToneError, u.managedToolPhase(status.Phase))))
		}
		if status.Managed && u.managedToolCustomApplication(key) {
			rows = append(rows, u.uxhAdvancedRow(u.tr("Selected application", "Aplicación seleccionada"),
				u.tr("Your custom application path takes priority. Use the Kilo copy to clear that path; your accounts, chats and project folders are kept.", "Tu ruta de aplicación personalizada tiene prioridad. Usa la copia Kilo para quitar esa ruta; tus cuentas, chats y carpetas de proyecto se conservan."), nil,
				u.disabled(status.Installed && !u.managedToolsWorking() && !u.uxhProfileWorking(key) && u.agentsState().FolderBusy == "", u.button("tool:"+key+":use-copy", u.tr("Use Kilo copy", "Usar copia Kilo"), func() { u.useManagedToolCopy(key) }))))
		}
		if status.CanInstall || status.CanUpdate {
			description := u.tr("Installs a separate copy for Kilo Proxy. Your existing installation, accounts and chats are kept.", "Instala una copia separada para Kilo Proxy. Tu instalación actual, cuentas y chats se conservan.")
			rows = append(rows, u.uxhAdvancedRow(u.tr("Kilo installation", "Instalación Kilo"), description, nil,
				u.disabled(u.managedToolPlanCurrent(key, status), u.primaryButton("tool:"+key+":prepare", u.managedToolAction(status), func() { u.prepareManagedTool(key) }))))
		}
		if m.confirmClient == key && m.confirmPlan == status.PlanID && u.managedToolPlanCurrent(key, status) {
			version := status.LatestVersion
			if status.Channel != "" {
				version += " · " + status.Channel
			}
			method := status.Method
			if method == "" || method == "kilo" {
				method = u.tr("Verified official download", "Descarga oficial verificada")
				if key == "t3-code" || key == "synara" {
					method = "GitHub Releases"
				}
			}
			scope := status.Scope
			if scope == "" || scope == "A separate Kilo-managed installation. Existing installations, accounts and chats are kept." {
				scope = u.tr("Private tools folder in your Kilo Proxy workspace", "Carpeta privada de herramientas de tu espacio Kilo Proxy")
			}
			selection := u.tr("Your existing installation and profiles are kept. Kilo Proxy will use this copy the next time you open the tool.", "Tu instalación y perfiles actuales se conservan. Kilo Proxy usará esta copia la próxima vez que abras la herramienta.")
			if u.managedToolCustomApplication(key) {
				selection = u.tr("Your existing installation and profiles are kept. Your custom application path stays selected until you choose Use Kilo copy.", "Tu instalación y perfiles actuales se conservan. Tu ruta de aplicación personalizada seguirá seleccionada hasta que elijas Usar copia Kilo.")
			}
			rows = append(rows, uxhCell(u.column(
				u.subheading(fmt.Sprintf(u.tr("Install %s %s?", "¿Instalar %s %s?"), uxhName(key), version)),
				u.note(u.tr("Method: ", "Método: ")+nativeMessage(method, u.language)),
				u.note(u.tr("Location: ", "Ubicación: ")+nativeMessage(scope, u.language)),
				u.note(selection),
				u.pills(u.button("tool:"+key+":cancel", u.tr("Cancel", "Cancelar"), func() { m.confirmClient, m.confirmPlan = "", "" }),
					u.primaryButton("tool:"+key+":confirm", u.tr("Confirm installation", "Confirmar instalación"), func() { u.installManagedTool(key) })),
			)))
		}
	}
	if url := u.uxhInstallURL(key); url != "" {
		rows = append(rows, u.uxhAdvancedRow(u.tr("Manual installation", "Instalación manual"), u.tr("Official download and setup instructions.", "Descarga e instrucciones oficiales."), nil,
			u.buttonWidget("tool:"+key+":manual", u.tr("How to install", "Cómo instalarlo"), nativeButtonSecondary, nativeIconOpenInNew, true, false, func() { u.open(url) })))
	}
	return u.uxhBlock(u.tr("Installation", "Instalación"), "", rows...)
}
