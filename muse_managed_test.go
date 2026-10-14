package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func museManagedTestDocuments(t *testing.T) (map[string]any, map[string]any) {
	t.Helper()
	const version = "1.4.6-R6000.1"
	channel := map[string]any{"channel": "muse-stable", "version": version, "state": "public", "manifest_url": museManagedURL(version, "manifest.json")}
	artifacts := map[string]any{}
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			artifacts[museManagedPlatform(platform, arch)] = map[string]any{"url": museManagedURL(version, museManagedAsset(platform, arch)), "checksum": managedToolTestDigest, "size": int64(12345)}
		}
	}
	manifest := map[string]any{"version": version, "checksum_algorithm": "sha256", "artifacts": artifacts, "msp_schema_fingerprint": "sha256:future-schema"}
	return channel, manifest
}

func museManagedTestCatalog(t *testing.T, channel, manifest any) *vendorManagedToolCatalog {
	t.Helper()
	channelData, err := json.Marshal(channel)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return managedToolTestCatalog(t, map[string][]byte{
		museManagedChannelURL:                            channelData,
		museManagedURL("1.4.6-R6000.1", "manifest.json"): manifestData,
	})
}

func TestMuseManagedCatalogVerifiesOfficialPlatformMatrix(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(platform+"/"+arch, func(t *testing.T) {
				channel, manifest := museManagedTestDocuments(t)
				release, err := museManagedTestCatalog(t, channel, manifest).Latest(context.Background(), "muse", platform, arch, "stable")
				if err != nil {
					t.Fatal(err)
				}
				wantAsset := map[string]string{"darwin/amd64": "muse-x86-macos", "darwin/arm64": "muse-aarch64-macos", "linux/amd64": "muse-x86-linux", "linux/arm64": "muse-aarch64-linux"}[platform+"/"+arch]
				if release.Client != "muse" || release.Version != "1.4.6-R6000.1" || release.Channel != "stable" || release.AssetName != wantAsset || release.Format != "binary" || release.Size != 12345 || release.SHA256 != managedToolTestDigest || release.URL != museManagedURL(release.Version, wantAsset) {
					t.Fatalf("wrong fixed release: %+v", release)
				}
			})
		}
	}
	for _, platform := range []string{"windows", "freebsd"} {
		if _, err := (&vendorManagedToolCatalog{}).Latest(context.Background(), "muse", platform, "amd64", "stable"); err == nil {
			t.Fatalf("automatic installation unexpectedly enabled on %s", platform)
		}
	}
	if _, err := (&vendorManagedToolCatalog{}).Latest(context.Background(), "muse", "darwin", "arm64", "canary"); err == nil {
		t.Fatal("unreviewed Muse channel accepted")
	}
}

func TestMuseManagedCatalogRejectsAlteredReleaseIdentities(t *testing.T) {
	cases := map[string]func(map[string]any, map[string]any){
		"private channel":        func(c, m map[string]any) { c["state"] = "private" },
		"wrong channel":          func(c, m map[string]any) { c["channel"] = "muse-canary" },
		"invalid version":        func(c, m map[string]any) { c["version"] = "1.4.6/../../outside" },
		"numeric version":        func(c, m map[string]any) { c["version"] = "1.4.6" },
		"other manifest host":    func(c, m map[string]any) { c["manifest_url"] = "https://github.com/muse/manifest.json" },
		"wrong manifest version": func(c, m map[string]any) { c["manifest_url"] = museManagedURL("1.4.5-R5000.1", "manifest.json") },
		"manifest mismatch":      func(c, m map[string]any) { m["version"] = "1.4.5-R5000.1" },
		"wrong algorithm":        func(c, m map[string]any) { m["checksum_algorithm"] = "sha512" },
		"missing architecture":   func(c, m map[string]any) { delete(m["artifacts"].(map[string]any), "aarch64_macos") },
		"wrong cpu": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["url"] = museManagedURL("1.4.6-R6000.1", "muse-x86-macos")
		},
		"previous artifact": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["url"] = museManagedURL("1.4.5-R5000.1", "muse-aarch64-macos")
		},
		"nonbinary artifact": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["url"] = museManagedURL("1.4.6-R6000.1", "muse.pkg")
		},
		"invalid checksum": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["checksum"] = strings.Repeat("z", 64)
		},
		"zero size": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["size"] = 0
		},
		"oversize": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["size"] = int64(2<<30 + 1)
		},
		"fractional size": func(c, m map[string]any) {
			m["artifacts"].(map[string]any)["aarch64_macos"].(map[string]any)["size"] = 1.5
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			channel, manifest := museManagedTestDocuments(t)
			mutate(channel, manifest)
			if _, err := museManagedTestCatalog(t, channel, manifest).Latest(context.Background(), "muse", "darwin", "arm64", "stable"); err == nil {
				t.Fatal("accepted altered Muse release")
			}
		})
	}
	for _, data := range []string{`{"version":"1.4.6-R6000.1","version":"1.4.5-R5000.1"}`, `{"artifacts":{"a":{"size":1,"size":2}}}`, `{"a":1} {"b":2}`, `{"a":1,}`, `{/*comment*/"a":1}`} {
		if err := museManagedDecode([]byte(data), &map[string]any{}); err == nil {
			t.Fatalf("accepted ambiguous or nonstandard metadata %s", data)
		}
	}
}

func TestMuseManagedURLContracts(t *testing.T) {
	valid := museManagedURL("1.4.6-R6000.1", "muse-aarch64-macos")
	for _, address := range []string{
		strings.Replace(valid, "https:", "http:", 1), strings.Replace(valid, "facebook.com", "facebook.com.evil.test", 1),
		strings.Replace(valid, "lookaside.facebook.com", "user@lookaside.facebook.com", 1), strings.Replace(valid, "lookaside.facebook.com", "lookaside.facebook.com:443", 1),
		strings.Replace(valid, "/lookaside/", "/%6cookaside/", 1), valid + "&file=muse-x86-macos", valid + "&extra=1", valid + "#fragment",
		strings.Replace(valid, "channel=muse", "channel=other", 1), strings.Replace(valid, "muse-aarch64-macos", "../muse", 1),
		museManagedURL("1.4.6-R6000.1", "manifest.json"), museManagedURL("1.4.6-R6000.1", "muse.pkg"),
	} {
		if managedToolDownloadURL(address) || museManagedArtifactURL(address, "", "") {
			t.Errorf("accepted unauthorized Muse download %s", address)
		}
	}
	if !managedToolDownloadURL(valid) || !managedToolMetadataURL(museManagedChannelURL) || !managedToolMetadataURL(museManagedURL("1.4.6-R6000.1", "manifest.json")) || managedToolMetadataURL(valid) || managedToolMetadataURL(museManagedChannelURL+"?channel=canary") {
		t.Fatal("metadata and payload endpoint contracts overlap or reject the official endpoint")
	}
}

func TestMuseManagedUpdatesCompareTheCompleteRelease(t *testing.T) {
	for _, versions := range [][2]string{
		{"1.4.6-R6000.2", "1.4.6-R6000.1"},
		{"1.4.6-R10000.1", "1.4.6-R9000.1"},
		{"1.4.6-R6000.10", "1.4.6-R6000.9"},
		{"1.4.7-R1", "1.4.6-R999999999999999999999999999999"},
		{"1.4.6-R999999999999999999999999999999", "1.4.6-R999999999999999999999999999998"},
	} {
		if !managedToolVersionNewer(versions[0], versions[1]) || managedToolVersionNewer(versions[1], versions[0]) {
			t.Fatalf("Muse update identity was truncated or ordered incorrectly: %v", versions)
		}
	}
	for _, versions := range [][2]string{{"1.4.6-R6000.1", "1.4.6-R6000.1"}, {"1.4.6-R6000", "1.4.6-R6000.0"}} {
		if managedToolVersionNewer(versions[0], versions[1]) || managedToolVersionNewer(versions[1], versions[0]) {
			t.Fatalf("same Muse build offered an update: %v", versions)
		}
	}
	if _, ok := museManagedCompareVersions("1.4.6", "1.4.6-R6000.1"); ok {
		t.Fatal("other vendor versions entered Muse's build comparison")
	}
}

func TestMuseManagedPlansUseBuildIdentityWithoutDowngrading(t *testing.T) {
	for _, installed := range []struct {
		version string
		update  bool
	}{{"1.4.6-R6000", true}, {"1.4.6-R6000.1", false}, {"1.4.6-R6000.2", false}} {
		t.Run(installed.version, func(t *testing.T) {
			a, manager, _ := managedToolsFixture(t)
			record := managedToolRegistryFixture(t, a.dir, "muse", "existing")
			record.Version, record.Channel = installed.version, "stable"
			if err := writeManagedTool(a.dir, record); err != nil {
				t.Fatal(err)
			}
			channel, manifest := museManagedTestDocuments(t)
			manager.catalog = museManagedTestCatalog(t, channel, manifest)
			if err := manager.check("muse", "stable"); err != nil {
				t.Fatal(err)
			}
			status := waitManagedTool(t, manager, "muse")
			if status.InstalledVersion != installed.version || status.LatestVersion != "1.4.6-R6000.1" || status.CanUpdate != installed.update || status.CanInstall || (status.PlanID != "") != installed.update {
				t.Fatalf("Muse full build identity was lost in the update plan: %+v", status)
			}
		})
	}
}

func TestMuseManagedDownloadRejectsRedirectsAndVerifiesPayload(t *testing.T) {
	data := []byte("bounded verified Muse fixture")
	hash := sha256.Sum256(data)
	for _, kind := range []string{"valid", "digest", "size", "missing asset", "missing version", "other vendor redirect", "previous version redirect"} {
		t.Run(kind, func(t *testing.T) {
			release := managedToolRelease{Client: "muse", Version: "1.4.6-R6000.1", Channel: "stable", URL: museManagedURL("1.4.6-R6000.1", "muse-aarch64-macos"), AssetName: "muse-aarch64-macos", Format: "binary", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(data))}
			if kind == "digest" {
				release.SHA256 = strings.Repeat("f", 64)
			}
			if kind == "size" {
				release.Size++
			}
			if kind == "missing asset" {
				release.AssetName = ""
			}
			if kind == "missing version" {
				release.Version = ""
			}
			calls := 0
			client := &http.Client{Transport: managedDownloadRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("download received credentials")
				}
				if strings.Contains(kind, "redirect") {
					address := "https://github.com/vendor/tool/releases/download/fixture/muse"
					if kind == "previous version redirect" {
						address = museManagedURL("1.4.5-R5000.1", release.AssetName)
					}
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{address}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}, nil
			})}
			err := downloadManagedTool(context.Background(), client, release, filepath.Join(t.TempDir(), "download"))
			if (err == nil) != (kind == "valid") {
				t.Fatalf("%s: %v", kind, err)
			}
			if strings.Contains(kind, "redirect") && calls != 1 || (kind == "missing asset" || kind == "missing version") && calls != 0 {
				t.Fatalf("untrusted redirect or identity contacted: %d requests", calls)
			}
		})
	}
	for _, location := range []string{"https://api.github.com/repos/vendor/muse/releases/latest", museManagedURL("1.4.6-R6000.1", "manifest.json")} {
		calls := 0
		catalog := &vendorManagedToolCatalog{httpClient: &http.Client{Transport: managedToolCatalogTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}}
		if _, err := catalog.Latest(context.Background(), "muse", "darwin", "arm64", "stable"); err == nil || calls != 1 {
			t.Fatalf("followed fixed metadata redirect: %d %v", calls, err)
		}
	}
}

func TestMuseManagedBinaryEntrypointAndPrivateProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture; Windows installation remains manual")
	}
	root := t.TempDir()
	archive := filepath.Join(root, "download")
	payload := filepath.Join(root, "payload")
	if err := os.Mkdir(payload, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("META_API_KEY", "must-not-inherit")
	t.Setenv("MUSE_AUTH_PATH", filepath.Join(root, "personal-auth.json"))
	t.Setenv("MUSE_NO_AUTO_UPDATE", "0")
	t.Setenv("TBH_CREDENTIAL_BACKEND", "keychain")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --version ] || exit 2\n[ \"$MUSE_NO_AUTO_UPDATE\" = 1 ] && [ \"$MUSE_LOGIN\" = 0 ] || exit 3\n[ \"$MUSE_AUTH_PATH\" = \"$HOME/muse-auth.json\" ] && [ \"$XDG_CONFIG_HOME\" = \"$HOME\" ] && [ \"$PWD\" = \"$HOME\" ] || exit 4\n[ -z \"${META_API_KEY-}${MODEL_API_KEY-}${KILO_LOCAL_API_KEY-}\" ] || exit 5\n[ \"$TBH_CREDENTIAL_BACKEND\" = file ] || exit 6\nprintf 'Muse Code 1.4.6 (1.4.6-R6000.1)\\n'\n"
	if err := os.WriteFile(archive, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	release := managedToolRelease{Client: "muse", Version: "1.4.6-R6000.1", Channel: "stable", Format: "binary"}
	if err := extractManagedTool(context.Background(), archive, payload, release); err != nil {
		t.Fatal(err)
	}
	entry, err := findManagedToolEntry(payload, "muse", runtime.GOOS)
	if err != nil || entry != filepath.Join(payload, "muse") {
		t.Fatal(entry, err)
	}
	if err = verifyManagedToolVersion(context.Background(), release, entry, runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{"Muse Code 1.4.5 (1.4.6-R6000.1)", "Muse Code 1.4.6", "Muse Code 1.4.6 (1.4.6-R6000.1)\nunexpected", "Other Code 1.4.6 (1.4.6-R6000.1)"} {
		if err := os.WriteFile(entry, []byte("#!/bin/sh\nprintf '%s\\n' "+helperShellQuote(response)+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := managedToolInstalledVersion(context.Background(), "muse", entry, runtime.GOOS, runtime.GOARCH); err == nil {
			t.Fatalf("accepted ambiguous Muse identity %q", response)
		}
	}
}
