package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const managedToolExpandedLimit int64 = 4 << 30

func managedToolDownloadURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Host {
	case "lookaside.facebook.com":
		return museManagedArtifactURL(raw, "", "")
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "persistent.oaistatic.com", "downloads.claude.ai":
		return true
	}
	return false
}

func managedToolHTTPClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !managedToolDownloadURL(req.URL.String()) {
			return errors.New("The download redirected outside the official download service.")
		}
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	}}
}

func downloadManagedTool(ctx context.Context, client *http.Client, release managedToolRelease, destination string) error {
	if !validManagedToolRelease(release) || !managedToolDownloadURL(release.URL) {
		return errors.New("The official release could not be verified.")
	}
	if release.Client == "muse" {
		if !museManagedReleaseValid(release) {
			return errors.New("The official Muse Code release could not be verified.")
		}
		// Muse's release service uses one fixed endpoint. Never follow a Muse
		// download to another vendor or attach a login to a redirected request.
		copy := *client
		copy.Jar = nil
		copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 || !museManagedArtifactURL(req.URL.String(), release.Version, release.AssetName) {
				return errors.New("The Muse Code download redirected outside its verified release.")
			}
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			return nil
		}
		client = &copy
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return errors.New("Cannot prepare the official download.")
	}
	req.Header.Set("User-Agent", "Kilo-Proxy/"+version)
	response, err := client.Do(req)
	if err != nil {
		return errors.New("Cannot download the official release. Check your connection and try again.")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.ContentLength > 2<<30 || release.Size > 0 && response.ContentLength >= 0 && response.ContentLength != release.Size {
		return errors.New("The official download size or response does not match the release.")
	}
	f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("Cannot create the private download file.")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(response.Body, (2<<30)+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n > 2<<30 || release.Size > 0 && n != release.Size || response.ContentLength >= 0 && n != response.ContentLength {
		return errors.New("The official download was incomplete. No installation was changed.")
	}
	if !secureEqual(hex.EncodeToString(hash.Sum(nil)), release.SHA256) {
		return errors.New("The download checksum does not match the official release. No installation was changed.")
	}
	return nil
}

func managedToolInstallDirectory(profile, client string) (string, error) {
	if !managedToolKnown(client) || !filepath.IsAbs(profile) {
		return "", errors.New("Invalid private tools directory.")
	}
	root := profile
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Cannot safely open the private tools directory.")
	}
	for _, part := range []string{"tools", client, "releases"} {
		root = filepath.Join(root, part)
		if err = os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
			return "", errors.New("Cannot create the private tools directory.")
		}
		info, err = os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("The private tools directory must be a private real directory.")
		}
	}
	dir, err := os.MkdirTemp(root, "install-")
	if err != nil {
		return "", errors.New("Cannot prepare the tool installation.")
	}
	return dir, nil
}

func installManagedTool(ctx context.Context, a *app, release managedToolRelease, phase func(string)) (record managedToolRecord, err error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" || !validManagedToolRelease(release) {
		return record, errors.New("Automatic installation is not supported for this tool and system.")
	}
	root, err := managedToolInstallDirectory(a.dir, release.Client)
	if err != nil {
		return record, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(root)
		}
	}()
	archive := filepath.Join(root, "download")
	phase("downloading")
	if err = downloadManagedTool(ctx, managedToolHTTPClient(), release, archive); err != nil {
		return record, err
	}
	phase("verifying")
	if ctx.Err() != nil {
		return record, ctx.Err()
	}
	payload := filepath.Join(root, "payload")
	if err = os.Mkdir(payload, 0700); err != nil {
		return record, errors.New("Cannot prepare the private tool directory.")
	}
	phase("installing")
	if err = extractManagedTool(ctx, archive, payload, release); err != nil {
		return record, err
	}
	if err = validateManagedToolTree(payload); err != nil {
		return record, err
	}
	entry, err := findManagedToolEntry(payload, release.Client, runtime.GOOS)
	if err != nil {
		return record, err
	}
	if runtime.GOOS == "darwin" && strings.HasSuffix(entry, ".app") {
		binary := managedToolBundleExecutable(entry)
		if binary == "" {
			return record, errors.New("The downloaded app has no valid executable.")
		}
		if err = runManagedToolCommand(ctx, payload, "/usr/bin/codesign", "--verify", "--deep", "--strict", entry); err != nil {
			return record, errors.New("The downloaded macOS app failed signature integrity verification.")
		}
	}
	binaries, err := managedToolRuntimeBinaries(entry, runtime.GOOS)
	if err != nil {
		return record, err
	}
	for _, binary := range binaries {
		if err = validateManagedToolBinary(binary, runtime.GOOS, runtime.GOARCH); err != nil {
			return record, err
		}
	}
	if err = verifyManagedToolVersion(ctx, release, entry, runtime.GOOS, runtime.GOARCH); err != nil {
		return record, err
	}
	// Preserve the package's own runtime; only the selected entrypoint is
	// published. No login or preferences are read or written by an installer.
	digest, err := managedToolFingerprint(entry, runtime.GOOS)
	if err != nil {
		return record, errors.New("Cannot verify the installed tool.")
	}
	record = managedToolRecord{Schema: 1, Client: release.Client, Version: release.Version, Channel: release.Channel, Root: root, Executable: entry, Digest: digest}
	if ctx.Err() != nil {
		return record, ctx.Err()
	}
	if err = writeManagedTool(a.dir, record); err != nil {
		return record, err
	}
	committed = true
	_ = os.Remove(archive)
	return record, nil
}

func verifyManagedToolVersion(ctx context.Context, release managedToolRelease, entry, platform, arch string) error {
	installed, channel, err := managedToolInstalledVersion(ctx, release.Client, entry, platform, arch)
	if err != nil || installed != release.Version {
		return errors.New("The downloaded tool version does not match the official release. No installation was changed.")
	}
	// These desktop products expose distinct channel identities in their
	// packaged metadata. Claude Code's version alone cannot prove its lane.
	if (release.Client == "t3-code" || release.Client == "synara" || release.Client == "zed") && channel != release.Channel {
		return errors.New("The downloaded app channel does not match the chosen release. No installation was changed.")
	}
	return nil
}

type managedArchiveLink struct {
	path, target string
	hard         bool
}
type managedArchiveWriter struct {
	root  string
	size  int64
	files int
	links []managedArchiveLink
}

func managedArchivePath(root, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00\r\n") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", errors.New("The archive contains an unsafe path.")
	}
	name = strings.TrimSuffix(name, "/")
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." {
		return root, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("The archive contains an unsafe path.")
	}
	return filepath.Join(root, clean), nil
}

func (w *managedArchiveWriter) add(name string, mode os.FileMode, size int64, reader io.Reader) error {
	w.files++
	if w.files > 100000 || size < 0 || size > managedToolExpandedLimit || w.size > managedToolExpandedLimit-size {
		return errors.New("The extracted release exceeds the installation size limit.")
	}
	w.size += size
	path, err := managedArchivePath(w.root, name)
	if err != nil {
		return err
	}
	if path == w.root {
		if mode.IsDir() {
			return nil
		}
		return errors.New("Invalid archive entry.")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errors.New("Cannot extract the official release.")
	}
	if mode.IsDir() {
		if err = os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return errors.New("Cannot extract the official release.")
		}
		return nil
	}
	if !mode.IsRegular() {
		return errors.New("The archive contains an unsupported file type.")
	}
	permissions := os.FileMode(0600)
	if mode.Perm()&0111 != 0 {
		permissions = 0700
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permissions)
	if err != nil {
		return errors.New("The archive contains a duplicate or unsafe file.")
	}
	n, copyErr := io.Copy(f, io.LimitReader(reader, size+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n != size {
		return errors.New("The official archive is incomplete.")
	}
	return nil
}

func (w *managedArchiveWriter) link(name, target string, hard bool) error {
	w.files++
	if w.files > 100000 {
		return errors.New("The extracted release contains too many files.")
	}
	path, err := managedArchivePath(w.root, name)
	if err != nil || path == w.root {
		return errors.New("The archive contains an unsafe link.")
	}
	if strings.ContainsAny(target, "\\\x00\r\n") || filepath.IsAbs(target) || strings.Contains(target, ":") {
		return errors.New("The archive contains an unsafe link.")
	}
	base := filepath.Dir(path)
	if hard {
		base = w.root
	}
	resolved := filepath.Clean(filepath.Join(base, filepath.FromSlash(target)))
	rel, err := filepath.Rel(w.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("The archive contains an unsafe link.")
	}
	w.links = append(w.links, managedArchiveLink{path, target, hard})
	return nil
}

func (w *managedArchiveWriter) finish() error {
	// Links are created last, so no extracted regular file can traverse one.
	for _, link := range w.links {
		if err := managedArchiveDirectories(w.root, filepath.Dir(link.path)); err != nil {
			return err
		}
		if _, err := os.Lstat(link.path); !os.IsNotExist(err) {
			return errors.New("The archive contains a duplicate link.")
		}
		if link.hard {
			target := filepath.Join(w.root, filepath.FromSlash(link.target))
			info, err := os.Lstat(target)
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("The archive contains an unsafe hard link.")
			}
			if err = os.Link(target, link.path); err != nil {
				return errors.New("Cannot extract the official release links.")
			}
		} else if err := os.Symlink(filepath.FromSlash(link.target), link.path); err != nil {
			return errors.New("Cannot extract the official release links.")
		}
	}
	return validateManagedToolTree(w.root)
}

func managedArchiveDirectories(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("The archive contains an unsafe directory.")
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			err = os.Mkdir(current, 0700)
			if err == nil {
				info, err = os.Lstat(current)
			}
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("An archive link cannot be used as an extraction directory.")
		}
	}
	return nil
}

func extractManagedToolZip(file, root string) error {
	return extractManagedToolZipContext(context.Background(), file, root)
}

func extractManagedToolZipContext(ctx context.Context, file, root string) error {
	z, err := zip.OpenReader(file)
	if err != nil {
		return errors.New("Cannot read the official ZIP archive.")
	}
	defer z.Close()
	w := managedArchiveWriter{root: root}
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.UncompressedSize64 > uint64(managedToolExpandedLimit) {
			return errors.New("The ZIP archive exceeds the installation size limit.")
		}
		f, err := entry.Open()
		if err != nil {
			return errors.New("Cannot read the official ZIP archive.")
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			data, e := io.ReadAll(io.LimitReader(f, 4097))
			f.Close()
			if e != nil || len(data) > 4096 {
				return errors.New("The archive contains an unsafe link.")
			}
			err = w.link(entry.Name, string(data), false)
		} else {
			err = w.add(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), managedToolContextReader{ctx, f})
			f.Close()
		}
		if err != nil {
			return err
		}
	}
	return w.finish()
}

func extractManagedToolTar(reader io.Reader, root string) error {
	return extractManagedToolTarContext(context.Background(), reader, root)
}

type managedToolContextReader struct {
	ctx context.Context
	io.Reader
}

func (r managedToolContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(data)
}

func extractManagedToolTarContext(ctx context.Context, reader io.Reader, root string) error {
	w := managedArchiveWriter{root: root}
	tr := tar.NewReader(managedToolContextReader{ctx, reader})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("Cannot read the official TAR archive.")
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir:
			err = w.add(h.Name, h.FileInfo().Mode(), h.Size, tr)
		case tar.TypeSymlink:
			err = w.link(h.Name, h.Linkname, false)
		case tar.TypeLink:
			err = w.link(h.Name, h.Linkname, true)
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		default:
			return errors.New("The archive contains an unsupported file type.")
		}
		if err != nil {
			return err
		}
	}
	return w.finish()
}

func validateManagedToolTree(root string) error {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("Cannot inspect the extracted release directory.")
	}
	files := 0
	var size int64
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return errors.New("Cannot inspect the extracted release.")
		}
		files++
		if files > 100000 {
			return errors.New("The extracted release contains too many files.")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return errors.New("The extracted release contains a broken or circular link.")
			}
			rel, err := filepath.Rel(canonical, target)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("The extracted release contains a link outside its directory.")
			}
		} else if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("The extracted release contains an unsupported file type.")
		} else if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil || info.Size() < 0 || info.Size() > managedToolExpandedLimit-size {
				return errors.New("The extracted release exceeds the installation size limit.")
			}
			size += info.Size()
		}
		return nil
	})
}

func runManagedToolCommand(ctx context.Context, dir, command string, args ...string) error {
	home := filepath.Join(filepath.Dir(dir), "installer-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return errors.New("Cannot prepare the isolated installer environment.")
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return errors.New("Cannot unpack the verified official release.")
	}
	return nil
}

func extractManagedTool(ctx context.Context, file, root string, release managedToolRelease) error {
	switch release.Format {
	case "zip":
		return extractManagedToolZipContext(ctx, file, root)
	case "tar.gz":
		f, err := os.Open(file)
		if err != nil {
			return errors.New("Cannot open the official archive.")
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return errors.New("Cannot read the official archive.")
		}
		defer gz.Close()
		return extractManagedToolTarContext(ctx, gz, root)
	case "binary":
		name := map[string]string{"codex-cli": "codex", "claude": "claude", "opencode": "opencode", "omp": "omp", "muse": "muse"}[release.Client]
		if name == "" {
			return errors.New("The release format does not match this tool.")
		}
		f, err := os.Open(file)
		if err != nil {
			return errors.New("Cannot open the official release.")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return errors.New("Cannot read the official release.")
		}
		w := managedArchiveWriter{root: root}
		return w.add(name, 0700, info.Size(), managedToolContextReader{ctx, f})
	case "appimage":
		if runtime.GOOS != "linux" {
			return errors.New("This release requires Linux.")
		}
		if err := os.Chmod(file, 0700); err != nil {
			return errors.New("Cannot unpack the verified AppImage.")
		}
		if err := runManagedToolCommand(ctx, root, file, "--appimage-extract"); err != nil {
			return err
		}
		return validateManagedToolTree(root)
	case "dmg":
		if runtime.GOOS != "darwin" {
			return errors.New("This release requires macOS.")
		}
		return extractManagedToolDMG(ctx, file, root, release.Client)
	case "deb":
		return extractManagedToolDeb(ctx, file, root)
	default:
		return errors.New("This official release format is not supported.")
	}
}

func extractManagedToolDMG(ctx context.Context, file, root, client string) (result error) {
	// A mount must never be inside staging: if detach fails, removing staging
	// recursively must not walk the mounted image. Only remove the empty mount
	// directory after a confirmed detach.
	mount, err := os.MkdirTemp("", "kilo-tool-mount-")
	if err != nil {
		return errors.New("Cannot prepare the disk image mount.")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := runManagedToolCommand(cleanup, root, "/usr/bin/hdiutil", "detach", mount); err != nil {
			force, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if err = runManagedToolCommand(force, root, "/usr/bin/hdiutil", "detach", "-force", mount); err != nil {
				// Remove does not descend into a mount. When attach failed without
				// mounting anything this still removes its empty directory.
				if os.Remove(mount) != nil && result == nil {
					result = errors.New("Cannot close the temporary disk image. No installation was changed.")
				}
				return
			}
		}
		_ = os.Remove(mount)
	}()
	if err := runManagedToolCommand(ctx, root, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", mount, "-owners", "off", file); err != nil {
		return errors.New("Cannot open the verified macOS disk image.")
	}
	entry, err := findManagedToolEntry(mount, client, "darwin")
	if err != nil {
		return err
	}
	if err = validateManagedToolTree(entry); err != nil {
		return err
	}
	return runManagedToolCommand(ctx, root, "/usr/bin/ditto", "--noqtn", entry, filepath.Join(root, filepath.Base(entry)))
}

func extractManagedToolDeb(ctx context.Context, file, root string) error {
	f, err := os.Open(file)
	if err != nil {
		return errors.New("Cannot open the official Debian package.")
	}
	defer f.Close()
	magic := make([]byte, 8)
	if _, err = io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return errors.New("Invalid official Debian package.")
	}
	for count := 0; count < 32; count++ {
		header := make([]byte, 60)
		if _, err = io.ReadFull(f, header); err != nil {
			return errors.New("The Debian package has no application payload.")
		}
		if string(header[58:]) != "`\n" {
			return errors.New("Invalid official Debian package header.")
		}
		size, e := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if e != nil || size < 0 || size > 2<<30 {
			return errors.New("Invalid official Debian package size.")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[:16])), "/")
		if name == "data.tar.gz" {
			gz, e := gzip.NewReader(io.LimitReader(f, size))
			if e != nil {
				return errors.New("Cannot read the Debian application payload.")
			}
			defer gz.Close()
			return extractManagedToolTarContext(ctx, gz, root)
		}
		if name == "data.tar.xz" || name == "data.tar.zst" {
			compressed := filepath.Join(filepath.Dir(root), "deb-data")
			out, e := os.OpenFile(compressed, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return errors.New("Cannot unpack the Debian application payload.")
			}
			_, e = io.CopyN(out, f, size)
			ce := out.Close()
			if e != nil || ce != nil {
				return errors.New("The Debian application payload is incomplete.")
			}
			defer os.Remove(compressed)
			return extractManagedToolCompressedTar(ctx, compressed, root, name)
		}
		if name == "data.tar" {
			return extractManagedToolTarContext(ctx, io.LimitReader(f, size), root)
		}
		if _, err = f.Seek(size+size%2, io.SeekCurrent); err != nil {
			return errors.New("Invalid official Debian package.")
		}
	}
	return errors.New("The Debian package has no supported application payload.")
}

func extractManagedToolCompressedTar(ctx context.Context, file, root, format string) error {
	// These are decompressors only: maintainer scripts from a .deb are never
	// run and the system package database is never modified.
	name := "xz"
	if strings.HasSuffix(format, ".zst") {
		name = "zstd"
	}
	command := ""
	for _, base := range []string{"/usr/bin", "/bin", "/opt/homebrew/bin", "/usr/local/bin"} {
		path := filepath.Join(base, name)
		if launchExecutable(path) {
			command = path
			break
		}
	}
	if command == "" {
		return fmt.Errorf("Install %s to unpack this official package, then try again.", name)
	}
	decompressed := filepath.Join(filepath.Dir(root), "deb-payload.tar")
	out, err := os.OpenFile(decompressed, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("Cannot unpack the Debian application payload.")
	}
	defer os.Remove(decompressed)
	cmd := exec.CommandContext(ctx, command, "--decompress", "--stdout", file)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Stderr = io.Discard
	cmd.Stdout = &managedToolLimitedWriter{writer: out, remaining: managedToolExpandedLimit}
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	closeErr := out.Close()
	if err != nil || closeErr != nil {
		return errors.New("Cannot decompress the official application payload.")
	}
	f, err := os.Open(decompressed)
	if err != nil {
		return errors.New("Cannot read the application payload.")
	}
	defer f.Close()
	return extractManagedToolTarContext(ctx, f, root)
}

type managedToolLimitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *managedToolLimitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("The application payload exceeds the size limit.")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func managedToolEntryNames(client, platform string) []string {
	if platform == "darwin" {
		switch client {
		case "codex":
			return []string{"Codex.app", "ChatGPT.app"}
		case "claude-desktop":
			return []string{"Claude.app"}
		case "open-design":
			return []string{"Open Design.app"}
		case "openmausbot":
			return []string{"OpenMausBot.app"}
		case "t3-code":
			return []string{"T3 Code.app", "T3 Code (Nightly).app", "T3 Code (Alpha).app", "T3 Code (Beta).app"}
		case "synara":
			return []string{"Synara.app", "Synara Beta.app", "Synara Alpha.app", "Synara Nightly.app"}
		case "zed":
			return []string{"Zed.app", "Zed Preview.app"}
		}
	}
	switch client {
	case "codex-cli":
		return []string{"codex"}
	case "claude":
		return []string{"claude"}
	case "opencode":
		return []string{"opencode"}
	case "omp":
		return []string{"omp"}
	case "muse":
		return []string{"muse"}
	case "codex":
		return []string{"chatgpt", "ChatGPT", "codex-desktop", "Codex"}
	case "claude-desktop":
		return []string{"claude-desktop", "Claude", "claude"}
	case "openmausbot":
		return []string{"openmausbot", "OpenMausBot"}
	case "t3-code":
		return []string{"t3code", "t3-code", "T3 Code", "T3 Code (Nightly)", "T3 Code (Beta)", "T3 Code (Alpha)"}
	case "synara":
		return []string{"synara-beta", "Synara Beta", "synara", "Synara", "Synara Alpha", "Synara Nightly"}
	case "zed":
		return []string{"zed"}
	}
	return nil
}

func findManagedToolEntry(root, client, platform string) (string, error) {
	if client == "codex-cli" {
		path := filepath.Join(root, "bin", "codex")
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && launchExecutable(path) {
			return path, nil
		}
	}
	names := managedToolEntryNames(client, platform)
	var candidates []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, name := range names {
			if entry.Name() != name {
				continue
			}
			if platform == "darwin" && strings.HasSuffix(name, ".app") && entry.IsDir() {
				candidates = append(candidates, path)
				return filepath.SkipDir
			}
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && launchExecutable(path) {
				candidates = append(candidates, path)
			}
		}
		return nil
	})
	if err != nil || len(candidates) != 1 {
		return "", errors.New("The official package has no unambiguous application entrypoint.")
	}
	return candidates[0], nil
}

func validateManagedToolBinary(path, platform, arch string) error {
	if platform == "darwin" {
		want := macho.CpuAmd64
		if arch == "arm64" {
			want = macho.CpuArm64
		}
		f, err := macho.Open(path)
		if err == nil {
			defer f.Close()
			if f.Cpu == want {
				return nil
			}
		}
		fat, err := macho.OpenFat(path)
		if err == nil {
			defer fat.Close()
			for _, entry := range fat.Arches {
				if entry.Cpu == want {
					return nil
				}
			}
		}
	} else if platform == "linux" {
		want := elf.EM_X86_64
		if arch == "arm64" {
			want = elf.EM_AARCH64
		}
		f, err := elf.Open(path)
		if err == nil {
			defer f.Close()
			if f.Machine == want && f.Class == elf.ELFCLASS64 {
				return nil
			}
		}
	}
	return errors.New("The downloaded executable does not match this operating system and architecture.")
}
