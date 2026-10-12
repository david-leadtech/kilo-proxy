package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type managedToolCatalogTransport func(*http.Request) (*http.Response, error)

func (f managedToolCatalogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

const managedToolTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func managedToolTestCatalog(t *testing.T, responses map[string][]byte) *vendorManagedToolCatalog {
	t.Helper()
	return &vendorManagedToolCatalog{httpClient: &http.Client{Transport: managedToolCatalogTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected metadata request: %s", r.Method)
		}
		data, ok := responses[r.URL.String()]
		if !ok {
			t.Fatalf("unexpected metadata address: %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: http.Header{}, ContentLength: int64(len(data)), Request: r}, nil
	})}}
}

func managedToolTestGitHub(t *testing.T, client, platform, arch, channel, version string) (managedToolGitHubRelease, string) {
	t.Helper()
	tag := "v" + version
	if client == "codex-cli" {
		tag = "rust-v" + version
	}
	if client == "open-design" {
		tag = "open-design-v" + version
	}
	name, format := managedToolGitHubAsset(client, platform, arch, channel, version)
	var release managedToolGitHubRelease
	release.Tag, release.Prerelease = tag, channel != "stable"
	data, _ := json.Marshal(map[string]any{"tag_name": tag, "prerelease": release.Prerelease, "assets": []any{map[string]any{
		"name": name, "browser_download_url": "https://github.com/" + managedToolGitHubRepository(client) + "/releases/download/" + tag + "/" + name, "digest": "sha256:" + managedToolTestDigest, "size": 12345,
	}}})
	if err := json.Unmarshal(data, &release); err != nil {
		t.Fatal(err)
	}
	return release, format
}

func TestManagedToolCatalogGitHubMatrix(t *testing.T) {
	cases := []struct{ client, platform, arch, channel, version, asset, format string }{
		{"codex-cli", "darwin", "arm64", "stable", "0.162.1", "codex-package-aarch64-apple-darwin.tar.gz", "tar.gz"},
		{"codex-cli", "darwin", "amd64", "stable", "0.162.1", "codex-package-x86_64-apple-darwin.tar.gz", "tar.gz"},
		{"codex-cli", "linux", "arm64", "stable", "0.162.1", "codex-package-aarch64-unknown-linux-musl.tar.gz", "tar.gz"},
		{"codex-cli", "linux", "amd64", "stable", "0.162.1", "codex-package-x86_64-unknown-linux-musl.tar.gz", "tar.gz"},
		{"opencode", "darwin", "amd64", "stable", "1.19.0", "opencode-darwin-x64-baseline.zip", "zip"},
		{"opencode", "darwin", "arm64", "stable", "1.19.0", "opencode-darwin-arm64.zip", "zip"},
		{"opencode", "linux", "amd64", "stable", "1.19.0", "opencode-linux-x64-baseline.tar.gz", "tar.gz"},
		{"opencode", "linux", "arm64", "stable", "1.19.0", "opencode-linux-arm64.tar.gz", "tar.gz"},
		{"omp", "darwin", "amd64", "stable", "18.10.2", "omp-darwin-x64", "binary"},
		{"omp", "linux", "arm64", "stable", "18.10.2", "omp-linux-arm64", "binary"},
		{"open-design", "darwin", "arm64", "stable", "0.24.1", "open-design-0.24.1-mac-arm64.dmg", "dmg"},
		{"open-design", "darwin", "amd64", "stable", "0.24.1", "open-design-0.24.1-mac-x64.dmg", "dmg"},
		{"openmausbot", "darwin", "amd64", "stable", "0.1.107", "OpenMausBot-0.1.107-x64.zip", "zip"},
		{"openmausbot", "linux", "amd64", "stable", "0.1.107", "OpenMausBot-0.1.107-x86_64.AppImage", "appimage"},
		{"t3-code", "darwin", "arm64", "stable", "0.0.45", "T3-Code-0.0.45-arm64.zip", "zip"},
		{"t3-code", "linux", "arm64", "nightly", "0.0.46-nightly.20261012.3021", "T3-Code-0.0.46-nightly.20261012.3021-arm64.AppImage", "appimage"},
		{"t3-code", "linux", "amd64", "nightly", "0.0.46-nightly.20261012.3021", "T3-Code-0.0.46-nightly.20261012.3021-x86_64.AppImage", "appimage"},
		{"synara", "darwin", "arm64", "stable", "1.0.3", "Synara-1.0.3-arm64.zip", "zip"},
		{"synara", "darwin", "amd64", "beta", "1.0.2-beta.1", "Synara-Beta-1.0.2-beta.1-x64.zip", "zip"},
		{"synara", "linux", "amd64", "beta", "1.0.2-beta.1", "Synara-Beta-1.0.2-beta.1-x86_64.AppImage", "appimage"},
		{"zed", "darwin", "arm64", "preview", "1.24.1-pre", "Zed-aarch64.dmg", "dmg"},
		{"zed", "linux", "amd64", "stable", "1.23.2", "zed-linux-x86_64.tar.gz", "tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.client+"/"+tc.platform+"/"+tc.arch+"/"+tc.channel, func(t *testing.T) {
			release, _ := managedToolTestGitHub(t, tc.client, tc.platform, tc.arch, tc.channel, tc.version)
			// Keep expected names independent of the resolver helper.
			release.Assets[0].Name = tc.asset
			release.Assets[0].URL = "https://github.com/" + managedToolGitHubRepository(tc.client) + "/releases/download/" + release.Tag + "/" + tc.asset
			address := "https://api.github.com/repos/" + managedToolGitHubRepository(tc.client) + "/releases/latest"
			var payload any = release
			if tc.channel != "stable" {
				address = "https://api.github.com/repos/" + managedToolGitHubRepository(tc.client) + "/releases?per_page=100&page=1"
				payload = []managedToolGitHubRelease{release}
			}
			data, _ := json.Marshal(payload)
			got, err := managedToolTestCatalog(t, map[string][]byte{address: data}).Latest(context.Background(), tc.client, tc.platform, tc.arch, tc.channel)
			if err != nil {
				t.Fatal(err)
			}
			if got.Client != tc.client || got.Version != tc.version || got.Channel != tc.channel || got.AssetName != tc.asset || got.Format != tc.format || got.SHA256 != managedToolTestDigest || got.Size != 12345 {
				t.Fatalf("wrong release: %+v", got)
			}
		})
	}
}

func TestManagedToolCatalogRejectsUntrustedGitHubMetadata(t *testing.T) {
	cases := map[string]func(*managedToolGitHubRelease){
		"missing digest":         func(r *managedToolGitHubRelease) { r.Assets[0].Digest = "" },
		"wrong digest algorithm": func(r *managedToolGitHubRelease) { r.Assets[0].Digest = "sha512:" + managedToolTestDigest },
		"bad digest":             func(r *managedToolGitHubRelease) { r.Assets[0].Digest = "sha256:" + strings.Repeat("z", 64) },
		"wrong host":             func(r *managedToolGitHubRelease) { r.Assets[0].URL = "https://example.com/tool.zip" },
		"wrong repository": func(r *managedToolGitHubRelease) {
			r.Assets[0].URL = strings.Replace(r.Assets[0].URL, "pingdotgg/t3code", "other/t3code", 1)
		},
		"wrong tag": func(r *managedToolGitHubRelease) {
			r.Assets[0].URL = strings.Replace(r.Assets[0].URL, "v0.0.45", "v0.0.44", 1)
		},
		"query":                func(r *managedToolGitHubRelease) { r.Assets[0].URL += "?other=1" },
		"zero size":            func(r *managedToolGitHubRelease) { r.Assets[0].Size = 0 },
		"oversize":             func(r *managedToolGitHubRelease) { r.Assets[0].Size = 2<<30 + 1 },
		"duplicate":            func(r *managedToolGitHubRelease) { r.Assets = append(r.Assets, r.Assets[0]) },
		"draft":                func(r *managedToolGitHubRelease) { r.Draft = true },
		"prerelease in stable": func(r *managedToolGitHubRelease) { r.Prerelease = true },
		"invalid version":      func(r *managedToolGitHubRelease) { r.Tag = "v../../invalid" },
		"wrong architecture":   func(r *managedToolGitHubRelease) { r.Assets[0].Name = "T3-Code-0.0.45-x64.zip" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			release, _ := managedToolTestGitHub(t, "t3-code", "darwin", "arm64", "stable", "0.0.45")
			mutate(&release)
			data, _ := json.Marshal(release)
			catalog := managedToolTestCatalog(t, map[string][]byte{"https://api.github.com/repos/pingdotgg/t3code/releases/latest": data})
			if _, err := catalog.Latest(context.Background(), "t3-code", "darwin", "arm64", "stable"); err == nil {
				t.Fatal("accepted unsafe metadata")
			}
		})
	}
}

func TestManagedToolCatalogChannelsDoNotFallback(t *testing.T) {
	stable, _ := managedToolTestGitHub(t, "synara", "darwin", "arm64", "stable", "1.0.3")
	nightly, _ := managedToolTestGitHub(t, "synara", "darwin", "arm64", "nightly", "1.1.0-nightly.1")
	beta, _ := managedToolTestGitHub(t, "synara", "darwin", "arm64", "beta", "1.0.2-beta.1")
	data, _ := json.Marshal([]managedToolGitHubRelease{nightly, stable, beta})
	address := "https://api.github.com/repos/Emanuele-web04/synara/releases?per_page=100&page=1"
	got, err := managedToolTestCatalog(t, map[string][]byte{address: data}).Latest(context.Background(), "synara", "darwin", "arm64", "")
	if err != nil || got.Channel != "beta" || got.Version != "1.0.2-beta.1" {
		t.Fatalf("lost default Beta channel: %+v %v", got, err)
	}
	data, _ = json.Marshal([]managedToolGitHubRelease{stable, nightly})
	if _, err := managedToolTestCatalog(t, map[string][]byte{address: data}).Latest(context.Background(), "synara", "darwin", "arm64", "beta"); err == nil {
		t.Fatal("fell back to another channel")
	}
	if _, err := (&vendorManagedToolCatalog{}).Latest(context.Background(), "synara", "darwin", "arm64", "../beta"); err == nil {
		t.Fatal("accepted unlisted channel")
	}
}

func TestManagedToolCatalogOpenCodeMajorGuard(t *testing.T) {
	release, _ := managedToolTestGitHub(t, "opencode", "linux", "amd64", "stable", "2.0.25")
	data, _ := json.Marshal(release)
	catalog := managedToolTestCatalog(t, map[string][]byte{"https://api.github.com/repos/anomalyco/opencode/releases/latest": data})
	if _, err := catalog.Latest(context.Background(), "opencode", "linux", "amd64", "stable"); err == nil || !strings.Contains(err.Error(), "1.x") {
		t.Fatalf("major upgrade not blocked: %v", err)
	}
}

func TestManagedToolCatalogSupport(t *testing.T) {
	for _, tc := range []struct{ client, platform, arch string }{
		{"claude", "windows", "amd64"}, {"codex-cli", "freebsd", "amd64"}, {"zed", "linux", "riscv64"}, {"open-design", "linux", "amd64"}, {"synara", "linux", "arm64"}, {"openmausbot", "linux", "arm64"}, {"xcode-chat", "darwin", "arm64"}, {"xcode-codex", "darwin", "arm64"}, {"xcode-claude", "linux", "amd64"}, {"unknown", "darwin", "arm64"},
	} {
		if reason, _ := managedToolSupport(tc.client, tc.platform, tc.arch); reason == "" {
			t.Fatalf("unsupported combination accepted: %+v", tc)
		}
	}
	for _, client := range []string{"codex", "codex-cli", "claude-desktop", "claude", "omp", "opencode", "t3-code", "zed"} {
		for _, platform := range []string{"darwin", "linux"} {
			for _, arch := range []string{"amd64", "arm64"} {
				if reason, _ := managedToolSupport(client, platform, arch); reason != "" {
					t.Fatalf("supported combination rejected: %s/%s/%s: %s", client, platform, arch, reason)
				}
			}
		}
	}
}

func TestManagedToolCatalogCasks(t *testing.T) {
	cases := []struct{ client, cask, platform, arch, version, address, variation, channel, format string }{
		{"codex", "chatgpt", "darwin", "arm64", "26.1007.30912", "https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-arm64-26.1007.30912.zip", "", "stable", "zip"},
		{"codex", "chatgpt", "darwin", "amd64", "26.1007.30912", "https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-x64-26.1007.30912.zip", "tahoe", "stable", "zip"},
		{"claude-desktop", "claude", "darwin", "amd64", "2.31226.1,abcdef", "https://downloads.claude.ai/releases/darwin/universal/2.31226.1/Claude-abcdef.zip", "", "stable", "zip"},
		{"claude", "claude-code", "darwin", "arm64", "2.1.287", "https://downloads.claude.ai/claude-code-releases/2.1.287/darwin-arm64/claude", "", "stable", "binary"},
		{"claude", "claude-code@latest", "darwin", "amd64", "2.1.296", "https://downloads.claude.ai/claude-code-releases/2.1.296/darwin-x64/claude", "sequoia", "latest", "binary"},
		{"claude", "claude-code", "linux", "amd64", "2.1.287", "https://downloads.claude.ai/claude-code-releases/2.1.287/linux-x64/claude", "x86_64_linux", "stable", "binary"},
		{"claude", "claude-code", "linux", "arm64", "2.1.287", "https://downloads.claude.ai/claude-code-releases/2.1.287/linux-arm64/claude", "arm64_linux", "stable", "binary"},
	}
	for _, tc := range cases {
		t.Run(tc.client+"/"+tc.platform+"/"+tc.arch+"/"+tc.channel, func(t *testing.T) {
			metadata := map[string]any{"version": tc.version, "url": tc.address, "sha256": managedToolTestDigest}
			if tc.variation != "" {
				metadata["url"] = "https://downloads.claude.ai/wrong-architecture"
				variations := map[string]any{tc.variation: map[string]any{"url": tc.address, "sha256": managedToolTestDigest}}
				if tc.platform == "darwin" {
					variations["x86_64_linux"] = map[string]any{"url": tc.address, "sha256": nil}
				}
				metadata["variations"] = variations
			}
			data, _ := json.Marshal(metadata)
			got, err := managedToolTestCatalog(t, map[string][]byte{"https://formulae.brew.sh/api/cask/" + tc.cask + ".json": data}).Latest(context.Background(), tc.client, tc.platform, tc.arch, tc.channel)
			if err != nil {
				t.Fatal(err)
			}
			version, _, _ := strings.Cut(tc.version, ",")
			if got.URL != tc.address || got.Version != version || got.Format != tc.format || got.Size != 0 || got.SHA256 != managedToolTestDigest {
				t.Fatalf("wrong cask: %+v", got)
			}
		})
	}
}

func TestManagedToolCatalogCaskRejectsUncheckedOrWrongPlatform(t *testing.T) {
	for _, tc := range []struct{ address, digest string }{
		{"https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-arm64-26.1007.30912.zip", "no_check"},
		{"https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-x64-26.1007.30912.zip", managedToolTestDigest},
		{"https://example.com/codex-app-prod/ChatGPT-darwin-arm64-26.1007.30912.zip", managedToolTestDigest},
		{"https://persistent.oaistatic.com/codex-app-prod/ChatGPT-darwin-arm64-26.1007.30912.zip?x=1", managedToolTestDigest},
	} {
		data, _ := json.Marshal(map[string]any{"version": "26.1007.30912", "url": tc.address, "sha256": tc.digest})
		if _, err := managedToolTestCatalog(t, map[string][]byte{"https://formulae.brew.sh/api/cask/chatgpt.json": data}).Latest(context.Background(), "codex", "darwin", "arm64", "stable"); err == nil {
			t.Fatalf("accepted %s %s", tc.address, tc.digest)
		}
	}
}

func managedToolTestDebianParagraph(pkg, version, arch string) string {
	return "Package: " + pkg + "\nVersion: " + version + "\nArchitecture: " + arch + "\nFilename: pool/main/" + pkg[:1] + "/" + pkg + "/" + pkg + "_" + version + "_" + arch + ".deb\nSize: 123456\nSHA256: " + managedToolTestDigest + "\nDescription: Package\n continuation\n"
}

func TestManagedToolCatalogDebianMatrix(t *testing.T) {
	for _, client := range []string{"codex", "claude-desktop"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(client+"/"+arch, func(t *testing.T) {
				pkg, base, version := "chatgpt", "https://persistent.oaistatic.com/codex-app-prod/linux/deb/", "26.1007.21434"
				if client == "claude-desktop" {
					pkg, base, version = "claude-desktop", "https://downloads.claude.ai/claude-desktop/apt/stable/", "2.31226.1"
				}
				data := []byte(managedToolTestDebianParagraph(pkg, "2.2.1", arch) + "\n" + managedToolTestDebianParagraph(pkg, version, arch) + "\n" + managedToolTestDebianParagraph("unrelated", "999.0.0", arch))
				address := base + "dists/stable/main/binary-" + arch + "/Packages"
				if client == "codex" {
					var buffer bytes.Buffer
					writer := gzip.NewWriter(&buffer)
					_, _ = writer.Write(data)
					_ = writer.Close()
					data = buffer.Bytes()
					address += ".gz"
				}
				got, err := managedToolTestCatalog(t, map[string][]byte{address: data}).Latest(context.Background(), client, "linux", arch, "stable")
				if err != nil {
					t.Fatal(err)
				}
				if got.Version != version || got.Format != "deb" || got.Size != 123456 || got.SHA256 != managedToolTestDigest || got.URL != base+"pool/main/"+pkg[:1]+"/"+pkg+"/"+pkg+"_"+version+"_"+arch+".deb" {
					t.Fatalf("wrong Debian package: %+v", got)
				}
			})
		}
	}
}

func TestManagedToolCatalogDebianRejectsUnsafeIndex(t *testing.T) {
	base := managedToolTestDebianParagraph("chatgpt", "26.1007.21434", "amd64")
	for name, data := range map[string]string{
		"path traversal":    strings.Replace(base, "pool/main/c/chatgpt/", "../../", 1),
		"absolute URL":      strings.Replace(base, "pool/main/c/chatgpt/", "https://example.com/", 1),
		"wrong digest":      strings.Replace(base, managedToolTestDigest, "no_check", 1),
		"zero size":         strings.Replace(base, "Size: 123456", "Size: 0", 1),
		"duplicate":         base + "SHA256: " + managedToolTestDigest + "\n",
		"epoch version":     strings.ReplaceAll(base, "26.1007.21434", "1:26.1007.21434"),
		"prerelease":        strings.ReplaceAll(base, "26.1007.21434", "26.1007.21434-beta.1"),
		"conflicting entry": base + "\n" + strings.Replace(base, "Size: 123456", "Size: 654321", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := managedToolDebianIndex([]byte(data), "codex", "chatgpt", "https://persistent.oaistatic.com/codex-app-prod/linux/deb/", "amd64", "stable"); err == nil {
				t.Fatal("accepted unsafe package index")
			}
		})
	}
}

func TestManagedToolCatalogMetadataBoundedAndAnonymous(t *testing.T) {
	for _, address := range []string{"http://api.github.com/releases", "https://api.github.com:443/releases", "https://user@api.github.com/releases", "https://example.com/releases", "https://api.github.com/releases#fragment"} {
		if managedToolMetadataURL(address) {
			t.Fatalf("allowed %s", address)
		}
	}
	client := &http.Client{Transport: managedToolCatalogTransport(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("https://secret.invalid/token?private=value")
	})}
	_, err := (&vendorManagedToolCatalog{httpClient: client}).metadata(context.Background(), "https://api.github.com/repos/openai/codex/releases/latest")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("leaked raw network error: %v", err)
	}
	client.Transport = managedToolCatalogTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), ContentLength: managedToolMetadataLimit + 1, Header: http.Header{}}, nil
	})
	if _, err := (&vendorManagedToolCatalog{httpClient: client}).metadata(context.Background(), "https://api.github.com/repos/openai/codex/releases/latest"); err == nil {
		t.Fatal("accepted oversized metadata")
	}
}
