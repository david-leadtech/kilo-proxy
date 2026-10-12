package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type managedDownloadRoundTrip func(*http.Request) (*http.Response, error)

func (f managedDownloadRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagedToolDownloadVerifiesEveryByteWithoutCredentials(t *testing.T) {
	data := []byte("verified fixture")
	hash := sha256.Sum256(data)
	for _, kind := range []string{"valid", "digest", "size", "truncated", "http", "url"} {
		t.Run(kind, func(t *testing.T) {
			release := managedToolRelease{Client: "codex-cli", Version: "1.2.3", SHA256: hex.EncodeToString(hash[:]), URL: "https://github.com/openai/codex/releases/download/fixture/codex.zip", Size: int64(len(data))}
			responseData := data
			status := 200
			switch kind {
			case "digest":
				release.SHA256 = strings.Repeat("b", 64)
			case "size":
				release.Size++
			case "truncated":
				responseData = data[:3]
			case "http":
				status = 500
			case "url":
				release.URL = "https://evil.example/codex"
			}
			calls := 0
			client := &http.Client{Transport: managedDownloadRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("installer inherited credentials")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(responseData)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
			})}
			err := downloadManagedTool(context.Background(), client, release, filepath.Join(t.TempDir(), "download"))
			if (err == nil) != (kind == "valid") {
				t.Fatal(kind, err)
			}
			if kind == "url" && calls != 0 {
				t.Fatal("untrusted download contacted")
			}
		})
	}
}

func TestManagedToolArchiveRejectsTraversalDuplicatesAndOutsideLinks(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "a/../../outside", "C:/outside", "a\\outside", "a\x00bad"} {
		if _, err := managedArchivePath(t.TempDir(), name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, kind := range []string{"normal", "traversal", "absolute-link", "outside-link", "duplicate", "device", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			var b bytes.Buffer
			tw := tar.NewWriter(&b)
			h := &tar.Header{Name: "bin/tool", Mode: 0700, Size: 4, Typeflag: tar.TypeReg}
			switch kind {
			case "traversal":
				h.Name = "../outside"
			case "absolute-link":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "/outside"
				h.Size = 0
			case "outside-link":
				h.Typeflag = tar.TypeSymlink
				h.Linkname = "../../outside"
				h.Size = 0
			case "device":
				h.Typeflag = tar.TypeChar
				h.Size = 0
			case "hardlink":
				h.Typeflag = tar.TypeLink
				h.Linkname = "missing"
				h.Size = 0
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if h.Size > 0 {
				tw.Write([]byte("test"))
			}
			if kind == "duplicate" {
				tw.WriteHeader(h)
				tw.Write([]byte("test"))
			}
			tw.Close()
			err := extractManagedToolTar(bytes.NewReader(b.Bytes()), t.TempDir())
			if (err == nil) != (kind == "normal") {
				t.Fatal(kind, err)
			}
		})
	}
}

func TestManagedToolZIPPreservesContainedFrameworkLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix bundles")
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	file, _ := zw.Create("Tool.app/Contents/Frameworks/Test.framework/Versions/A/Test")
	file.Write([]byte("fixture"))
	h := &zip.FileHeader{Name: "Tool.app/Contents/Frameworks/Test.framework/Versions/Current"}
	h.SetMode(os.ModeSymlink | 0700)
	link, _ := zw.CreateHeader(h)
	link.Write([]byte("A"))
	zw.Close()
	archive := filepath.Join(t.TempDir(), "fixture.zip")
	os.WriteFile(archive, b.Bytes(), 0600)
	root := t.TempDir()
	if err := extractManagedToolZip(archive, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "Tool.app/Contents/Frameworks/Test.framework/Versions/Current/Test"))
	if err != nil || string(data) != "fixture" {
		t.Fatal("framework link was damaged", err)
	}
}

func TestManagedToolLinksNeverCreatePathsOutsideStaging(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix archive links")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "payload")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	w := managedArchiveWriter{root: root}
	for _, link := range [][2]string{{"a", "."}, {"a/b/escape", "../.."}, {"a/b/escape/created-outside/link", "."}} {
		if err := w.link(link[0], link[1], false); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.finish(); err == nil {
		t.Fatal("accepted a link as an extraction directory")
	}
	if _, err := os.Lstat(filepath.Join(parent, "created-outside")); !os.IsNotExist(err) {
		t.Fatal("archive wrote outside staging", err)
	}
}

func TestManagedToolStagingNeverTraversesAUserLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installation")
	}
	profile := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(profile, "tools")); err != nil {
		t.Fatal(err)
	}
	if _, err := managedToolInstallDirectory(profile, "codex-cli"); err == nil {
		t.Fatal("installer followed an external tools directory")
	}
	entries, _ := os.ReadDir(external)
	if len(entries) != 0 {
		t.Fatal("external directory was changed")
	}
}

func TestManagedToolEntrypointMustBeUnambiguousAndNative(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "codex"), []byte("not native"), 0700)
	path, err := findManagedToolEntry(root, "codex-cli", runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	if validateManagedToolBinary(path, runtime.GOOS, runtime.GOARCH) == nil {
		t.Fatal("non-native binary was accepted")
	}
	os.Mkdir(filepath.Join(root, "extra"), 0700)
	os.WriteFile(filepath.Join(root, "extra", "codex"), []byte("duplicate"), 0700)
	if _, err = findManagedToolEntry(root, "codex-cli", runtime.GOOS); err == nil {
		t.Fatal("ambiguous package was accepted")
	}
}

func TestManagedToolRejectsMislabeledPackagedVersionsAndChannels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix managed packages")
	}
	app := syntheticT3CodeVersionExecutable(t, t.TempDir(), "macos", "0.0.46-nightly.20261012.3021")
	for _, tc := range []struct {
		version, channel string
		valid            bool
	}{
		{"0.0.46-nightly.20261012.3021", "nightly", true},
		{"0.0.47-nightly.20261013.1", "nightly", false},
		{"0.0.46-nightly.20261012.3021", "stable", false},
	} {
		err := verifyManagedToolVersion(context.Background(), managedToolRelease{Client: "t3-code", Version: tc.version, Channel: tc.channel}, app, "macos", runtime.GOARCH)
		if (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
}

func TestManagedToolCancelledArchiveCreatesNoPayload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var archive bytes.Buffer
	w := tar.NewWriter(&archive)
	if err := w.WriteHeader(&tar.Header{Name: "cancelled", Mode: 0700, Size: 4}); err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("data"))
	w.Close()
	root := t.TempDir()
	if err := extractManagedToolTarContext(ctx, bytes.NewReader(archive.Bytes()), root); err == nil {
		t.Fatal("cancelled extraction succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled extraction wrote payload", err)
	}
}
