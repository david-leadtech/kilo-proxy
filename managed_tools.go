package main

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

type managedToolPlan struct {
	release  managedToolRelease
	identity string
	expires  time.Time
}

type managedToolsManager struct {
	mu              sync.Mutex
	owner           *app
	platform, arch  string
	catalog         managedToolCatalog
	statuses        map[string]managedToolStatus
	plans           map[string]managedToolPlan
	ctx             context.Context
	cancel          context.CancelFunc
	operation       context.CancelFunc
	operationClient string
	install         func(context.Context, *app, managedToolRelease, func(string)) (managedToolRecord, error)
}

func managedToolKnown(client string) bool {
	for _, id := range launchClients {
		if client == id {
			return true
		}
	}
	return false
}

func (a *app) toolsManager() *managedToolsManager {
	a.managedToolsMu.Lock()
	defer a.managedToolsMu.Unlock()
	if a.managedTools == nil {
		ctx, cancel := context.WithCancel(context.Background())
		rt := a.launchRuntime()
		platform := rt.platform
		if platform == "macos" {
			platform = "darwin"
		}
		m := &managedToolsManager{owner: a, platform: platform, arch: runtime.GOARCH, catalog: &vendorManagedToolCatalog{}, statuses: map[string]managedToolStatus{}, plans: map[string]managedToolPlan{}, ctx: ctx, cancel: cancel, install: installManagedTool}
		for _, client := range launchClients {
			name, _ := launchClientIdentity(client)
			reason, url := managedToolSupport(client, platform, m.arch)
			if reason == "" {
				reason = managedToolSystemRequirement(client, platform)
			}
			m.statuses[client] = managedToolStatus{Client: client, Name: name, UnsupportedReason: reason, InstallURL: url, Channels: managedToolChannels(client), Method: "kilo", Scope: "A separate Kilo-managed installation. Existing installations, accounts and chats are kept."}
		}
		a.managedTools = m
		go func() {
			select {
			case <-a.quit:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return a.managedTools
}

func (m *managedToolsManager) snapshot() managedToolsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := managedToolsSnapshot{Platform: m.platform, Clients: make(map[string]managedToolStatus, len(m.statuses))}
	for client, status := range m.statuses {
		status.Channels = append([]string(nil), status.Channels...)
		result.Clients[client] = status
	}
	return result
}

func (a *app) managedToolsAPI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == managedToolsStatusEndpoint {
		if r.Method != http.MethodGet {
			jsonError(w, 405, "Method not allowed.")
			return
		}
		jsonResponse(w, 200, a.toolsManager().snapshot())
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input struct {
		Client  string `json:"client"`
		Channel string `json:"channel,omitempty"`
		PlanID  string `json:"planID,omitempty"`
		Confirm bool   `json:"confirm,omitempty"`
	}
	if !decodeBody(w, r, &input) {
		return
	}
	if !managedToolKnown(input.Client) {
		jsonError(w, 400, "Unknown launch client.")
		return
	}
	m := a.toolsManager()
	var err error
	switch r.URL.Path {
	case managedToolsCheckEndpoint:
		err = m.check(input.Client, input.Channel)
	case managedToolsInstallEndpoint:
		if !input.Confirm {
			jsonError(w, 409, "Confirm the tool installation before continuing.")
			return
		}
		if a.headless {
			jsonError(w, 409, "Install tools from the desktop application.")
			return
		}
		err = m.start(input.Client, input.PlanID)
	case "/api/tools/cancel":
		m.mu.Lock()
		if m.operationClient == input.Client && m.operation != nil {
			m.operation()
		}
		m.mu.Unlock()
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		jsonError(w, 409, err.Error())
		return
	}
	jsonResponse(w, http.StatusAccepted, m.snapshot())
}

func managedToolIdentity(path, version, channel string, managed bool) string {
	// A plan cannot silently change target or adopt an installation that moved.
	return path + "\x00" + version + "\x00" + channel + "\x00" + map[bool]string{true: "kilo", false: "external"}[managed]
}

func (m *managedToolsManager) installed(ctx context.Context, client string) (managedToolStatus, string) {
	status := managedToolStatus{Client: client}
	if record, err := readManagedTool(m.owner.dir, client); err == nil {
		status.Installed, status.Managed, status.Path = true, true, record.Executable
		status.InstalledVersion, status.InstalledChannel = record.Version, record.Channel
	} else if path, err := m.owner.launchRuntime().resolve(client, ""); err == nil {
		status.Installed, status.Path = true, path
		status.InstalledVersion, status.InstalledChannel, _ = managedToolInstalledVersion(ctx, client, path, m.platform, m.arch)
	}
	return status, managedToolIdentity(status.Path, status.InstalledVersion, status.InstalledChannel, status.Managed)
}

func (m *managedToolsManager) check(client, channel string) error {
	if m.ctx.Err() != nil {
		return errors.New("Kilo Proxy is closing.")
	}
	if channel != "" {
		valid := false
		for _, c := range managedToolChannels(client) {
			valid = valid || c == channel
		}
		if !valid {
			return errors.New("Choose an available release channel.")
		}
	}
	m.mu.Lock()
	status := m.statuses[client]
	if status.Busy {
		m.mu.Unlock()
		return errors.New("A check or installation is already running for this tool.")
	}
	status.Busy, status.Phase, status.Error = true, "checking", ""
	status.CanInstall, status.CanUpdate, status.PlanID = false, false, ""
	delete(m.plans, client)
	m.statuses[client] = status
	m.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 45*time.Second)
		defer cancel()
		installed, identity := m.installed(ctx, client)
		if channel == "" {
			channel = installed.InstalledChannel
		}
		if channel == "" {
			channel = "stable"
			if client == "synara" {
				channel = "beta"
			}
		}
		reason := managedToolSystemRequirement(client, m.platform)
		var release managedToolRelease
		var err error
		if reason != "" {
			err = errors.New(reason)
		} else {
			release, err = m.catalog.Latest(ctx, client, m.platform, m.arch, channel)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		status := m.statuses[client]
		status.Installed, status.Managed, status.Path = installed.Installed, installed.Managed, installed.Path
		status.InstalledVersion, status.InstalledChannel = installed.InstalledVersion, installed.InstalledChannel
		status.Channel, status.CheckedAt, status.Busy = channel, time.Now().Unix(), false
		status.UnsupportedReason, _ = managedToolSupport(client, m.platform, m.arch)
		if reason != "" {
			status.UnsupportedReason = reason
		}
		if err != nil {
			status.Phase, status.Error = "failed", err.Error()
			m.statuses[client] = status
			return
		}
		status.Channel, status.LatestVersion, status.Phase = release.Channel, release.Version, "complete"
		status.CanInstall = !status.Managed
		status.CanUpdate = status.Managed && (status.InstalledChannel != release.Channel || managedToolVersionNewer(release.Version, status.InstalledVersion))
		if status.CanInstall || status.CanUpdate {
			status.PlanID = randomKey("")
			m.plans[client] = managedToolPlan{release: release, identity: identity, expires: time.Now().Add(10 * time.Minute)}
		}
		m.statuses[client] = status
	}()
	return nil
}

func managedToolVersionNewer(latest, installed string) bool {
	left, leftOK := parseReleaseVersion(latest)
	right, rightOK := parseReleaseVersion(installed)
	if leftOK && rightOK {
		return compareReleaseVersions(left, right) > 0
	}
	// The catalog validates release labels; unknown older vendor labels can be
	// replaced, but two known versions in the same channel are never downgraded.
	return latest != installed
}

func (m *managedToolsManager) start(client, planID string) error {
	if m.platform != "darwin" && m.platform != "linux" {
		return errors.New("Install and update tools manually on Windows.")
	}
	if m.ctx.Err() != nil {
		return errors.New("Kilo Proxy is closing.")
	}
	if reason := managedToolSystemRequirement(client, m.platform); reason != "" {
		return errors.New(reason)
	}
	m.mu.Lock()
	status := m.statuses[client]
	plan, ok := m.plans[client]
	if !ok || status.Busy || planID == "" || !secureEqual(planID, status.PlanID) || time.Now().After(plan.expires) {
		m.mu.Unlock()
		return errors.New("Check the chosen release channel again before installing.")
	}
	if m.operationClient != "" {
		m.mu.Unlock()
		return errors.New("Another tool installation is already running.")
	}
	if !m.owner.launchMu.TryLock() {
		m.mu.Unlock()
		return errors.New("Another launch or installation is being prepared.")
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
	m.operation, m.operationClient = cancel, client
	status.Busy, status.Phase, status.Error = true, "downloading", ""
	status.CanInstall, status.CanUpdate, status.PlanID = false, false, ""
	m.statuses[client] = status
	delete(m.plans, client)
	m.mu.Unlock()
	go func() {
		defer m.owner.launchMu.Unlock()
		defer cancel()
		_, identity := m.installed(ctx, client)
		var record managedToolRecord
		var err error
		if identity != plan.identity {
			err = errors.New("The tool installation changed. Check versions again.")
		} else {
			record, err = m.install(ctx, m.owner, plan.release, func(phase string) {
				m.mu.Lock()
				s := m.statuses[client]
				s.Phase = phase
				m.statuses[client] = s
				m.mu.Unlock()
			})
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		status := m.statuses[client]
		status.Busy = false
		m.operation, m.operationClient = nil, ""
		if err != nil {
			status.Phase, status.Error = "failed", err.Error()
			if ctx.Err() != nil {
				status.Error = "The installation was cancelled. The previous installation is still available."
			}
		} else {
			status.Phase, status.Error = "complete", ""
			status.Installed, status.Managed, status.Path = true, true, record.Executable
			status.InstalledVersion, status.InstalledChannel = record.Version, record.Channel
			status.CanInstall, status.CanUpdate = false, false
		}
		m.statuses[client] = status
	}()
	return nil
}

func validManagedToolRelease(release managedToolRelease) bool {
	if !managedToolKnown(release.Client) || release.Version == "" || len(release.Version) > 128 || strings.ContainsAny(release.Version, "\x00\r\n") || len(release.SHA256) != 64 {
		return false
	}
	for _, ch := range release.SHA256 {
		if !strings.ContainsRune("0123456789abcdef", ch) {
			return false
		}
	}
	return release.Size >= 0 && release.Size <= 2<<30
}
