package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Only the metadata transport is replaceable, for offline tests. Vendor roots,
// repository names, asset names and channels never come from the HTTP caller.
type vendorManagedToolCatalog struct {
	httpClient *http.Client
}

const managedToolMetadataLimit = 16 << 20

var managedToolCatalogVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)
var managedToolCatalogSHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

func managedToolChannels(client string) []string {
	switch client {
	case "t3-code":
		return []string{"stable", "nightly"}
	case "synara":
		return []string{"stable", "beta"}
	case "claude":
		return []string{"stable", "latest"}
	case "zed":
		return []string{"stable", "preview"}
	default:
		return []string{"stable"}
	}
}

func managedToolSupport(client, platform, arch string) (reason, installURL string) {
	links := map[string]string{
		"codex":          "https://learn.chatgpt.com/docs/app",
		"codex-cli":      "https://github.com/openai/codex#installation",
		"claude":         "https://code.claude.com/docs/en/setup",
		"claude-desktop": "https://claude.ai/download",
		"opencode":       "https://opencode.ai/docs/",
		"omp":            "https://github.com/can1357/oh-my-pi#installation",
		"open-design":    "https://github.com/nexu-io/open-design#installation",
		"openmausbot":    "https://github.com/milind-soni/OpenMausBot/releases",
		"t3-code":        "https://github.com/pingdotgg/t3code/releases",
		"synara":         "https://www.trysynara.com/docs/getting-started/installation",
		"zed":            "https://zed.dev/docs/installation",
		"xcode-chat":     "https://apps.apple.com/app/xcode/id497799835",
		"xcode-codex":    "https://apps.apple.com/app/xcode/id497799835",
		"xcode-claude":   "https://apps.apple.com/app/xcode/id497799835",
	}
	installURL, known := links[client]
	if !known {
		return "This tool is not in the managed installation catalog.", ""
	}
	if platform == "windows" {
		return "Install and update this tool manually on Windows.", installURL
	}
	if platform != "darwin" && platform != "linux" {
		return "Managed installation is available on macOS and Linux.", installURL
	}
	if strings.HasPrefix(client, "xcode-") {
		return "Install or update Xcode using the Mac App Store or Apple Developer Downloads.", installURL
	}
	if arch != "amd64" && arch != "arm64" {
		return "The vendor does not publish a supported package for this architecture.", installURL
	}
	if platform == "linux" {
		switch client {
		case "open-design":
			return "Open Design does not publish a prebuilt Linux desktop package. Follow its source installation guide.", installURL
		case "openmausbot", "synara":
			if arch == "arm64" {
				return "The vendor does not publish a Linux ARM64 desktop package.", installURL
			}
		}
	}
	return "", installURL
}

func (c *vendorManagedToolCatalog) Latest(ctx context.Context, client, platform, arch, channel string) (managedToolRelease, error) {
	if reason, _ := managedToolSupport(client, platform, arch); reason != "" {
		return managedToolRelease{}, errors.New(reason)
	}
	if channel == "" {
		channel = "stable"
		if client == "synara" {
			channel = "beta"
		}
	}
	validChannel := false
	for _, allowed := range managedToolChannels(client) {
		validChannel = validChannel || channel == allowed
	}
	if !validChannel {
		return managedToolRelease{}, errors.New("The selected tool channel is not supported.")
	}
	switch client {
	case "codex", "claude-desktop":
		if platform == "linux" {
			return c.debianRelease(ctx, client, arch, channel)
		}
		cask := "chatgpt"
		if client == "claude-desktop" {
			cask = "claude"
		}
		return c.caskRelease(ctx, client, cask, platform, arch, channel)
	case "claude":
		cask := "claude-code"
		if channel == "latest" {
			cask = "claude-code@latest"
		}
		return c.caskRelease(ctx, client, cask, platform, arch, channel)
	default:
		return c.githubRelease(ctx, client, platform, arch, channel)
	}
}

func managedToolMetadataURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Host {
	case "api.github.com", "formulae.brew.sh", "downloads.claude.ai", "persistent.oaistatic.com":
		return true
	default:
		return false
	}
}

func (c *vendorManagedToolCatalog) metadata(ctx context.Context, address string) ([]byte, error) {
	if !managedToolMetadataURL(address) {
		return nil, errors.New("The vendor metadata address is not allowed.")
	}
	transport := http.Client{Timeout: 25 * time.Second}
	if c.httpClient != nil {
		transport = *c.httpClient
		if transport.Timeout == 0 {
			transport.Timeout = 25 * time.Second
		}
	}
	transport.CheckRedirect = func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 4 || !managedToolMetadataURL(req.URL.String()) {
			return errors.New("The vendor metadata redirect is not allowed.")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("Cannot create the vendor metadata request.")
	}
	req.Header.Set("User-Agent", "Kilo-Proxy-Managed-Tools")
	req.Header.Set("Accept", "application/json, text/plain, application/octet-stream")
	response, err := transport.Do(req)
	if err != nil {
		return nil, errors.New("Cannot contact the official tool metadata service.")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("The official tool metadata service returned HTTP %d.", response.StatusCode)
	}
	if response.ContentLength > managedToolMetadataLimit {
		return nil, errors.New("The official tool metadata response is too large.")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, managedToolMetadataLimit+1))
	if err != nil || len(data) > managedToolMetadataLimit {
		return nil, errors.New("Cannot read bounded official tool metadata.")
	}
	return data, nil
}

type managedToolGitHubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name   string `json:"name"`
		URL    string `json:"browser_download_url"`
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"assets"`
}

func managedToolGitHubRepository(client string) string {
	switch client {
	case "codex-cli":
		return "openai/codex"
	case "opencode":
		return "anomalyco/opencode"
	case "omp":
		return "can1357/oh-my-pi"
	case "open-design":
		return "nexu-io/open-design"
	case "openmausbot":
		return "milind-soni/OpenMausBot"
	case "t3-code":
		return "pingdotgg/t3code"
	case "synara":
		return "Emanuele-web04/synara"
	case "zed":
		return "zed-industries/zed"
	default:
		return ""
	}
}

func managedToolGitHubVersion(client, tag string) string {
	switch client {
	case "codex-cli":
		return strings.TrimPrefix(tag, "rust-v")
	case "open-design":
		return strings.TrimPrefix(tag, "open-design-v")
	default:
		return strings.TrimPrefix(tag, "v")
	}
}

func managedToolReleaseChannel(version string) string {
	_, suffix, found := strings.Cut(version, "-")
	if !found {
		return "stable"
	}
	first := strings.FieldsFunc(suffix, func(r rune) bool { return r == '.' || r == '-' })
	if len(first) == 0 {
		return ""
	}
	if first[0] == "pre" {
		return "preview"
	}
	return first[0]
}

func (c *vendorManagedToolCatalog) githubRelease(ctx context.Context, client, platform, arch, channel string) (managedToolRelease, error) {
	repository := managedToolGitHubRepository(client)
	if repository == "" {
		return managedToolRelease{}, errors.New("This tool has no fixed GitHub release feed.")
	}
	var selected managedToolGitHubRelease
	if channel == "stable" {
		data, err := c.metadata(ctx, "https://api.github.com/repos/"+repository+"/releases/latest")
		if err != nil {
			return managedToolRelease{}, err
		}
		if json.Unmarshal(data, &selected) != nil {
			return managedToolRelease{}, errors.New("Invalid official GitHub release metadata.")
		}
	} else {
		// Stable is always resolved independently; a stream of nightly releases
		// must never hide it or cause an implicit channel switch. The bounded
		// scan returns an explicit error when no release of a chosen channel exists.
		for page := 1; page <= 3 && selected.Tag == ""; page++ {
			address := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100&page=%d", repository, page)
			data, err := c.metadata(ctx, address)
			if err != nil {
				return managedToolRelease{}, err
			}
			var releases []managedToolGitHubRelease
			if json.Unmarshal(data, &releases) != nil {
				return managedToolRelease{}, errors.New("Invalid official GitHub channel metadata.")
			}
			for _, candidate := range releases {
				version := managedToolGitHubVersion(client, candidate.Tag)
				if !candidate.Draft && candidate.Prerelease && managedToolCatalogVersion.MatchString(version) && managedToolReleaseChannel(version) == channel {
					selected = candidate
					break
				}
			}
			if len(releases) < 100 {
				break
			}
		}
	}
	version := managedToolGitHubVersion(client, selected.Tag)
	if selected.Draft || !managedToolCatalogVersion.MatchString(version) || managedToolReleaseChannel(version) != channel || selected.Prerelease != (channel != "stable") {
		return managedToolRelease{}, errors.New("No published official release is available for the selected channel.")
	}
	if client == "opencode" && !strings.HasPrefix(version, "1.") {
		return managedToolRelease{}, errors.New("This Kilo Proxy adapter requires OpenCode 1.x. A newer major version must be reviewed before installation.")
	}
	name, format := managedToolGitHubAsset(client, platform, arch, channel, version)
	if name == "" {
		return managedToolRelease{}, errors.New("No official package is available for this operating system and architecture.")
	}
	var result managedToolRelease
	for _, asset := range selected.Assets {
		if asset.Name != name {
			continue
		}
		if result.URL != "" {
			return managedToolRelease{}, errors.New("The official release contains ambiguous matching packages.")
		}
		checksum := strings.TrimPrefix(asset.Digest, "sha256:")
		expected := "https://github.com/" + repository + "/releases/download/" + selected.Tag + "/" + name
		if !strings.HasPrefix(asset.Digest, "sha256:") || !managedToolCatalogSHA256.MatchString(checksum) || asset.URL != expected || asset.Size <= 0 || asset.Size > 2<<30 {
			return managedToolRelease{}, errors.New("The official package has no valid SHA256, size or fixed vendor download address.")
		}
		result = managedToolRelease{Client: client, Version: version, Channel: channel, URL: asset.URL, SHA256: checksum, Size: asset.Size, AssetName: name, Format: format}
	}
	if result.URL == "" {
		return managedToolRelease{}, errors.New("The selected official release has no package for this operating system and architecture.")
	}
	return result, nil
}

func managedToolGitHubAsset(client, platform, arch, channel, version string) (name, format string) {
	x64 := "x64"
	if arch == "arm64" {
		x64 = "arm64"
	}
	gnu := "x86_64"
	if arch == "arm64" {
		gnu = "aarch64"
	}
	switch client {
	case "codex-cli":
		triple := gnu + "-apple-darwin"
		if platform == "linux" {
			triple = gnu + "-unknown-linux-musl"
		}
		return "codex-package-" + triple + ".tar.gz", "tar.gz"
	case "opencode":
		if platform == "darwin" {
			cpu := x64
			if arch == "amd64" {
				cpu += "-baseline"
			}
			return "opencode-darwin-" + cpu + ".zip", "zip"
		}
		if arch == "amd64" {
			x64 += "-baseline"
		}
		return "opencode-linux-" + x64 + ".tar.gz", "tar.gz"
	case "omp":
		return "omp-" + platform + "-" + x64, "binary"
	case "open-design":
		return "open-design-" + version + "-mac-" + x64 + ".dmg", "dmg"
	case "openmausbot":
		if platform == "darwin" {
			return "OpenMausBot-" + version + "-" + x64 + ".zip", "zip"
		}
		return "OpenMausBot-" + version + "-x86_64.AppImage", "appimage"
	case "t3-code":
		if platform == "darwin" {
			return "T3-Code-" + version + "-" + x64 + ".zip", "zip"
		}
		linuxArch := "x86_64"
		if arch == "arm64" {
			linuxArch = "arm64"
		}
		return "T3-Code-" + version + "-" + linuxArch + ".AppImage", "appimage"
	case "synara":
		prefix := "Synara-"
		if channel == "beta" {
			prefix = "Synara-Beta-"
		}
		if platform == "darwin" {
			return prefix + version + "-" + x64 + ".zip", "zip"
		}
		return prefix + version + "-x86_64.AppImage", "appimage"
	case "zed":
		// Preview and stable publish the same asset names in separate releases.
		if platform == "darwin" {
			return "Zed-" + gnu + ".dmg", "dmg"
		}
		return "zed-linux-" + gnu + ".tar.gz", "tar.gz"
	default:
		return "", ""
	}
}

type managedToolCaskArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

func (c *vendorManagedToolCatalog) caskRelease(ctx context.Context, client, cask, platform, arch, channel string) (managedToolRelease, error) {
	data, err := c.metadata(ctx, "https://formulae.brew.sh/api/cask/"+cask+".json")
	if err != nil {
		return managedToolRelease{}, err
	}
	var metadata struct {
		managedToolCaskArtifact
		Version    string                             `json:"version"`
		Variations map[string]managedToolCaskArtifact `json:"variations"`
	}
	if json.Unmarshal(data, &metadata) != nil {
		return managedToolRelease{}, errors.New("Invalid official Homebrew package metadata.")
	}
	version, _, _ := strings.Cut(metadata.Version, ",")
	if !managedToolCatalogVersion.MatchString(version) || managedToolReleaseChannel(version) != "stable" {
		return managedToolRelease{}, errors.New("The official native package has an invalid version.")
	}
	artifact := metadata.managedToolCaskArtifact
	if platform == "linux" {
		key := "x86_64_linux"
		if arch == "arm64" {
			key = "arm64_linux"
		}
		var found bool
		artifact, found = metadata.Variations[key]
		if !found {
			return managedToolRelease{}, errors.New("The official package metadata has no Linux package for this architecture.")
		}
	} else if arch == "amd64" && client != "claude-desktop" {
		found := false
		// Homebrew's default macOS payload is ARM64. Intel variations are
		// keyed by OS release; accept only an unambiguous x64 native payload.
		for variationName, variation := range metadata.Variations {
			// Homebrew may emit inherited macOS URLs in Linux variations with
			// null digests. Those are not candidates for a macOS package.
			if strings.HasSuffix(variationName, "_linux") {
				continue
			}
			if !strings.Contains(variation.URL, "/darwin-x64/") && !strings.Contains(variation.URL, "-darwin-x64-") {
				continue
			}
			if found && variation != artifact {
				return managedToolRelease{}, errors.New("The official package metadata contains ambiguous Intel packages.")
			}
			artifact, found = variation, true
		}
		if !found {
			return managedToolRelease{}, errors.New("The official package metadata has no Intel macOS package.")
		}
	}
	u, parseErr := url.Parse(artifact.URL)
	if parseErr != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || !managedToolCatalogSHA256.MatchString(artifact.SHA256) {
		return managedToolRelease{}, errors.New("The official native package has no valid SHA256 or vendor address.")
	}
	format := "zip"
	switch client {
	case "codex":
		cpu := "arm64"
		if arch == "amd64" {
			cpu = "x64"
		}
		if u.Host != "persistent.oaistatic.com" || u.Path != "/codex-app-prod/ChatGPT-darwin-"+cpu+"-"+version+".zip" {
			return managedToolRelease{}, errors.New("The Codex desktop download does not match the official native package.")
		}
	case "claude-desktop":
		if u.Host != "downloads.claude.ai" || !strings.HasPrefix(u.Path, "/releases/darwin/universal/"+version+"/Claude-") || !strings.HasSuffix(u.Path, ".zip") {
			return managedToolRelease{}, errors.New("The Claude desktop download does not match the official universal package.")
		}
	case "claude":
		cpu := "arm64"
		if arch == "amd64" {
			cpu = "x64"
		}
		if u.Host != "downloads.claude.ai" || u.Path != "/claude-code-releases/"+version+"/"+platform+"-"+cpu+"/claude" {
			return managedToolRelease{}, errors.New("The Claude Code download does not match the official native package.")
		}
		format = "binary"
	default:
		return managedToolRelease{}, errors.New("Unknown fixed native package feed.")
	}
	return managedToolRelease{Client: client, Version: version, Channel: channel, URL: artifact.URL, SHA256: artifact.SHA256, AssetName: strings.TrimPrefix(u.Path[strings.LastIndex(u.Path, "/"):], "/"), Format: format}, nil
}

func (c *vendorManagedToolCatalog) debianRelease(ctx context.Context, client, arch, channel string) (managedToolRelease, error) {
	packageName := "chatgpt"
	base := "https://persistent.oaistatic.com/codex-app-prod/linux/deb/"
	gzipped := true
	if client == "claude-desktop" {
		packageName = "claude-desktop"
		base = "https://downloads.claude.ai/claude-desktop/apt/stable/"
		gzipped = false
	}
	index := base + "dists/stable/main/binary-" + arch + "/Packages"
	if gzipped {
		index += ".gz"
	}
	data, err := c.metadata(ctx, index)
	if err != nil {
		return managedToolRelease{}, err
	}
	if gzipped {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return managedToolRelease{}, errors.New("The official package index is not valid gzip.")
		}
		defer reader.Close()
		data, err = io.ReadAll(io.LimitReader(reader, managedToolMetadataLimit+1))
		if err != nil || len(data) > managedToolMetadataLimit {
			return managedToolRelease{}, errors.New("The official package index exceeds the metadata limit.")
		}
	}
	return managedToolDebianIndex(data, client, packageName, base, arch, channel)
}

func managedToolDebianIndex(data []byte, client, packageName, base, arch, channel string) (managedToolRelease, error) {
	var newest managedToolRelease
	for _, paragraph := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		malformed := false
		for _, line := range strings.Split(paragraph, "\n") {
			if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				malformed = true
				break
			}
			if _, exists := fields[key]; exists {
				malformed = true
				break
			}
			fields[key] = strings.TrimSpace(value)
		}
		if fields["Package"] != packageName || fields["Architecture"] != arch {
			continue
		}
		version := fields["Version"]
		if malformed || !managedToolCatalogVersion.MatchString(version) || managedToolReleaseChannel(version) != "stable" {
			return managedToolRelease{}, errors.New("The official package index has invalid package metadata.")
		}
		name := packageName + "_" + version + "_" + arch + ".deb"
		expectedFilename := "pool/main/" + packageName[:1] + "/" + packageName + "/" + name
		size, sizeErr := strconv.ParseInt(fields["Size"], 10, 64)
		if fields["Filename"] != expectedFilename || !managedToolCatalogSHA256.MatchString(fields["SHA256"]) || sizeErr != nil || size <= 0 || size > 2<<30 {
			return managedToolRelease{}, errors.New("The official package index has no valid SHA256, size or fixed package path.")
		}
		candidate := managedToolRelease{Client: client, Version: version, Channel: channel, URL: base + expectedFilename, SHA256: fields["SHA256"], Size: size, AssetName: name, Format: "deb"}
		comparison := managedToolCompareNumericVersion(version, newest.Version)
		if newest.URL != "" && comparison == 0 && candidate != newest {
			return managedToolRelease{}, errors.New("The official package index has conflicting entries for one version.")
		}
		if newest.URL == "" || comparison > 0 {
			newest = candidate
		}
	}
	if newest.URL == "" {
		return managedToolRelease{}, errors.New("The official package index has no package for this architecture.")
	}
	return newest, nil
}

func managedToolCompareNumericVersion(left, right string) int {
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < 3; index++ {
		l, r := "0", "0"
		if index < len(leftParts) {
			l = strings.TrimLeft(leftParts[index], "0")
			if l == "" {
				l = "0"
			}
		}
		if index < len(rightParts) {
			r = strings.TrimLeft(rightParts[index], "0")
			if r == "" {
				r = "0"
			}
		}
		if len(l) > len(r) {
			return 1
		}
		if len(l) < len(r) {
			return -1
		}
		if l > r {
			return 1
		}
		if l < r {
			return -1
		}
	}
	return 0
}
