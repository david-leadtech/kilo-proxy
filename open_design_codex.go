package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Open Design starts Codex without an interactive shell. An npm wrapper can
// exist even when its optional native package is missing, so pin a native
// executable for that workflow without changing ordinary terminal discovery.
func resolveOpenDesignCLI(engine string, rt clientLaunchRuntime) (string, error) {
	selected, err := rt.resolve(engine, "")
	if err != nil || engine != "codex-cli" {
		return selected, err
	}
	return resolveOpenDesignCodex(selected, rt.platform, runtime.GOARCH, []string{"/Applications", filepath.Join(rt.home, "Applications")})
}

func resolveOpenDesignCodex(selected, platform, arch string, applicationRoots []string) (string, error) {
	real, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return "", errors.New("Cannot resolve the detected Codex CLI executable.")
	}
	f, err := os.Open(real)
	if err != nil {
		return "", errors.New("Cannot inspect the detected Codex CLI executable.")
	}
	var header [256]byte
	n, _ := f.Read(header[:])
	_ = f.Close()
	ext := strings.ToLower(filepath.Ext(real))
	wrapper := bytes.HasPrefix(header[:n], []byte("#!")) || ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".cmd" || ext == ".bat"
	if !wrapper {
		return selected, nil
	}
	candidates := []string{}
	if platform == "macos" || platform == "darwin" {
		for _, root := range applicationRoots {
			for _, bundle := range []string{"Codex.app", "ChatGPT.app"} {
				candidates = append(candidates, filepath.Join(root, bundle, "Contents", "Resources", "codex"))
			}
		}
	}
	suffix, target := openDesignCodexTarget(platform, arch)
	if suffix != "" {
		name := "codex"
		if platform == "windows" {
			name += ".exe"
		}
		for _, seed := range []string{selected, real} {
			for dir, depth := filepath.Dir(seed), 0; depth < 12; depth++ {
				// Windows npm .cmd launchers are files, not links into the
				// package; include its nested optional-dependency directory.
				for _, root := range []string{dir, filepath.Join(dir, "node_modules", "@openai", "codex")} {
					pkg := filepath.Join(root, "node_modules", "@openai", "codex-"+suffix)
					candidates = append(candidates,
						filepath.Join(pkg, "vendor", target, "codex", name),
						filepath.Join(pkg, name), filepath.Join(pkg, "bin", name),
						filepath.Join(root, "vendor", target, "codex", name))
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
	}
	for _, candidate := range candidates {
		path, err := filepath.EvalSymlinks(candidate)
		if err == nil && openDesignNativeCodex(path, platform, arch) {
			return path, nil
		}
	}
	return "", errors.New("Open Design needs a native Codex executable. Repair the Codex CLI installation or install Codex Desktop, then refresh installed apps.")
}

func openDesignCodexTarget(platform, arch string) (string, string) {
	cpu, npmArch := "", ""
	switch arch {
	case "arm64":
		cpu, npmArch = "aarch64", "arm64"
	case "amd64":
		cpu, npmArch = "x86_64", "x64"
	default:
		return "", ""
	}
	switch platform {
	case "darwin", "macos":
		return "darwin-" + npmArch, cpu + "-apple-darwin"
	case "windows":
		return "win32-" + npmArch, cpu + "-pc-windows-msvc"
	case "linux":
		return "linux-" + npmArch, cpu + "-unknown-linux-musl"
	}
	return "", ""
}

// Inspect only executable headers; discovery never executes a package wrapper
// or the discovered binary. Architecture mismatches are skipped.
func openDesignNativeCodex(path, platform, arch string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	// Windows does not expose Unix executable bits, even when inspecting a
	// different platform's header. Keep the permission check on Unix hosts.
	if runtime.GOOS != "windows" && platform != "windows" && info.Mode().Perm()&0111 == 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var header [64]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return false
	}
	switch platform {
	case "macos", "darwin":
		magic := binary.BigEndian.Uint32(header[:4])
		cpu := uint32(0x01000007)
		if arch == "arm64" {
			cpu = 0x0100000c
		}
		if magic == 0xcffaedfe {
			return binary.LittleEndian.Uint32(header[4:8]) == cpu
		}
		if magic == 0xfeedfacf {
			return binary.BigEndian.Uint32(header[4:8]) == cpu
		}
		// A universal Mach-O contains one architecture record per slice.
		if magic == 0xcafebabe || magic == 0xcafebabf {
			count := binary.BigEndian.Uint32(header[4:8])
			size := int64(20)
			if magic == 0xcafebabf {
				size = 32
			}
			for i := uint32(0); i < count && i < 32; i++ {
				var entry [4]byte
				if _, err := f.ReadAt(entry[:], 8+int64(i)*size); err != nil {
					return false
				}
				if binary.BigEndian.Uint32(entry[:]) == cpu {
					return true
				}
			}
		}
	case "windows":
		if !strings.EqualFold(filepath.Ext(path), ".exe") || !bytes.Equal(header[:2], []byte("MZ")) {
			return false
		}
		offset := int64(binary.LittleEndian.Uint32(header[60:64]))
		var pe [6]byte
		if offset < 64 || offset > info.Size()-6 {
			return false
		}
		if _, err := f.ReadAt(pe[:], offset); err != nil || !bytes.Equal(pe[:4], []byte("PE\x00\x00")) {
			return false
		}
		machine := uint16(0x8664)
		if arch == "arm64" {
			machine = 0xaa64
		}
		return binary.LittleEndian.Uint16(pe[4:6]) == machine
	case "linux":
		machine := uint16(62)
		if arch == "arm64" {
			machine = 183
		}
		return bytes.Equal(header[:6], []byte{0x7f, 'E', 'L', 'F', 2, 1}) && binary.LittleEndian.Uint16(header[18:20]) == machine
	}
	return false
}
