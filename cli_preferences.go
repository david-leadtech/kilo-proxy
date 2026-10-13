package main

import (
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
)

const cliPreferencesEndpoint = "/api/clients/cli/preferences"

type cliPreferences struct {
	Version          int    `json:"version"`
	CodexExecutable  string `json:"codexExecutable"`
	ClaudeExecutable string `json:"claudeExecutable"`
	MuseExecutable   string `json:"museExecutable"`
	SynaraExecutable string `json:"synaraExecutable"`
}

func validateCLIExecutable(path string) error {
	if path != "" && (len(path) > 8192 || !packageUpdateAbsolute(path) || strings.ContainsAny(path, "\x00\r\n")) {
		return errors.New("Choose an absolute CLI executable path, or leave it empty for automatic detection.")
	}
	return nil
}

// Resolvers also run while app.mu is held by profile preparation. Keep the
// selected paths in a separate snapshot rather than acquiring that lock again.
func (a *app) cliExecutable(client string) string {
	a.cliPathsMu.RLock()
	defer a.cliPathsMu.RUnlock()
	switch client {
	case "codex-cli":
		return a.cliExecutables[0]
	case "claude":
		return a.cliExecutables[1]
	case "muse":
		return a.museExecutable
	case "synara":
		return a.synaraExecutable
	}
	return ""
}

func selectedClientPathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// An explicit path inside Kilo's tool store must remain the verified current
// entrypoint. Selecting a retained release or an alias cannot bypass its digest.
// External installations are user choices and do not acquire managed status.
func (a *app) validateSelectedClientExecutable(client, path string) error {
	if validateCLIExecutable(path) != nil || path == "" || !launchExecutable(path) {
		return errors.New("The selected CLI executable is unavailable. Choose its installed path or return to automatic detection in Kilo settings.")
	}
	tools := filepath.Join(a.dir, "tools")
	owned := a.dir != "" && selectedClientPathWithin(tools, path)
	if canonicalTools, err := filepath.EvalSymlinks(tools); err == nil {
		if canonicalPath, err := filepath.EvalSymlinks(path); err == nil && selectedClientPathWithin(canonicalTools, canonicalPath) {
			owned = true
		}
	}
	if !owned {
		return nil
	}
	record, err := readManagedTool(a.dir, client)
	current := err == nil && filepath.Clean(record.Executable) == filepath.Clean(path)
	if err == nil && !current {
		// macOS can spell the profile's temporary parent as /var or
		// /private/var. A native npm dependency is returned canonically;
		// accept that spelling, but never an arbitrary symlink alias.
		canonical, canonicalErr := filepath.EvalSymlinks(record.Executable)
		current = canonicalErr == nil && filepath.Clean(path) == canonical
	}
	if runtime.GOOS == "windows" || !current {
		return errors.New("Choose the verified current Kilo installation, or install the desired version again from Kilo settings.")
	}
	return nil
}

func (a *app) cliPreferencesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		jsonError(w, 405, "Method not allowed.")
		return
	}
	var input struct {
		Version          int     `json:"version"`
		CodexExecutable  *string `json:"codexExecutable,omitempty"`
		ClaudeExecutable *string `json:"claudeExecutable,omitempty"`
		MuseExecutable   *string `json:"museExecutable,omitempty"`
		SynaraExecutable *string `json:"synaraExecutable,omitempty"`
	}
	if r.Method == http.MethodPost {
		if !decodeBody(w, r, &input) {
			return
		}
		if input.Version != 1 {
			jsonError(w, 400, "Invalid CLI executable preferences.")
			return
		}
		if !a.launchMu.TryLock() {
			jsonError(w, 409, "Another launch is already being prepared.")
			return
		}
		defer a.launchMu.Unlock()
		for _, selected := range []struct {
			client string
			path   *string
		}{{"codex-cli", input.CodexExecutable}, {"claude", input.ClaudeExecutable}, {"muse", input.MuseExecutable}} {
			if selected.path == nil || *selected.path == "" {
				continue
			}
			if err := validateCLIExecutable(*selected.path); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
			if err := a.validateSelectedClientExecutable(selected.client, *selected.path); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
		}
		if input.SynaraExecutable != nil && *input.SynaraExecutable != "" {
			if err := validateSynaraExecutablePath(*input.SynaraExecutable); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
			if err := a.validateSelectedSynaraExecutable(*input.SynaraExecutable, a.launchRuntime().platform); err != nil {
				jsonError(w, 400, err.Error())
				return
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		next := a.config
		if input.CodexExecutable != nil {
			next.CodexCLIExecutable = *input.CodexExecutable
		}
		if input.ClaudeExecutable != nil {
			next.ClaudeCodeExecutable = *input.ClaudeExecutable
		}
		if input.MuseExecutable != nil {
			next.MuseCodeExecutable = *input.MuseExecutable
		}
		if input.SynaraExecutable != nil {
			next.SynaraExecutable = *input.SynaraExecutable
		}
		if err := writeSettings(a.dir, next); err != nil {
			jsonError(w, 500, "Cannot save CLI executable preferences. Your previous settings are unchanged.")
			return
		}
		a.config = next
		a.cliPathsMu.Lock()
		a.cliExecutables = [2]string{next.CodexCLIExecutable, next.ClaudeCodeExecutable}
		a.museExecutable = next.MuseCodeExecutable
		a.synaraExecutable = next.SynaraExecutable
		a.cliPathsMu.Unlock()
	}
	jsonResponse(w, 200, cliPreferences{Version: 1, CodexExecutable: a.config.CodexCLIExecutable, ClaudeExecutable: a.config.ClaudeCodeExecutable, MuseExecutable: a.config.MuseCodeExecutable, SynaraExecutable: a.config.SynaraExecutable})
}
