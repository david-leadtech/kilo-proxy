package main

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func managedToolZedRegistryFixture(t *testing.T, profile string) managedToolRecord {
	t.Helper()
	record := managedToolRegistryFixture(t, profile, "zed", "release-1")
	// The package folder name is incidental; only bin/zed + libexec/zed-editor
	// define the official CLI/runtime relationship.
	packageRoot := filepath.Join(record.Root, "package with arbitrary name")
	record.Executable = filepath.Join(packageRoot, "bin", "zed")
	for _, path := range []string{record.Executable, filepath.Join(packageRoot, "libexec", "zed-editor")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("synthetic native runtime; never executed"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	record.Digest, err = managedToolFingerprint(record.Executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestManagedToolZedRegistryBindsEditorRuntimeAndRejectsMissingOrLinkedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installation")
	}
	for _, change := range []string{"changed", "missing", "file-link", "directory-link", "not-executable"} {
		t.Run(change, func(t *testing.T) {
			profile := t.TempDir()
			record := managedToolZedRegistryFixture(t, profile)
			if err := writeManagedTool(profile, record); err != nil {
				t.Fatal(err)
			}
			if got, err := readManagedTool(profile, "zed"); err != nil || got != record {
				t.Fatal("unmodified CLI/runtime pair rejected", err)
			}
			editor := filepath.Join(filepath.Dir(filepath.Dir(record.Executable)), "libexec", "zed-editor")
			switch change {
			case "changed":
				if err := os.WriteFile(editor, []byte("replacement runtime"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(editor); err != nil {
					t.Fatal(err)
				}
			case "file-link":
				external := filepath.Join(t.TempDir(), "editor")
				if err := os.WriteFile(external, []byte("outside runtime"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(editor); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, editor); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				directory := filepath.Dir(editor)
				if err := os.Rename(directory, directory+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(directory+"-retained", directory); err != nil {
					t.Fatal(err)
				}
			case "not-executable":
				if err := os.Chmod(editor, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readManagedTool(profile, "zed"); err == nil {
				t.Fatal("changed or unsafe Zed editor retained a valid managed record")
			}
		})
	}
}

func TestManagedToolZedNativeRuntimeArchitectureIsCheckedSeparately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed Unix installation")
	}
	record := managedToolZedRegistryFixture(t, t.TempDir())
	paths, err := managedToolRuntimeBinaries(record.Executable, "linux")
	if err != nil || len(paths) != 2 || paths[0] != record.Executable {
		t.Fatal(paths, err)
	}
	elfFixture := func(machine uint16) []byte {
		header := make([]byte, 64)
		copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
		binary.LittleEndian.PutUint16(header[16:18], 2)
		binary.LittleEndian.PutUint16(header[18:20], machine)
		binary.LittleEndian.PutUint32(header[20:24], 1)
		binary.LittleEndian.PutUint16(header[52:54], 64)
		binary.LittleEndian.PutUint16(header[54:56], 56)
		binary.LittleEndian.PutUint16(header[58:60], 64)
		return header
	}
	for _, path := range paths {
		if err := os.WriteFile(path, elfFixture(62), 0700); err != nil {
			t.Fatal(err)
		}
		if err := validateManagedToolBinary(path, "linux", "amd64"); err != nil {
			t.Fatal("synthetic Linux x64 fixture rejected", err)
		}
	}
	if err := os.WriteFile(paths[1], elfFixture(183), 0700); err != nil {
		t.Fatal(err)
	}
	if validateManagedToolBinary(paths[1], "linux", "amd64") == nil {
		t.Fatal("the editor runtime's wrong architecture was accepted")
	}
}

func managedToolRegistryFixture(t *testing.T, profile, client, token string) managedToolRecord {
	t.Helper()
	root := filepath.Join(profile, "tools", client, "releases", token)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "tool")
	if err := os.WriteFile(executable, []byte("synthetic native tool; never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := managedToolFingerprint(executable, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	return managedToolRecord{Schema: 1, Client: client, Version: "1.2.3", Channel: "stable", Root: root, Executable: executable, Digest: digest}
}

func TestManagedToolRegistryActivatesOnlyVerifiedReleaseAndRetainsOldFiles(t *testing.T) {
	profile := t.TempDir()
	old := managedToolRegistryFixture(t, profile, "codex-cli", "old-release")
	if err := writeManagedTool(profile, old); err != nil {
		t.Fatal(err)
	}
	got, err := readManagedTool(profile, "codex-cli")
	if err != nil || got != old {
		t.Fatal(got, err)
	}
	next := managedToolRegistryFixture(t, profile, "codex-cli", "next-release")
	next.Version = "1.2.4"
	if err := writeManagedTool(profile, next); err != nil {
		t.Fatal(err)
	}
	got, err = readManagedTool(profile, "codex-cli")
	if err != nil || got != next {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(old.Executable); err != nil {
		t.Fatal("old version was removed", err)
	}
	info, err := os.Stat(filepath.Join(profile, "tools", "codex-cli", "current.json"))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("registry is not private", err)
	}
}

func TestManagedToolRegistryRejectsTamperingWithoutChangingCurrent(t *testing.T) {
	for _, name := range []string{"outside-root", "wrong-client", "traversal-root", "changed-binary", "bad-digest", "unknown-field", "world-readable"} {
		t.Run(name, func(t *testing.T) {
			profile := t.TempDir()
			record := managedToolRegistryFixture(t, profile, "codex-cli", "release-1")
			if err := writeManagedTool(profile, record); err != nil {
				t.Fatal(err)
			}
			current := filepath.Join(profile, "tools", "codex-cli", "current.json")
			switch name {
			case "outside-root":
				record.Executable = filepath.Join(profile, "outside")
			case "wrong-client":
				record.Client = "claude"
			case "traversal-root":
				record.Root = filepath.Dir(record.Root)
			case "changed-binary":
				if err := os.WriteFile(record.Executable, []byte("changed in place"), 0700); err != nil {
					t.Fatal(err)
				}
			case "bad-digest":
				record.Digest = strings.Repeat("0", 64)
			case "world-readable":
				if runtime.GOOS == "windows" {
					t.Skip("Windows mode bits do not represent POSIX privacy")
				}
				if err := os.Chmod(current, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if name != "world-readable" {
				data, _ := json.Marshal(record)
				if name == "unknown-field" {
					data = append(data[:len(data)-1], []byte(`,"command":"unsafe"}`)...)
				}
				if err := os.WriteFile(current, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readManagedTool(profile, "codex-cli"); err == nil {
				t.Fatal("unsafe registry accepted")
			}
		})
	}
}

func TestManagedToolRegistryRejectsDirectoryLinksAndAllowsBundleFrameworkLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink fixture")
	}
	profile := t.TempDir()
	record := managedToolRegistryFixture(t, profile, "codex-cli", "release-1")
	if err := writeManagedTool(profile, record); err != nil {
		t.Fatal(err)
	}
	moved := record.Root + "-moved"
	if err := os.Rename(record.Root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, record.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedTool(profile, "codex-cli"); err == nil {
		t.Fatal("linked release directory accepted")
	}

	root := filepath.Join(profile, "tools", "t3-code", "releases", "nightly-1")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	bundle := syntheticT3CodeExecutable(t, root, "darwin", t3CodeNightlyVersion)
	frameworks := filepath.Join(bundle, "Contents", "Frameworks")
	if err := os.MkdirAll(filepath.Join(frameworks, "Fixture.framework", "Versions", "A"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("A", filepath.Join(frameworks, "Fixture.framework", "Versions", "Current")); err != nil {
		t.Fatal(err)
	}
	digest, err := managedToolFingerprint(bundle, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	installed := managedToolRecord{Schema: 1, Client: "t3-code", Version: t3CodeNightlyVersion, Channel: "nightly", Root: root, Executable: bundle, Digest: digest}
	if err := writeManagedTool(profile, installed); err != nil {
		t.Fatal("legitimate internal framework link rejected", err)
	}
	if _, err := readManagedTool(profile, "t3-code"); err != nil {
		t.Fatal(err)
	}
}

func TestManagedToolResolverFailsClosedForCorruptRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows installations remain manual")
	}
	profile := t.TempDir()
	record := managedToolRegistryFixture(t, profile, "codex-cli", "release-1")
	if err := writeManagedTool(profile, record); err != nil {
		t.Fatal(err)
	}
	owner := &app{dir: profile}
	got, err := owner.launchRuntime().resolve("codex-cli", "")
	if err != nil || got != record.Executable {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(record.Executable, []byte("unexpected replacement"), 0700); err != nil {
		t.Fatal(err)
	}
	if path, err := owner.launchRuntime().resolve("codex-cli", ""); err == nil || path != "" {
		t.Fatal("corrupt managed release fell back to another installation", path, err)
	}
}
