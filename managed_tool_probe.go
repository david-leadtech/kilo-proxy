package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var managedToolVersionPattern = regexp.MustCompile(`(?:^|[\s/])v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:[\s(]|$)`)

// Keep the buffer private rather than embedding bytes.Buffer: its promoted
// ReadFrom method would let io.Copy bypass this writer's size limit.
type managedToolOutput struct {
	data     []byte
	limit    int
	exceeded bool
}

func (output *managedToolOutput) Write(data []byte) (int, error) {
	count := len(data)
	available := output.limit - len(output.data)
	if count > available {
		output.exceeded = true
		data = data[:available]
	}
	output.data = append(output.data, data...)
	return count, nil
}
func (output *managedToolOutput) String() string { return string(output.data) }

func managedToolChannel(version, executable string) string {
	label := strings.ToLower(version + " " + filepath.Base(executable))
	for _, channel := range []string{"nightly", "alpha", "beta", "preview"} {
		if strings.Contains(label, channel) {
			return channel
		}
	}
	return "stable"
}

func managedToolVersionLabel(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !managedToolLabelPattern.MatchString(value) {
		return "", errors.New("Cannot verify the installed tool version.")
	}
	return value, nil
}

// Version discovery starts only CLIs with their fixed --version argument. All
// credential/configuration locations are disposable and no provider variables,
// shell startup files, Node hooks or proxy settings enter the child process.
func managedToolProbeOutput(ctx context.Context, binary string) (string, error) {
	if !packageUpdateAbsolute(binary) || !launchExecutable(binary) {
		return "", errors.New("Cannot inspect the installed CLI executable.")
	}
	scratch, err := os.MkdirTemp("", "kilo-tool-version-")
	if err != nil {
		return "", errors.New("Cannot create a private version-check directory.")
	}
	defer os.RemoveAll(scratch)
	scratch, err = filepath.EvalSymlinks(scratch)
	if err != nil {
		return "", errors.New("Cannot resolve the private version-check directory.")
	}
	path := os.Getenv("PATH")
	if !validPackageUpdateRestartPath(path) {
		path = packageUpdateManagerPath
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	environment := []string{
		"HOME=" + scratch, "USERPROFILE=" + scratch, "PATH=" + path, "LANG=C", "LC_ALL=C",
		"CODEX_HOME=" + scratch, "CLAUDE_CONFIG_DIR=" + scratch, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + scratch,
		"XDG_CONFIG_HOME=" + scratch, "XDG_DATA_HOME=" + scratch, "XDG_STATE_HOME=" + scratch, "XDG_CACHE_HOME=" + scratch,
		"TMPDIR=" + scratch, "TMP=" + scratch, "TEMP=" + scratch, "NO_COLOR=1", "CI=1", "DO_NOT_TRACK=1",
		"MUSE_NO_AUTO_UPDATE=1", "MUSE_LOGIN=0", "MUSE_AUTH_PATH=" + filepath.Join(scratch, "muse-auth.json"),
		"TBH_CREDENTIAL_BACKEND=" + museKiloCredentialBackend,
	}
	if runtime.GOOS == "windows" {
		// Native CLIs may require Windows' loader directory. No other ambient state
		// is inherited, and Windows never gets managed installation actions.
		if system := os.Getenv("SystemRoot"); filepath.IsAbs(system) && !strings.ContainsAny(system, "\x00\r\n") {
			environment = append(environment, "SystemRoot="+system)
		}
	}
	command, cleanup, err := managedToolProbeCommand(probe, binary, environment)
	if err != nil {
		return "", err
	}
	defer cleanup()
	command.Dir = scratch
	command.WaitDelay = time.Second
	output := &managedToolOutput{limit: 16 << 10}
	command.Stdout, command.Stderr = output, io.Discard
	if command.Run() != nil || output.exceeded || probe.Err() != nil {
		return "", errors.New("The installed CLI did not return a bounded version response.")
	}
	return strings.TrimSpace(output.String()), nil
}

func managedToolProbeVersion(ctx context.Context, binary string) (string, error) {
	output, err := managedToolProbeOutput(ctx, binary)
	if err != nil {
		return "", err
	}
	match := managedToolVersionPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", errors.New("Cannot recognize the installed CLI version.")
	}
	return managedToolVersionLabel(match[1])
}

func managedToolZedReleaseVersion(version, channel string) string {
	// Zed's preview tag adds -pre while its Cargo/CFBundle version stays
	// numeric. The explicit CLI/bundle identity, not the version number,
	// establishes which channel was installed.
	if channel == "preview" && !strings.ContainsAny(version, "-+") {
		return version + "-pre"
	}
	return version
}

func managedToolZedProbeVersion(ctx context.Context, binary string) (version, channel string, err error) {
	output, err := managedToolProbeOutput(ctx, binary)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 || fields[0] != "Zed" {
		return "", "", errors.New("Cannot recognize the installed Zed version and channel.")
	}
	index := 1
	channel = "stable"
	switch fields[index] {
	case "preview", "nightly", "dev":
		channel = fields[index]
		index++
	}
	if index >= len(fields) {
		return "", "", errors.New("Cannot recognize the installed Zed version and channel.")
	}
	match := managedToolVersionPattern.FindStringSubmatch(fields[index])
	if len(match) != 2 || strings.TrimPrefix(fields[index], "v") != match[1] {
		return "", "", errors.New("Cannot recognize the installed Zed version and channel.")
	}
	version, err = managedToolVersionLabel(match[1])
	if err != nil {
		return "", "", err
	}
	return managedToolZedReleaseVersion(version, channel), channel, nil
}

func managedToolBundleInfo(bundle string) (map[string]string, error) {
	if !strings.HasSuffix(bundle, ".app") || !packageUpdateAbsolute(bundle) {
		return nil, errors.New("Invalid application bundle.")
	}
	data, err := readOpenDesignShimFile(filepath.Join(bundle, "Contents", "Info.plist"), 1<<20)
	if err != nil {
		return nil, errors.New("Cannot inspect the application bundle metadata.")
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if runtime.GOOS != "darwin" {
			return nil, errors.New("Binary application metadata requires macOS.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "xml1", "-o", "-", "--", filepath.Join(bundle, "Contents", "Info.plist"))
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
		output := &managedToolOutput{limit: 1 << 20}
		command.Stdout, command.Stderr = output, io.Discard
		command.WaitDelay = time.Second
		if command.Run() != nil || output.exceeded {
			return nil, errors.New("Cannot decode the application bundle metadata.")
		}
		data = []byte(output.String())
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	result := map[string]string{}
	key := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("Invalid application bundle metadata.")
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "key":
			if decoder.DecodeElement(&key, &start) != nil {
				return nil, errors.New("Invalid application bundle metadata.")
			}
		case "string":
			var value string
			if decoder.DecodeElement(&value, &start) != nil {
				return nil, errors.New("Invalid application bundle metadata.")
			}
			if key != "" {
				result[key] = value
			}
			key = ""
		}
	}
	return result, nil
}

func managedToolBundleExecutable(bundle string) string {
	meta, err := managedToolBundleInfo(bundle)
	name := meta["CFBundleExecutable"]
	if err != nil || name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\x00\r\n") {
		return ""
	}
	binary := filepath.Join(bundle, "Contents", "MacOS", name)
	if !launchExecutable(binary) {
		return ""
	}
	return binary
}

func managedToolPackagedVersion(executable, platform string) (string, error) {
	if strings.HasSuffix(executable, ".app") {
		meta, err := managedToolBundleInfo(executable)
		if err != nil {
			return "", err
		}
		if version := meta["CFBundleShortVersionString"]; version != "" {
			return managedToolVersionLabel(version)
		}
		return managedToolVersionLabel(meta["CFBundleVersion"])
	}
	for _, resources := range []string{"resources", "Resources"} {
		archive, err := t3CodeOpenASAR(filepath.Join(filepath.Dir(executable), resources, "app.asar"))
		if err != nil {
			continue
		}
		data, readErr := archive.read("package.json", 64<<10)
		archive.file.Close()
		var meta struct{ Version string }
		if readErr == nil && json.Unmarshal(data, &meta) == nil {
			return managedToolVersionLabel(meta.Version)
		}
	}
	return "", errors.New("No bounded application version metadata was found.")
}

func managedToolInstalledVersion(ctx context.Context, client, executable, platform, arch string) (version, channel string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !managedToolClientValid(client) || !packageUpdateAbsolute(executable) {
		return "", "", errors.New("Cannot inspect this installed tool.")
	}
	if err = ctx.Err(); err != nil {
		return "", "", errors.New("The tool version check was cancelled.")
	}
	switch client {
	case "muse":
		version, err = museManagedProbeVersion(ctx, executable)
	case "codex-cli", "claude", "opencode", "omp":
		version, err = managedToolProbeVersion(ctx, executable)
	case "zed":
		if platform == "linux" {
			version, channel, err = managedToolZedProbeVersion(ctx, executable)
		} else {
			version, err = managedToolPackagedVersion(executable, platform)
			if err == nil {
				channel = managedToolChannel(version, executable)
				metadata, metadataErr := managedToolBundleInfo(executable)
				if metadataErr == nil {
					switch metadata["CFBundleIdentifier"] {
					case "dev.zed.Zed-Preview":
						channel = "preview"
					case "dev.zed.Zed":
						channel = "stable"
					}
				}
				version = managedToolZedReleaseVersion(version, channel)
			}
		}
	case "t3-code":
		version, err = t3CodeVersion(executable, platform)
		if err == nil {
			// T3 Code's product branding can include "(Alpha)" or "(Beta)"
			// even when the selected release is stable. Its packaged version,
			// which is checked against the selected release before installation,
			// is the channel identity; the app bundle name is not.
			channel = managedToolReleaseChannel(version)
		}
	case "synara":
		var meta synaraPackageInfo
		meta, err = synaraPackageMetadata(executable, platform)
		if err == nil {
			version, err = managedToolVersionLabel(meta.Version)
			channel = synaraPackageChannel(meta)
		}
	case "open-design":
		root := filepath.Dir(executable)
		if strings.HasSuffix(executable, ".app") {
			root = filepath.Join(executable, "Contents")
		}
		data, readErr := readOpenDesignShimFile(filepath.Join(root, "Resources", "open-design-config.json"), 64<<10)
		if readErr != nil {
			data, readErr = readOpenDesignShimFile(filepath.Join(root, "resources", "open-design-config.json"), 64<<10)
		}
		var metadata struct {
			Version string `json:"appVersion"`
		}
		if readErr == nil && json.Unmarshal(data, &metadata) == nil {
			version, err = managedToolVersionLabel(metadata.Version)
		} else {
			err = errors.New("Cannot inspect Open Design package metadata.")
		}
	default:
		version, err = managedToolPackagedVersion(executable, platform)
	}
	if err != nil {
		return "", channel, err
	}
	if channel == "" && client != "claude" && client != "t3-code" {
		channel = managedToolChannel(version, executable)
	}
	// A Claude release number cannot establish whether the user selected the
	// vendor's stable or latest update track. Managed records carry that choice.
	return version, channel, nil
}

func (a *app) installedClaude() claudeCapabilities {
	path, err := a.launchRuntime().resolve("claude", "")
	if err != nil {
		return claudeCapabilities{}
	}
	version, _, err := managedToolInstalledVersion(context.Background(), "claude", path, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return claudeCapabilities{}
	}
	return claudeCaps(version)
}
