package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

type museKiloCatalogModel struct {
	ModelID                 string              `json:"model_id"`
	ProviderID              string              `json:"provider_id"`
	ProfileID               string              `json:"profile_id"`
	DisplayLabel            string              `json:"display_label"`
	Visibility              string              `json:"visibility"`
	IsDefault               bool                `json:"is_default"`
	ContextLimit            int                 `json:"context_limit"`
	OutputLimit             int                 `json:"output_limit"`
	ReasoningEffortVariants []museReasoningTier `json:"reasoning_effort_variants"`
}

type museReasoningTier struct {
	Tier string `json:"tier"`
}

// Private HOME/XDG paths alone do not prevent Muse's native Keychain migration.
// The verified file backend keeps this account's loopback credential in auth.json.
const museKiloCredentialBackend = "file"

func buildMuseKiloProfile(library modelLibrary, catalog []modelInfo, baseURL, localToken string) ([]byte, []byte, error) {
	if err := validateModelLibrary(library); err != nil {
		return nil, nil, err
	}
	if len(library.Models) == 0 {
		return nil, nil, errors.New("Open Models in Kilo Proxy and save a model selection first.")
	}
	u, err := url.Parse(baseURL)
	host, port, splitErr := net.SplitHostPort(uHost(u))
	p, numberErr := strconv.Atoi(port)
	credential, tokenErr := hex.DecodeString(localToken)
	if err != nil || splitErr != nil || numberErr != nil || tokenErr != nil || len(credential) != 32 || p < 1 || p > 65535 || u.Scheme != "http" || host != "127.0.0.1" || u.Host != net.JoinHostPort(host, strconv.Itoa(p)) || (u.Path != "/muse/v1" && u.Path != "/t3-code/muse/v1") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, nil, errors.New("The Muse Code proxy connection is invalid.")
	}
	rows := make([]museKiloCatalogModel, 0, len(library.Models))
	for _, choice := range terminalLibraryChoices(library, catalog) {
		limits, err := contextPolicyForChoice(choice)
		if err != nil {
			return nil, nil, err
		}
		name := choice.DisplayName
		if name == "" {
			name = choice.Model.Name
		}
		if name == "" {
			name = choice.Model.ID
		}
		levels, _ := nativeReasoningFor(choice)
		variants := make([]museReasoningTier, 0, len(levels))
		for _, level := range levels {
			variants = append(variants, museReasoningTier{Tier: level})
		}
		rows = append(rows, museKiloCatalogModel{ModelID: choice.Model.ID, ProviderID: "meta", ProfileID: "tbh", DisplayLabel: name, Visibility: "visible", IsDefault: choice.Model.ID == library.DefaultModel, ContextLimit: limits.ContextWindow, OutputLimit: limits.MaxOutputTokens, ReasoningEffortVariants: variants})
	}
	settings, err := json.MarshalIndent(map[string]any{
		"schema_version": 1, "provider": "meta", "model": library.DefaultModel,
		"endpoint_transport": map[string]string{"base_url": baseURL, "auth": "bearer"},
		"model_catalog":      rows,
	}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	auth, err := json.MarshalIndent(map[string]any{"schema_version": 1, "providers": map[string]any{"meta": map[string]string{"api_key": localToken}}}, "", "  ")
	return append(settings, '\n'), append(auth, '\n'), err
}

func uHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Host
}

func museKiloEnvironment(home string) map[string]string {
	return map[string]string{
		"HOME": home, "USERPROFILE": home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state"),
		"APPDATA": filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"MUSE_NO_AUTO_UPDATE": "1", "TBH_CREDENTIAL_BACKEND": museKiloCredentialBackend,
	}
}

func museKiloStateDirectories(home string) []string {
	return []string{filepath.Join(home, ".config", "muse"), filepath.Join(home, ".local", "share", "muse"), filepath.Join(home, ".local", "state", "muse"), filepath.Join(home, ".cache", "muse"), filepath.Join(home, "AppData", "Roaming", "muse"), filepath.Join(home, "AppData", "Local", "muse")}
}

func museKiloStateSafe(home, root string) bool {
	for _, dir := range museKiloStateDirectories(home) {
		if !safeLaunchDir(dir, root) {
			return false
		}
	}
	return true
}

// Inspect only path identities; never open a normal Muse configuration, token,
// transcript or database while preparing a separate Kilo account.
func museNormalPaths(home string, environment []string, platform string) []string {
	paths := []string{filepath.Join(home, ".config", "muse"), filepath.Join(home, ".local", "share", "muse"), filepath.Join(home, ".local", "state", "muse"), filepath.Join(home, ".cache", "muse")}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" {
			continue
		}
		switch strings.ToUpper(name) {
		case "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA":
			paths = append(paths, filepath.Join(value, "muse"))
		case "MUSE_AUTH_PATH", "MUSE_HOME":
			paths = append(paths, value)
		}
	}
	if platform == "windows" {
		paths = append(paths, filepath.Join(home, "AppData", "Roaming", "muse"), filepath.Join(home, "AppData", "Local", "muse"))
	}
	return paths
}

func museKiloIsolation(home string, normalPaths []string) ([]string, error) {
	private, err := t3CodeCanonicalPath(home)
	if err != nil {
		return nil, errors.New("Cannot resolve the private Muse Code profile.")
	}
	identities := []string{private}
	for _, path := range normalPaths {
		if !packageUpdateAbsolute(path) || strings.ContainsAny(path, "\x00\r\n") {
			return nil, errors.New("Normal Muse Code configuration paths must be absolute before preparing Kilo models.")
		}
		normal, err := t3CodeCanonicalPath(filepath.Clean(path))
		if err != nil {
			return nil, errors.New("Cannot resolve a normal Muse Code configuration directory.")
		}
		if withinT3CodePath(normal, private) || withinT3CodePath(private, normal) {
			return nil, errors.New("The private Muse Code profile must be separate from the normal Meta configuration and data.")
		}
		identities = append(identities, normal)
	}
	return identities, nil
}

func museKiloInheritedEnvironment(environment []string) []string {
	unset := []string{"KILO_LOCAL_API_KEY", "MUSE_AUTH_PATH", "MUSE_HOME", "MUSE_LOGIN", "META_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if (strings.HasPrefix(strings.ToUpper(name), "MUSE_") || strings.HasPrefix(strings.ToUpper(name), "META_")) && !helperContains(unset, name) {
			unset = append(unset, name)
		}
	}
	return unset
}
