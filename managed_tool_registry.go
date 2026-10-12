package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var managedToolLabelPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+\-]{0,127}$`)

// Only the fixed harness catalog can own a managed installation. Xcode remains
// an operating-system/App Store installation, never a writable Kilo package.
func managedToolClientValid(client string) bool {
	switch client {
	case "codex", "claude-desktop", "codex-cli", "claude", "opencode", "omp", "open-design", "openmausbot", "t3-code", "synara", "zed":
		return true
	}
	return false
}

func managedToolDirectory(profile, client string) (string, error) {
	if !managedToolClientValid(client) || !packageUpdateAbsolute(profile) {
		return "", errors.New("Invalid managed tool location.")
	}
	info, err := os.Lstat(profile)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Cannot safely inspect the Kilo Proxy profile.")
	}
	dir := filepath.Join(profile, "tools", client)
	for _, path := range []string{filepath.Join(profile, "tools"), dir} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
			return "", errors.New("The managed tool directory is unsafe.")
		}
	}
	return dir, nil
}

func managedToolValidateRecord(profile string, record managedToolRecord, fingerprint bool) error {
	dir, err := managedToolDirectory(profile, record.Client)
	if err != nil || record.Schema != 1 || !managedToolLabelPattern.MatchString(record.Version) || !managedToolLabelPattern.MatchString(record.Channel) || !packageUpdateAbsolute(record.Root) || !packageUpdateAbsolute(record.Executable) {
		return errors.New("Invalid managed tool record.")
	}
	releaseRoot := filepath.Join(dir, "releases")
	relative, err := filepath.Rel(releaseRoot, record.Root)
	if err != nil || relative == "." || filepath.Base(relative) != relative || !managedToolLabelPattern.MatchString(relative) {
		return errors.New("The managed tool is outside its private releases directory.")
	}
	relative, err = filepath.Rel(record.Root, record.Executable)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("The managed executable is outside its installation.")
	}
	// Framework links inside application bundles are allowed. The root and every
	// component leading to the selected entrypoint itself must remain real paths.
	for path := record.Executable; ; path = filepath.Dir(path) {
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || path != record.Executable && !info.IsDir() {
			return errors.New("The managed tool path contains an unsafe link or directory.")
		}
		if path == record.Executable && !(info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0) || info.IsDir() && strings.HasSuffix(path, ".app")) {
			return errors.New("The managed tool entrypoint is invalid.")
		}
		if path == profile {
			break
		}
		if path == filepath.Dir(path) {
			return errors.New("The managed tool is outside its profile.")
		}
		if (path == dir || path == releaseRoot || path == record.Root || path == filepath.Join(profile, "tools")) && runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
			return errors.New("The managed tool directory is writable by another user.")
		}
	}
	if len(record.Digest) != 64 {
		return errors.New("Invalid managed tool fingerprint.")
	}
	if _, err := hex.DecodeString(record.Digest); err != nil || strings.ToLower(record.Digest) != record.Digest {
		return errors.New("Invalid managed tool fingerprint.")
	}
	if fingerprint {
		digest, err := managedToolFingerprint(record.Executable, runtime.GOOS)
		if err != nil || digest != record.Digest {
			return errors.New("The managed installation changed. Install it again before opening it.")
		}
	}
	return nil
}

func readManagedTool(profile, client string) (managedToolRecord, error) {
	dir, err := managedToolDirectory(profile, client)
	if err != nil {
		return managedToolRecord{}, err
	}
	path := filepath.Join(dir, "current.json")
	info, err := os.Lstat(path)
	if err != nil {
		return managedToolRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<10 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return managedToolRecord{}, errors.New("The managed tool record must be a private regular file.")
	}
	data, err := readOpenDesignShimFile(path, 16<<10)
	if err != nil {
		return managedToolRecord{}, errors.New("Cannot safely read the managed tool record.")
	}
	var record managedToolRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || decoder.Decode(&struct{}{}) != io.EOF || record.Client != client {
		return managedToolRecord{}, errors.New("Invalid managed tool record.")
	}
	if err := managedToolValidateRecord(profile, record, true); err != nil {
		return managedToolRecord{}, err
	}
	return record, nil
}

func writeManagedTool(profile string, record managedToolRecord) error {
	if err := managedToolValidateRecord(profile, record, true); err != nil {
		return err
	}
	dir, _ := managedToolDirectory(profile, record.Client)
	path := filepath.Join(dir, "current.json")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return errors.New("Cannot replace an unsafe managed tool record.")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return atomicProfileFile(path, append(data, '\n'), 0600)
}

// Resolve the native files a managed entrypoint executes. The Linux Zed
// distribution has a separate bin/zed CLI and libexec/zed-editor runtime; both
// belong to the same retained package and must remain intact.
func managedToolRuntimeBinaries(executable, platform string) ([]string, error) {
	if !packageUpdateAbsolute(executable) {
		return nil, errors.New("Invalid managed executable.")
	}
	root := filepath.Dir(executable)
	binary := executable
	if strings.HasSuffix(executable, ".app") {
		root = executable
		binary = managedToolBundleExecutable(executable)
		if binary == "" {
			return nil, errors.New("Cannot verify the application bundle executable.")
		}
	}
	paths := []string{binary}
	if platform != "windows" && binary == executable && filepath.Base(binary) == "zed" && filepath.Base(filepath.Dir(binary)) == "bin" {
		root = filepath.Dir(filepath.Dir(binary))
		paths = append(paths, filepath.Join(root, "libexec", "zed-editor"))
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("The managed tool runtime directory is unsafe.")
	}
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || !safeLaunchDir(filepath.Dir(path), root) {
			return nil, errors.New("The managed native runtime is outside its installation.")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("The managed tool native runtime is missing or unsafe.")
		}
	}
	return paths, nil
}

// This fingerprint covers the actual entrypoint and the metadata/runtime ASAR
// that Electron executes. It never traverses frameworks or personal profiles.
func managedToolFingerprint(executable, platform string) (string, error) {
	paths, err := managedToolRuntimeBinaries(executable, platform)
	if err != nil {
		return "", err
	}
	nativeRoot := filepath.Dir(executable)
	if len(paths) > 1 {
		nativeRoot = filepath.Dir(nativeRoot)
	}
	if strings.HasSuffix(executable, ".app") {
		nativeRoot = executable
		paths = append(paths, filepath.Join(executable, "Contents", "Info.plist"))
		for _, name := range []string{"header.json", "open-design-config.json"} {
			candidate := filepath.Join(executable, "Contents", "Resources", name)
			if _, err := os.Lstat(candidate); err == nil {
				paths = append(paths, candidate)
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
	}
	hash := sha256.New()
	for i, path := range paths {
		if !safeLaunchDir(filepath.Dir(path), nativeRoot) {
			return "", errors.New("Unsafe managed installation metadata directory.")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 1<<30 {
			return "", errors.New("Cannot safely fingerprint the managed installation.")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", errors.New("Cannot read the managed installation.")
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			file.Close()
			return "", errors.New("The managed installation changed during inspection.")
		}
		fmt.Fprintf(hash, "%d:%d\x00", i, info.Size())
		_, copyErr := io.Copy(hash, io.LimitReader(file, (1<<30)+1))
		after, statErr := file.Stat()
		closeErr := file.Close()
		if copyErr != nil || statErr != nil || closeErr != nil || after.Size() != opened.Size() || !after.ModTime().Equal(opened.ModTime()) {
			return "", errors.New("The managed installation changed during inspection.")
		}
	}
	// ASAR indexes and the root package/main are bounded. Frameworks and the
	// potentially enormous dependency tree are not traversed during discovery.
	root := filepath.Dir(executable)
	if strings.HasSuffix(executable, ".app") {
		root = filepath.Join(executable, "Contents")
	}
	for _, resources := range []string{"Resources", "resources"} {
		path := filepath.Join(root, resources, "app.asar")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", err
		}
		if !safeLaunchDir(filepath.Dir(path), root) {
			return "", errors.New("Unsafe managed application resources directory.")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("Unsafe managed application archive.")
		}
		archive, err := t3CodeOpenASAR(path)
		if err != nil {
			return "", err
		}
		packageData, err := archive.read("package.json", 64<<10)
		var metadata struct{ Main string }
		if err != nil || json.Unmarshal(packageData, &metadata) != nil {
			archive.file.Close()
			return "", errors.New("Invalid managed application metadata.")
		}
		fmt.Fprintf(hash, "asar:%d\x00", len(archive.header))
		hash.Write(archive.header)
		fmt.Fprintf(hash, "package:%d\x00", len(packageData))
		hash.Write(packageData)
		if metadata.Main != "" {
			mainPath := metadata.Main
			for strings.HasPrefix(mainPath, "./") {
				mainPath = strings.TrimPrefix(mainPath, "./")
			}
			main, err := archive.read(mainPath, 16<<20)
			if err != nil {
				archive.file.Close()
				return "", errors.New("Invalid managed application main entrypoint.")
			}
			fmt.Fprintf(hash, "main:%d\x00", len(main))
			hash.Write(main)
		}
		after, statErr := archive.file.Stat()
		archive.file.Close()
		if statErr != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
			return "", errors.New("The managed archive changed during inspection.")
		}
		break
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// An invalid existing managed record is a visible error, never an implicit
// switch to a different external installation or release channel.
func (a *app) resolveManagedTool(client, custom string) (string, error) {
	if custom != "" || a.dir == "" || runtime.GOOS == "windows" || !managedToolClientValid(client) {
		return resolveLaunchClient(client, custom)
	}
	record, err := readManagedTool(a.dir, client)
	if err == nil {
		return record.Executable, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return resolveLaunchClient(client, custom)
}

func (a *app) managedToolPath(client string) (string, bool) {
	if runtime.GOOS == "windows" {
		return "", false
	}
	record, err := readManagedTool(a.dir, client)
	return record.Executable, err == nil
}
