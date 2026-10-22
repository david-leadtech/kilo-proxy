package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const omnigentCodexSelectionError = "Omnigent requires the selected native Codex CLI or its matching official npm package. Choose its native executable or repair that selected installation; no other Codex installation will be used."

// The observed official npm entrypoint is never executed by detection. Unknown
// wrappers are rejected instead of searching unrelated installs or ancestors.
var omnigentCodexWrapperHashes = map[string]bool{
	"61b0194f3bb6534439c8d26a3ed57d0805f84b884588b761795323eeb92fcf70": true,
}
var omnigentCodexPackageVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

type omnigentCodexPackage struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Bin                  map[string]string `json:"bin"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	OS                   []string          `json:"os"`
	CPU                  []string          `json:"cpu"`
}

func resolveOmnigentCodexCLI(rt clientLaunchRuntime) (string, error) {
	path, _, err := resolveOmnigentCodexSelection(rt)
	return path, err
}
func resolveOmnigentCodexSelection(rt clientLaunchRuntime) (string, string, error) {
	selected, err := rt.resolve("codex-cli", "")
	if err != nil {
		return "", "", err
	}
	return resolveOmnigentCodexPackage(selected, rt.platform, runtime.GOARCH)
}
func readOmnigentCodexPackage(path string) (omnigentCodexPackage, []byte, error) {
	var p omnigentCodexPackage
	data, err := omnigentReadRegular(path, 1<<20)
	if err != nil {
		return p, nil, err
	}
	if _, err = decodeClaudeDesktopObject(data); err != nil {
		return p, nil, err
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, nil, err
	}
	return p, data, nil
}
func resolveOmnigentCodexPackage(selected, platform, arch string) (string, string, error) {
	fail := errors.New(omnigentCodexSelectionError)
	if !filepath.IsAbs(selected) || strings.ContainsAny(selected, "\x00\r\n") {
		return "", "", fail
	}
	real, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return "", "", fail
	}
	provenance := map[string]string{"selected": selected, "canonicalSelected": real}
	finish := func(native string) (string, string, error) {
		provenance["canonicalNative"] = native
		raw, _ := json.Marshal(provenance)
		return native, omnigentHash(raw), nil
	}
	if omnigentNativeCodex(real, platform, arch) {
		return finish(real)
	}
	if filepath.Base(real) != "codex.js" || filepath.Base(filepath.Dir(real)) != "bin" {
		return "", "", fail
	}
	wrapper, err := omnigentReadRegular(real, 64<<10)
	if err != nil || !omnigentCodexWrapperHashes[omnigentHash(wrapper)] {
		return "", "", fail
	}
	root := filepath.Dir(filepath.Dir(real))
	main, raw, err := readOmnigentCodexPackage(filepath.Join(root, "package.json"))
	if err != nil || main.Name != "@openai/codex" || !omnigentCodexPackageVersion.MatchString(main.Version) || main.Bin["codex"] != "bin/codex.js" {
		return "", "", fail
	}
	provenance["wrapperSha256"], provenance["mainPackageSha256"], provenance["mainPackageRoot"] = omnigentHash(wrapper), omnigentHash(raw), root
	suffix, target := openDesignCodexTarget(platform, arch)
	if suffix == "" {
		return "", "", fail
	}
	dependency := "@openai/codex-" + suffix
	expected := main.OptionalDependencies[dependency]
	if expected != "" && expected != main.Version && expected != "npm:@openai/codex@"+main.Version+"-"+suffix {
		return "", "", fail
	}
	roots := []string{filepath.Join(root, "node_modules", "@openai", "codex-"+suffix)}
	// A direct sibling is the only outer dependency location considered. This
	// mirrors the selected @openai package scope without an ancestor search.
	if filepath.Base(root) == "codex" && filepath.Base(filepath.Dir(root)) == "@openai" && filepath.Base(filepath.Dir(filepath.Dir(root))) == "node_modules" {
		roots = append(roots, filepath.Join(filepath.Dir(root), "codex-"+suffix))
	}
	nativeRoot := root
	for _, candidate := range roots {
		if _, err = os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", "", fail
		}
		if expected == "" {
			return "", "", fail
		}
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", fail
		}
		pkg, raw, err := readOmnigentCodexPackage(filepath.Join(canonical, "package.json"))
		if err != nil {
			return "", "", fail
		}
		npmOS := platform
		if npmOS == "macos" {
			npmOS = "darwin"
		}
		if npmOS == "windows" {
			npmOS = "win32"
		}
		npmArch := arch
		if npmArch == "amd64" {
			npmArch = "x64"
		}
		direct := expected == main.Version && pkg.Name == dependency && pkg.Version == main.Version
		alias := expected == "npm:@openai/codex@"+main.Version+"-"+suffix && pkg.Name == "@openai/codex" && pkg.Version == main.Version+"-"+suffix
		if !(direct || alias) || len(pkg.OS) != 1 || pkg.OS[0] != npmOS || len(pkg.CPU) != 1 || pkg.CPU[0] != npmArch {
			return "", "", fail
		}
		provenance["platformPackageRoot"], provenance["platformPackageSha256"] = canonical, omnigentHash(raw)
		nativeRoot = canonical
		break
	}
	name := "codex"
	if platform == "windows" {
		name += ".exe"
	}
	for _, layout := range []string{"bin", "codex"} {
		candidate := filepath.Join(nativeRoot, "vendor", target, layout, name)
		if _, err = os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", "", fail
		}
		canonical, err := filepath.EvalSymlinks(candidate)
		if err != nil || !withinT3CodePath(nativeRoot, canonical) || !omnigentNativeCodex(canonical, platform, arch) {
			return "", "", fail
		}
		return finish(canonical)
	}
	return "", "", fail
}

// Open nonblocking before checking the descriptor, so replacing a selected
// executable with a FIFO cannot hang discovery. No wrapper or binary is run.
func omnigentNativeCodex(path, platform, arch string) bool {
	f, err := synaraOpenCandidate(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return false
	}
	if runtime.GOOS != "windows" && platform != "windows" && info.Mode().Perm()&0111 == 0 {
		return false
	}
	var header [64]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return false
	}
	switch platform {
	case "darwin", "macos":
		cpu := uint32(0x01000007)
		if arch == "arm64" {
			cpu = 0x0100000c
		} else if arch != "amd64" {
			return false
		}
		magic := binary.BigEndian.Uint32(header[:4])
		if magic == 0xcffaedfe {
			return binary.LittleEndian.Uint32(header[4:8]) == cpu
		}
		if magic == 0xfeedfacf {
			return binary.BigEndian.Uint32(header[4:8]) == cpu
		}
		if magic == 0xcafebabe || magic == 0xcafebabf {
			count := binary.BigEndian.Uint32(header[4:8])
			size := int64(20)
			if magic == 0xcafebabf {
				size = 32
			}
			if count < 1 || count > 32 {
				return false
			}
			for i := uint32(0); i < count; i++ {
				var entry [4]byte
				if _, err = f.ReadAt(entry[:], 8+int64(i)*size); err != nil {
					return false
				}
				if binary.BigEndian.Uint32(entry[:]) == cpu {
					return true
				}
			}
		}
	case "linux":
		machine := uint16(62)
		if arch == "arm64" {
			machine = 183
		} else if arch != "amd64" {
			return false
		}
		return bytes.Equal(header[:6], []byte{0x7f, 'E', 'L', 'F', 2, 1}) && binary.LittleEndian.Uint16(header[18:20]) == machine
	case "windows":
		if !bytes.Equal(header[:2], []byte("MZ")) {
			return false
		}
		offset := int64(binary.LittleEndian.Uint32(header[60:64]))
		if offset < 64 || offset > info.Size()-6 {
			return false
		}
		var pe [6]byte
		if _, err = f.ReadAt(pe[:], offset); err != nil || !bytes.Equal(pe[:4], []byte("PE\x00\x00")) {
			return false
		}
		machine := uint16(0x8664)
		if arch == "arm64" {
			machine = 0xaa64
		} else if arch != "amd64" {
			return false
		}
		return binary.LittleEndian.Uint16(pe[4:6]) == machine
	}
	return false
}
