package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const synaraAppImagePending = "Synara AppImage detected. Select its extracted executable in Kilo settings, or install a verified Kilo copy. The original AppImage is kept and is never executed during detection."

func (a *app) synaraSystemRequirement(platform string) error {
	check := a.synaraCheckSystem
	if check == nil {
		check = func(platform string) string { return managedToolSystemRequirement("synara", platform) }
	}
	if reason := check(platform); reason != "" {
		return errors.New(reason)
	}
	return nil
}

var synaraAppImageName = regexp.MustCompile(`(?i)^synara(-beta)?-[0-9][a-z0-9.+-]*-x86_64\.appimage$`)

func validateSynaraExecutablePath(path string) error {
	if path != "" && (len(path) > 8192 || !packageUpdateAbsolute(path) || strings.ContainsAny(path, "\x00\r\n")) {
		return errors.New("Choose an absolute Synara application path, or leave it empty for automatic detection.")
	}
	if path != "" && !synaraPersistentPath(path) {
		return errors.New("Select the original AppImage or a persistent extracted Synara installation, not its temporary mount or a process path.")
	}
	return nil
}

func synaraPersistentPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "/proc" || strings.HasPrefix(path, "/proc/") || strings.HasSuffix(path, " (deleted)") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if strings.HasPrefix(segment, ".mount_") {
			return false
		}
	}
	return true
}

// This header identifies an AppImage candidate, not its publisher, version or
// channel. Never run a local package to discover metadata or extract it here.
func synaraAppImage(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 64 {
		return false
	}
	f, err := synaraOpenCandidate(path)
	if err != nil {
		return false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return false
	}
	var header [64]byte
	_, err = io.ReadFull(f, header[:])
	return err == nil && bytes.Equal(header[:4], []byte{'\x7f', 'E', 'L', 'F'}) && header[4] == 2 && header[5] == 1 && bytes.Equal(header[8:11], []byte{'A', 'I', 2})
}

// Only bounded, conventional per-user locations are inspected. Arbitrary
// files, live process environments and ephemeral AppImage mounts are not read.
func synaraAppImageCandidates(home string) []string {
	if !filepath.IsAbs(home) {
		return nil
	}
	var result []string
	for _, dir := range []string{filepath.Join(home, "Applications"), filepath.Join(home, ".local", "bin"), filepath.Join(home, "Downloads"), filepath.Join(home, "Descargas")} {
		f, err := synaraOpenCandidate(dir)
		if err != nil {
			continue
		}
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			f.Close()
			continue
		}
		names, _ := f.Readdirnames(1024)
		f.Close()
		sort.Strings(names)
		for _, name := range names {
			if synaraAppImageName.MatchString(name) {
				path := filepath.Join(dir, name)
				canonical, err := filepath.EvalSymlinks(path)
				if err == nil && synaraPersistentPath(canonical) && synaraAppImage(path) {
					result = append(result, path)
				}
			}
		}
	}
	return result
}

func (a *app) validateSelectedSynaraExecutable(path, platform string) error {
	if err := validateSynaraExecutablePath(path); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if path == "" || err != nil || !synaraPersistentPath(canonical) {
		return errors.New("The selected Synara installation is unavailable. Choose its installed path or return to automatic detection in Kilo settings.")
	}
	tools := filepath.Join(a.dir, "tools")
	owned := a.dir != "" && selectedClientPathWithin(tools, path)
	if canonicalTools, err := filepath.EvalSymlinks(tools); err == nil && selectedClientPathWithin(canonicalTools, canonical) {
		owned = true
	}
	if owned {
		record, err := readManagedTool(a.dir, "synara")
		current := err == nil && filepath.Clean(record.Executable) == filepath.Clean(path)
		if err == nil && !current {
			resolved, resolveErr := filepath.EvalSymlinks(record.Executable)
			current = resolveErr == nil && filepath.Clean(path) == resolved
		}
		if runtime.GOOS == "windows" || !current {
			return errors.New("Choose the verified current Kilo installation, or install the desired version again from Kilo settings.")
		}
	}
	if platform == "linux" && synaraAppImage(path) {
		return nil
	}
	if (platform == "macos" || platform == "darwin") && strings.HasSuffix(path, ".app") {
		if managedToolBundleExecutable(path) == "" {
			return errors.New("The Synara desktop application bundle is invalid.")
		}
	} else if !launchExecutable(path) {
		return errors.New("The selected Synara installation is unavailable. Choose its installed path or return to automatic detection in Kilo settings.")
	}
	_, err = synaraPackageMetadata(path, platform)
	return err
}
