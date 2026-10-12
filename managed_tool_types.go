package main

import "context"

const managedToolsStatusEndpoint = "/api/tools/status"
const managedToolsCheckEndpoint = "/api/tools/check"
const managedToolsInstallEndpoint = "/api/tools/install"

// A release is resolved from a fixed vendor catalog, never from HTTP input.
type managedToolRelease struct {
	Client, Version, Channel, URL, SHA256, AssetName, Format string
	Size                                                     int64
}

type managedToolStatus struct {
	Client            string   `json:"client"`
	Name              string   `json:"name"`
	Installed         bool     `json:"installed"`
	InstalledVersion  string   `json:"installedVersion,omitempty"`
	LatestVersion     string   `json:"latestVersion,omitempty"`
	Channel           string   `json:"channel,omitempty"`
	InstalledChannel  string   `json:"installedChannel,omitempty"`
	Channels          []string `json:"channels,omitempty"`
	Path              string   `json:"path,omitempty"`
	Managed           bool     `json:"managed"`
	Method            string   `json:"method,omitempty"`
	Scope             string   `json:"scope,omitempty"`
	CanInstall        bool     `json:"canInstall"`
	CanUpdate         bool     `json:"canUpdate"`
	Busy              bool     `json:"busy"`
	Phase             string   `json:"phase,omitempty"`
	Error             string   `json:"error,omitempty"`
	PlanID            string   `json:"planID,omitempty"`
	CheckedAt         int64    `json:"checkedAt,omitempty"`
	UnsupportedReason string   `json:"unsupportedReason,omitempty"`
	InstallURL        string   `json:"installURL,omitempty"`
}

type managedToolsSnapshot struct {
	Platform string                       `json:"platform"`
	Clients  map[string]managedToolStatus `json:"clients"`
}

type managedToolRecord struct {
	Schema     int    `json:"schema"`
	Client     string `json:"client"`
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	Root       string `json:"root"`
	Executable string `json:"executable"`
	Digest     string `json:"digest"`
}

type managedToolCatalog interface {
	Latest(context.Context, string, string, string, string) (managedToolRelease, error)
}
