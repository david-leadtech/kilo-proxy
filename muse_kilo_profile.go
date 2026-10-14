package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

type museBinaryIdentity struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type musePreparedProfile struct {
	Version     int                `json:"version"`
	Binary      museBinaryIdentity `json:"binary"`
	Release     string             `json:"release"`
	Fingerprint string             `json:"fingerprint"`
}

func museExecutableIdentity(binary string) (museBinaryIdentity, error) {
	if validateCLIExecutable(binary) != nil {
		return museBinaryIdentity{}, errors.New("Select an installed Muse Code executable first.")
	}
	st, err := os.Stat(binary)
	if err != nil || !st.Mode().IsRegular() || !launchExecutable(binary) {
		return museBinaryIdentity{}, errors.New("The selected Muse Code executable is unavailable.")
	}
	return museBinaryIdentity{Path: filepath.Clean(binary), Size: st.Size(), Modified: st.ModTime().UnixNano()}, nil
}

func museKiloVersionSupported(release string) bool {
	// These are the official binaries whose configuration and Responses
	// contracts passed the isolated compatibility gate. Unknown releases keep
	// native Meta available rather than silently assuming the same protocol.
	return release == "1.4.6-R6000.1" || release == "1.4.7-R6101.1"
}

func (a *app) checkMuseKiloVersion(binary string) (string, error) {
	before, identityErr := museExecutableIdentity(binary)
	if identityErr != nil {
		return "", identityErr
	}
	var release string
	var err error
	if a.museVersionProbe != nil {
		release, err = a.museVersionProbe(binary)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		release, err = museManagedProbeVersion(ctx, binary)
	}
	if err != nil || !museKiloVersionSupported(release) {
		return "", errors.New("This Muse Code release has not been verified for Kilo models. Choose Muse Code 1.4.6-R6000.1 or 1.4.7-R6101.1, or use its native Meta connection.")
	}
	after, identityErr := museExecutableIdentity(binary)
	if identityErr != nil || after != before {
		return "", errors.New("The Muse Code executable changed during preparation. Refresh it and try again.")
	}
	return release, nil
}

func museTerminalHome(appDir string) string {
	return filepath.Join(appDir, "muse-code", "home")
}

func museKiloProfilePaths(home string) (settings, auth string) {
	dir := filepath.Join(home, ".config", "muse")
	return filepath.Join(dir, "settings.json"), filepath.Join(dir, "auth.json")
}

func prepareMusePrivateFile(path string, data []byte) (profileFile, error) {
	file, err := prepareProfileFile(path, data)
	if err != nil {
		return file, err
	}
	file.mode = 0600
	if file.exists {
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() {
			return file, errors.New("Cannot inspect the private Muse Code file safely.")
		}
		file.oldMode = st.Mode().Perm()
		if runtime.GOOS != "windows" && file.oldMode != 0600 {
			file.changed = true
		}
	}
	return file, nil
}

func musePreparedFingerprint(identity museBinaryIdentity, release string, settings, auth []byte, library modelLibrary, org, scope string, isolation []string) string {
	data, _ := json.Marshal([]any{2, identity, release, openDesignHash(settings), openDesignHash(auth), library, org, scope, isolation, museKiloCredentialBackend})
	return openDesignHash(data)
}

// Caller holds app.mu and launchMu. No normal Muse files or credentials enter
// this profile; its auth.json contains only the derived loopback credential.
func (a *app) prepareMuseTerminalProfile(binary, release string, library modelLibrary) error {
	identity, err := museExecutableIdentity(binary)
	if err != nil {
		return err
	}
	home := museTerminalHome(a.dir)
	rt := a.launchRuntime()
	isolation, err := museKiloIsolation(home, museNormalPaths(rt.home, os.Environ(), rt.platform))
	if err != nil {
		return err
	}
	settingsPath, authPath := museKiloProfilePaths(home)
	catalog := readNativeCatalogCache(a.dir, a.catalogScopeLocked())
	settings, auth, err := buildMuseKiloProfile(library, catalog, "http://127.0.0.1:"+strconv.Itoa(a.config.Port)+"/muse/v1", museProxyToken(a.config.LocalKey, "terminal"))
	if err != nil {
		return err
	}
	if err = safeEditorDir(a.dir, filepath.Dir(settingsPath)); err != nil {
		return err
	}
	for _, dir := range museKiloStateDirectories(home) {
		if err = safeEditorDir(a.dir, dir); err != nil {
			return err
		}
	}
	manifest := musePreparedProfile{Version: 1, Binary: identity, Release: release, Fingerprint: musePreparedFingerprint(identity, release, settings, auth, library, a.config.OrgID, a.catalogScopeLocked(), isolation)}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	var files []profileFile
	for _, item := range []struct {
		path string
		data []byte
	}{{settingsPath, settings}, {authPath, auth}, {filepath.Join(home, "kilo-profile.json"), append(data, '\n')}} {
		file, err := prepareMusePrivateFile(item.path, item.data)
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	_, err = saveEditorFiles(files)
	return err
}

// Readiness is a pure disk check. Version probes are only performed on an
// explicit prepare, never per request or while rendering Settings.
func (a *app) museTerminalPrepared(binary string, library modelLibrary) error {
	fail := errors.New("Prepare Muse Code again: its private profile no longer matches the selected models, connection or executable.")
	home := museTerminalHome(a.dir)
	rt := a.launchRuntime()
	isolation, err := museKiloIsolation(home, museNormalPaths(rt.home, os.Environ(), rt.platform))
	if err != nil {
		return fail
	}
	settingsPath, authPath := museKiloProfilePaths(home)
	if !museKiloStateSafe(home, a.dir) {
		return fail
	}
	data, err := readCatalogFile(filepath.Join(home, "kilo-profile.json"))
	var saved musePreparedProfile
	if err != nil || json.Unmarshal(data, &saved) != nil || saved.Version != 1 || !museKiloVersionSupported(saved.Release) {
		return fail
	}
	identity, err := museExecutableIdentity(binary)
	if err != nil || identity != saved.Binary {
		return fail
	}
	settings, auth, err := buildMuseKiloProfile(library, readNativeCatalogCache(a.dir, a.catalogScopeLocked()), "http://127.0.0.1:"+strconv.Itoa(a.config.Port)+"/muse/v1", museProxyToken(a.config.LocalKey, "terminal"))
	if err != nil || saved.Fingerprint != musePreparedFingerprint(identity, saved.Release, settings, auth, library, a.config.OrgID, a.catalogScopeLocked(), isolation) {
		return fail
	}
	for _, item := range []struct {
		path string
		data []byte
	}{{settingsPath, settings}, {authPath, auth}} {
		st, err := os.Lstat(item.path)
		if err != nil || !st.Mode().IsRegular() || runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0 {
			return fail
		}
		actual, err := readCatalogFile(item.path)
		if err != nil || !bytes.Equal(actual, item.data) {
			return fail
		}
	}
	return nil
}
