package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The official Windows installer writes this fixed shim beside the versioned
// executable. Resolve its recorded binary directly so native argv never crosses
// cmd.exe or PowerShell's argument parser. No arbitrary script is interpreted.
const museWindowsOfficialShim = "@echo off\r\nsetlocal\r\n\"%SystemRoot%\\System32\\WindowsPowerShell\\v1.0\\powershell.exe\" -NoProfile -ExecutionPolicy Bypass -File \"%~dp0.muse-launcher.ps1\" %*\r\nexit /b %ERRORLEVEL%\r\n"

func resolveMuseWindowsLauncher(path, platform string) (string, error) {
	if platform != "windows" || !strings.EqualFold(filepath.Base(path), "muse.cmd") {
		return path, nil
	}
	invalid := errors.New("Cannot resolve this official Muse Code installation. Restore its recorded executable or select the exact Muse .exe in CLI settings.")
	// An unrelated custom command stays a command; never infer a sibling binary
	// from its filename alone. Symlinked shims also retain generic launch rules.
	shim, err := museWindowsRegularFile(path, 4096)
	if err != nil {
		return path, nil
	}
	shim = bytes.TrimPrefix(shim, []byte{0xef, 0xbb, 0xbf})
	if string(bytes.ReplaceAll(shim, []byte("\r\n"), []byte("\n"))) != strings.ReplaceAll(museWindowsOfficialShim, "\r\n", "\n") {
		return path, nil
	}
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", invalid
	}
	version, err := museWindowsRegularFile(filepath.Join(filepath.Dir(path), ".muse-version"), 128)
	if err != nil {
		return "", invalid
	}
	build := strings.TrimSpace(string(version))
	if !museManagedVersionPattern.MatchString(build) {
		return "", invalid
	}
	binary := filepath.Join(filepath.Dir(path), "muse-bin-"+build+".exe")
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !launchExecutable(binary) {
		return "", invalid
	}
	return binary, nil
}

func museWindowsRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("Invalid Muse Code installation metadata.")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("Muse Code installation metadata changed.")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Invalid Muse Code installation metadata.")
	}
	return data, nil
}
