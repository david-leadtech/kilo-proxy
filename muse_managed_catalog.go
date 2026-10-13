package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tailscale/hujson"
)

const museManagedChannelURL = "https://api.meta.ai/muse-code/channels/muse-stable"

var museManagedVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)-R([0-9]+)(?:\.([0-9]+))?$`)
var museManagedVersionOutput = regexp.MustCompile(`^Muse Code ([0-9]+\.[0-9]+\.[0-9]+) \(([0-9]+\.[0-9]+\.[0-9]+-R[0-9]+(?:\.[0-9]+)?)\)$`)

func museManagedPlatform(platform, arch string) string {
	system := map[string]string{"darwin": "macos", "linux": "linux", "windows": "windows"}[platform]
	cpu := map[string]string{"amd64": "x86", "arm64": "aarch64"}[arch]
	if system == "" || cpu == "" {
		return ""
	}
	return cpu + "_" + system
}

func museManagedAsset(platform, arch string) string {
	key := museManagedPlatform(platform, arch)
	if key == "" {
		return ""
	}
	name := "muse-" + strings.ReplaceAll(key, "_", "-")
	if platform == "windows" {
		name += ".exe"
	}
	return name
}

func museManagedURL(version, file string) string {
	return "https://lookaside.facebook.com/lookaside/muse/download/?channel=muse&version=" + version + "&file=" + file
}

// Only the release endpoint and its exact query contract belong to Muse's
// installer. A vendor host alone never authorizes unrelated downloads.
func museManagedEndpoint(raw string) (version, file string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "lookaside.facebook.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Path != "/lookaside/muse/download/" {
		return "", "", false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 3 || len(query["channel"]) != 1 || query.Get("channel") != "muse" || len(query["version"]) != 1 || len(query["file"]) != 1 {
		return "", "", false
	}
	version, file = query.Get("version"), query.Get("file")
	return version, file, museManagedVersionPattern.MatchString(version)
}

func museManagedMetadataURL(raw string) bool {
	if raw == museManagedChannelURL {
		return true
	}
	_, file, ok := museManagedEndpoint(raw)
	return ok && file == "manifest.json"
}

func museManagedArtifactURL(raw, expectedVersion, expectedAsset string) bool {
	version, file, ok := museManagedEndpoint(raw)
	if !ok || expectedVersion != "" && version != expectedVersion || expectedAsset != "" && file != expectedAsset {
		return false
	}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if file == museManagedAsset(platform, arch) {
				return true
			}
		}
	}
	return false
}

func museManagedReleaseValid(release managedToolRelease) bool {
	return release.Client == "muse" && release.Channel == "stable" && len(release.Version) <= 128 && museManagedVersionPattern.MatchString(release.Version) && release.Format == "binary" && release.AssetName != "" && release.Size > 0 && release.Size <= 2<<30 && managedToolCatalogSHA256.MatchString(release.SHA256) && museManagedArtifactURL(release.URL, release.Version, release.AssetName)
}

// Muse's R build is part of the installed identity, not a SemVer prerelease.
// Compare every decimal component so R10000 sorts after R9000 and large build
// numbers cannot overflow. The optional revision is zero when absent.
func museManagedCompareVersions(left, right string) (int, bool) {
	l, r := museManagedVersionPattern.FindStringSubmatch(left), museManagedVersionPattern.FindStringSubmatch(right)
	if len(l) != 6 || len(r) != 6 {
		return 0, false
	}
	for index := 1; index < len(l); index++ {
		a, b := strings.TrimLeft(l[index], "0"), strings.TrimLeft(r[index], "0")
		if a == "" {
			a = "0"
		}
		if b == "" {
			b = "0"
		}
		if comparison := compareReleaseNumber(a, b); comparison != 0 {
			return comparison, true
		}
	}
	return 0, true
}

func museManagedDecode(data []byte, target any) error {
	// Standard JSON only, with one unambiguous value for each field. Unknown
	// upstream metadata is harmless; duplicate release identities are not.
	value, err := hujson.Parse(data)
	if err != nil || !json.Valid(data) || !uniqueEditorJSON(value) || json.Unmarshal(data, target) != nil {
		return errors.New("The official Muse Code metadata is invalid or ambiguous.")
	}
	return nil
}

func (c *vendorManagedToolCatalog) museRelease(ctx context.Context, platform, arch, channel string) (managedToolRelease, error) {
	if channel != "stable" || museManagedPlatform(platform, arch) == "" {
		return managedToolRelease{}, errors.New("This Muse Code platform or channel is not supported.")
	}
	data, err := c.metadata(ctx, museManagedChannelURL)
	if err != nil {
		return managedToolRelease{}, err
	}
	var latest struct {
		Channel     string `json:"channel"`
		Version     string `json:"version"`
		ManifestURL string `json:"manifest_url"`
		State       string `json:"state"`
	}
	if err = museManagedDecode(data, &latest); err != nil {
		return managedToolRelease{}, err
	}
	manifestVersion, manifestFile, manifestOK := museManagedEndpoint(latest.ManifestURL)
	if latest.Channel != "muse-stable" || latest.State != "public" || len(latest.Version) > 128 || !museManagedVersionPattern.MatchString(latest.Version) || !manifestOK || manifestVersion != latest.Version || manifestFile != "manifest.json" {
		return managedToolRelease{}, errors.New("No public official Muse Code stable release could be verified.")
	}
	data, err = c.metadata(ctx, latest.ManifestURL)
	if err != nil {
		return managedToolRelease{}, err
	}
	var manifest struct {
		Version   string `json:"version"`
		Algorithm string `json:"checksum_algorithm"`
		Artifacts map[string]struct {
			URL      string `json:"url"`
			Checksum string `json:"checksum"`
			Size     int64  `json:"size"`
		} `json:"artifacts"`
	}
	if err = museManagedDecode(data, &manifest); err != nil {
		return managedToolRelease{}, err
	}
	if manifest.Version != latest.Version || manifest.Algorithm != "sha256" {
		return managedToolRelease{}, errors.New("The Muse Code manifest does not identify the verified release.")
	}
	artifact, found := manifest.Artifacts[museManagedPlatform(platform, arch)]
	asset := museManagedAsset(platform, arch)
	release := managedToolRelease{Client: "muse", Version: latest.Version, Channel: channel, URL: artifact.URL, SHA256: artifact.Checksum, Size: artifact.Size, AssetName: asset, Format: "binary"}
	if !found || !museManagedReleaseValid(release) {
		return managedToolRelease{}, errors.New("The Muse Code release has no verified binary for this operating system and architecture.")
	}
	return release, nil
}

func museManagedProbeVersion(ctx context.Context, executable string) (string, error) {
	output, err := managedToolProbeOutput(ctx, executable)
	if err != nil {
		return "", err
	}
	match := museManagedVersionOutput.FindStringSubmatch(output)
	if len(match) != 3 || !strings.HasPrefix(match[2], match[1]+"-R") {
		return "", errors.New("Cannot recognize the installed Muse Code release version.")
	}
	return match[2], nil
}

func museInstallationPaths(home, platform, localAppData string) []string {
	if platform == "windows" {
		if !packageUpdateAbsolute(localAppData) {
			return nil
		}
		return []string{filepath.Join(localAppData, "Programs", "muse", "muse.cmd")}
	}
	if (platform == "darwin" || platform == "linux") && packageUpdateAbsolute(home) {
		return []string{filepath.Join(home, ".local", "bin", "muse")}
	}
	return nil
}
