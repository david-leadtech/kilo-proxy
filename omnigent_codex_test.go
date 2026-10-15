package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exact official @openai/codex npm entrypoint observed at 0.162.1. Fixture
// bytes are inspected only and are never executed by these tests.
const omnigentCodexWrapperFixture = "#!/usr/bin/env node\n// Unified entry point for the Codex CLI.\n\nimport { spawn } from \"node:child_process\";\nimport { existsSync, readFileSync, realpathSync } from \"fs\";\nimport { createRequire } from \"node:module\";\nimport path from \"path\";\nimport { fileURLToPath } from \"url\";\n\n// __dirname equivalent in ESM\nconst __filename = fileURLToPath(import.meta.url);\nconst __dirname = path.dirname(__filename);\nconst require = createRequire(import.meta.url);\nconst codexPackageRoot = realpathSync(path.join(__dirname, \"..\"));\n\nconst PLATFORM_PACKAGE_BY_TARGET = {\n  \"x86_64-unknown-linux-musl\": \"@openai/codex-linux-x64\",\n  \"aarch64-unknown-linux-musl\": \"@openai/codex-linux-arm64\",\n  \"x86_64-apple-darwin\": \"@openai/codex-darwin-x64\",\n  \"aarch64-apple-darwin\": \"@openai/codex-darwin-arm64\",\n  \"x86_64-pc-windows-msvc\": \"@openai/codex-win32-x64\",\n  \"aarch64-pc-windows-msvc\": \"@openai/codex-win32-arm64\",\n};\n\nconst { platform, arch } = process;\n\nlet targetTriple = null;\nswitch (platform) {\n  case \"linux\":\n  case \"android\":\n    switch (arch) {\n      case \"x64\":\n        targetTriple = \"x86_64-unknown-linux-musl\";\n        break;\n      case \"arm64\":\n        targetTriple = \"aarch64-unknown-linux-musl\";\n        break;\n      default:\n        break;\n    }\n    break;\n  case \"darwin\":\n    switch (arch) {\n      case \"x64\":\n        targetTriple = \"x86_64-apple-darwin\";\n        break;\n      case \"arm64\":\n        targetTriple = \"aarch64-apple-darwin\";\n        break;\n      default:\n        break;\n    }\n    break;\n  case \"win32\":\n    switch (arch) {\n      case \"x64\":\n        targetTriple = \"x86_64-pc-windows-msvc\";\n        break;\n      case \"arm64\":\n        targetTriple = \"aarch64-pc-windows-msvc\";\n        break;\n      default:\n        break;\n    }\n    break;\n  default:\n    break;\n}\n\nif (!targetTriple) {\n  throw new Error(`Unsupported platform: ${platform} (${arch})`);\n}\n\nconst platformPackage = PLATFORM_PACKAGE_BY_TARGET[targetTriple];\nif (!platformPackage) {\n  throw new Error(`Unsupported target triple: ${targetTriple}`);\n}\n\nfunction findCodexExecutable() {\n  let vendorRoot;\n  try {\n    const packageJsonPath = require.resolve(`${platformPackage}/package.json`);\n    vendorRoot = path.join(path.dirname(packageJsonPath), \"vendor\");\n  } catch {\n    vendorRoot = path.join(__dirname, \"..\", \"vendor\");\n  }\n\n  const codexExecutable = path.join(\n    vendorRoot,\n    targetTriple,\n    \"bin\",\n    process.platform === \"win32\" ? \"codex.exe\" : \"codex\",\n  );\n  if (existsSync(codexExecutable)) {\n    return codexExecutable;\n  }\n\n  const packageManager = detectPackageManager();\n  const updateCommand =\n    packageManager === \"bun\"\n      ? \"bun install -g @openai/codex@latest\"\n      : packageManager === \"pnpm\"\n        ? \"pnpm add -g @openai/codex@latest\"\n        : packageManager === \"vite-plus\"\n          ? \"vp install -g @openai/codex@latest\"\n          : \"npm install -g @openai/codex@latest\";\n  throw new Error(\n    `Missing optional dependency ${platformPackage}. Reinstall Codex: ${updateCommand}`,\n  );\n}\n\nconst binaryPath = findCodexExecutable();\n\n// Use an asynchronous spawn instead of spawnSync so that Node is able to\n// respond to signals (e.g. Ctrl-C / SIGINT) while the native binary is\n// executing. This allows us to forward those signals to the child process\n// and guarantees that when either the child terminates or the parent\n// receives a fatal signal, both processes exit in a predictable manner.\n\nfunction isPnpmOwnedCodexInstall(nodeModulesDir) {\n  if (!existsSync(path.join(nodeModulesDir, \".modules.yaml\"))) {\n    return false;\n  }\n\n  try {\n    return (\n      realpathSync(path.join(nodeModulesDir, \"@openai\", \"codex\")) ===\n      codexPackageRoot\n    );\n  } catch {\n    return false;\n  }\n}\n\nfunction isVitePlusOwnedCodexInstall(packagesDir) {\n  if (path.basename(packagesDir) !== \"packages\") {\n    return false;\n  }\n\n  try {\n    const metadata = JSON.parse(\n      readFileSync(path.join(packagesDir, \"@openai\", \"codex.json\"), \"utf8\"),\n    );\n    if (metadata.name !== \"@openai/codex\") {\n      return false;\n    }\n\n    // Vite+ records the active global installation in packages/@openai/codex.json.\n    // Older installs have no ID or append a #-prefixed ID to the package name;\n    // newer installs put the ID in a subdirectory of the package prefix.\n    const installId = metadata.installId || \"\";\n    const installDir = installId.startsWith(\"#\")\n      ? path.join(packagesDir, `@openai/codex${installId}`)\n      : path.join(packagesDir, \"@openai/codex\", installId);\n    for (const nodeModulesDir of [\n      path.join(installDir, \"lib\", \"node_modules\"),\n      path.join(installDir, \"node_modules\"),\n    ]) {\n      const packageRoot = path.join(nodeModulesDir, \"@openai\", \"codex\");\n      if (\n        existsSync(packageRoot) &&\n        realpathSync(packageRoot) === codexPackageRoot\n      ) {\n        return true;\n      }\n    }\n  } catch {\n    // Missing or unreadable ownership metadata must not prevent Codex starting.\n  }\n  return false;\n}\n\n/**\n * Use heuristics to detect the package manager that was used to install Codex\n * in order to give the user a hint about how to update it.\n */\nfunction detectPackageManager() {\n  // Package-manager ownership metadata can be several parents above the package.\n  // Search ancestors of both the canonical package root and lexical entrypoint\n  // because the package manager may link either path.\n  const entrypointDir = path.dirname(path.resolve(process.argv[1]));\n  for (const startDir of new Set([codexPackageRoot, entrypointDir])) {\n    const filesystemRoot = path.parse(startDir).root;\n    for (\n      let currentDir = startDir;\n      currentDir !== filesystemRoot;\n      currentDir = path.dirname(currentDir)\n    ) {\n      if (isVitePlusOwnedCodexInstall(currentDir)) {\n        return \"vite-plus\";\n      }\n      if (isPnpmOwnedCodexInstall(path.join(currentDir, \"node_modules\"))) {\n        return \"pnpm\";\n      }\n    }\n\n    if (isPnpmOwnedCodexInstall(path.join(filesystemRoot, \"node_modules\"))) {\n      return \"pnpm\";\n    }\n  }\n\n  const userAgent = process.env.npm_config_user_agent || \"\";\n  if (/\\bbun\\//.test(userAgent)) {\n    return \"bun\";\n  }\n\n  const execPath = process.env.npm_execpath || \"\";\n  if (execPath.includes(\"bun\")) {\n    return \"bun\";\n  }\n\n  if (\n    __dirname.includes(\".bun/install/global\") ||\n    __dirname.includes(\".bun\\\\install\\\\global\")\n  ) {\n    return \"bun\";\n  }\n\n  return userAgent ? \"npm\" : null;\n}\n\nconst packageManager = detectPackageManager();\nconst packageManagerEnvVar =\n  packageManager === \"bun\"\n    ? \"CODEX_MANAGED_BY_BUN\"\n    : packageManager === \"pnpm\"\n      ? \"CODEX_MANAGED_BY_PNPM\"\n      : packageManager === \"vite-plus\"\n        ? \"CODEX_MANAGED_BY_VITE_PLUS\"\n        : \"CODEX_MANAGED_BY_NPM\";\nconst env = {\n  ...process.env,\n  CODEX_MANAGED_PACKAGE_ROOT: codexPackageRoot,\n};\ndelete env.CODEX_MANAGED_BY_NPM;\ndelete env.CODEX_MANAGED_BY_BUN;\ndelete env.CODEX_MANAGED_BY_PNPM;\ndelete env.CODEX_MANAGED_BY_VITE_PLUS;\nenv[packageManagerEnvVar] = \"1\";\n\nconst child = spawn(binaryPath, process.argv.slice(2), {\n  stdio: \"inherit\",\n  env,\n});\n\nchild.on(\"error\", (err) => {\n  // Typically triggered when the binary is missing or not executable.\n  // Re-throwing here will terminate the parent with a non-zero exit code\n  // while still printing a helpful stack trace.\n  // eslint-disable-next-line no-console\n  console.error(err);\n  process.exit(1);\n});\n\n// Forward common termination signals to the child so that it shuts down\n// gracefully. In the handler we temporarily disable the default behavior of\n// exiting immediately; once the child has been signaled we simply wait for\n// its exit event which will in turn terminate the parent (see below).\nconst forwardSignal = (signal) => {\n  if (child.killed) {\n    return;\n  }\n  try {\n    child.kill(signal);\n  } catch {\n    /* ignore */\n  }\n};\n\n[\"SIGINT\", \"SIGTERM\", \"SIGHUP\"].forEach((sig) => {\n  process.on(sig, () => forwardSignal(sig));\n});\n\n// When the child exits, mirror its termination reason in the parent so that\n// shell scripts and other tooling observe the correct exit status.\n// Wrap the lifetime of the child process in a Promise so that we can await\n// its termination in a structured way. The Promise resolves with an object\n// describing how the child exited: either via exit code or due to a signal.\nconst childResult = await new Promise((resolve) => {\n  child.on(\"exit\", (code, signal) => {\n    if (signal) {\n      resolve({ type: \"signal\", signal });\n    } else {\n      resolve({ type: \"code\", exitCode: code ?? 1 });\n    }\n  });\n});\n\nif (childResult.type === \"signal\") {\n  // Re-emit the same signal so that the parent terminates with the expected\n  // semantics (this also sets the correct exit code of 128 + n).\n  process.kill(process.pid, childResult.signal);\n} else {\n  process.exit(childResult.exitCode);\n}\n"

func omnigentCodexFixtureWrite(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
func omnigentCodexHeader(platform, arch string) []byte {
	if platform != "linux" {
		return openDesignCodexNativeFixture(platform, arch)
	}
	header := make([]byte, 128)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(header[18:20], machine)
	return header
}
func omnigentCodexNpmFixture(t *testing.T, platform, arch, layout, location string) (wrapper, native, mainMetadata, platformMetadata string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "node_modules", "@openai", "codex")
	wrapper = omnigentCodexFixtureWrite(t, filepath.Join(root, "bin", "codex.js"), []byte(omnigentCodexWrapperFixture))
	suffix, target := openDesignCodexTarget(platform, arch)
	dep := "@openai/codex-" + suffix
	main := omnigentCodexPackage{Name: "@openai/codex", Version: "0.162.1", Bin: map[string]string{"codex": "bin/codex.js"}, OptionalDependencies: map[string]string{dep: "npm:@openai/codex@0.162.1-" + suffix}}
	raw, _ := json.Marshal(main)
	mainMetadata = omnigentCodexFixtureWrite(t, filepath.Join(root, "package.json"), raw)
	packageRoot := root
	if location != "vendored" {
		packageRoot = filepath.Join(root, "node_modules", "@openai", "codex-"+suffix)
		if location == "sibling" {
			packageRoot = filepath.Join(filepath.Dir(root), "codex-"+suffix)
		}
		system, cpu := platform, arch
		if system == "macos" {
			system = "darwin"
		}
		if system == "windows" {
			system = "win32"
		}
		if cpu == "amd64" {
			cpu = "x64"
		}
		platformPackage := omnigentCodexPackage{Name: "@openai/codex", Version: "0.162.1-" + suffix, OS: []string{system}, CPU: []string{cpu}}
		raw, _ = json.Marshal(platformPackage)
		platformMetadata = omnigentCodexFixtureWrite(t, filepath.Join(packageRoot, "package.json"), raw)
	}
	name := "codex"
	if platform == "windows" {
		name += ".exe"
	}
	native = omnigentCodexFixtureWrite(t, filepath.Join(packageRoot, "vendor", target, layout, name), omnigentCodexHeader(platform, arch))
	return
}
func TestOmnigentCodexResolvesOnlySelectedPackageAndBothVendorLayouts(t *testing.T) {
	for _, platform := range []string{"linux", "macos", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			for _, layout := range []string{"bin", "codex"} {
				for _, location := range []string{"nested", "sibling", "vendored"} {
					t.Run(platform+"/"+arch+"/"+layout+"/"+location, func(t *testing.T) {
						wrapper, want, _, _ := omnigentCodexNpmFixture(t, platform, arch, layout, location)
						got, identity, err := resolveOmnigentCodexPackage(wrapper, platform, arch)
						if err != nil || got != want || identity == "" {
							t.Fatalf("got %q, %q, %v; want %q", got, identity, err, want)
						}
						if got, _, err = resolveOmnigentCodexPackage(want, platform, arch); err != nil || got != want {
							t.Fatalf("native selection changed: %q %v", got, err)
						}
					})
				}
			}
		}
	}
}
func TestOmnigentCodexPackageRejectsUnboundOrChangedInstallation(t *testing.T) {
	for _, change := range []string{"wrapper", "name", "version", "bin", "dependency", "platform-name", "platform-version", "platform-os", "platform-cpu", "duplicate", "native-header"} {
		t.Run(change, func(t *testing.T) {
			wrapper, native, mainPath, platformPath := omnigentCodexNpmFixture(t, "linux", "arm64", "bin", "nested")
			main, _, err := readOmnigentCodexPackage(mainPath)
			if err != nil {
				t.Fatal(err)
			}
			pkg, _, err := readOmnigentCodexPackage(platformPath)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "wrapper":
				omnigentCodexFixtureWrite(t, wrapper, []byte(omnigentCodexWrapperFixture+"\n// changed entrypoint"))
			case "name":
				main.Name = "another-package"
			case "version":
				main.Version = "0.163.0"
			case "bin":
				main.Bin["codex"] = "../../foreign.js"
			case "dependency":
				main.OptionalDependencies["@openai/codex-linux-arm64"] = "npm:@openai/codex@0.163.0-linux-arm64"
			case "platform-name":
				pkg.Name = "foreign-package"
			case "platform-version":
				pkg.Version = "0.160.0-linux-arm64"
			case "platform-os":
				pkg.OS = []string{"darwin"}
			case "platform-cpu":
				pkg.CPU = []string{"x64"}
			case "native-header":
				omnigentCodexFixtureWrite(t, native, []byte("#!/bin/sh\nexit 0\n"))
			}
			raw, _ := json.Marshal(main)
			omnigentCodexFixtureWrite(t, mainPath, raw)
			raw, _ = json.Marshal(pkg)
			omnigentCodexFixtureWrite(t, platformPath, raw)
			if change == "duplicate" {
				omnigentCodexFixtureWrite(t, mainPath, []byte(`{"name":"foreign","name":"@openai/codex","version":"0.162.1","bin":{"codex":"bin/codex.js"}}`))
			}
			// An unrelated desktop/native candidate must never rescue this selection.
			omnigentCodexFixtureWrite(t, filepath.Join(t.TempDir(), "Applications", "Codex.app", "Contents", "Resources", "codex"), omnigentCodexHeader("linux", "arm64"))
			got, _, err := resolveOmnigentCodexPackage(wrapper, "linux", "arm64")
			if err == nil || got != "" || err.Error() != omnigentCodexSelectionError {
				t.Fatalf("changed selected package accepted: %q %v", got, err)
			}
		})
	}
}
func TestOmnigentCodexMissingOwnPackageNeverSearchesAncestors(t *testing.T) {
	base := t.TempDir()
	wrapper := omnigentCodexFixtureWrite(t, filepath.Join(base, "selected", "bin", "codex"), []byte("#!/usr/bin/env node\n"))
	suffix, target := openDesignCodexTarget("macos", "arm64")
	omnigentCodexFixtureWrite(t, filepath.Join(base, "node_modules", "@openai", "codex-"+suffix, "vendor", target, "bin", "codex"), omnigentCodexHeader("macos", "arm64"))
	omnigentCodexFixtureWrite(t, filepath.Join(base, "Applications", "Codex.app", "Contents", "Resources", "codex"), omnigentCodexHeader("macos", "arm64"))
	if got, _, err := resolveOmnigentCodexPackage(wrapper, "macos", "arm64"); err == nil || got != "" {
		t.Fatalf("unrelated native candidate adopted: %q %v", got, err)
	}
}
func TestOmnigentCodexProvenanceBindsPackageBytesAndSelectedAlias(t *testing.T) {
	wrapper, native, mainPath, _ := omnigentCodexNpmFixture(t, "linux", "arm64", "bin", "nested")
	_, before, err := resolveOmnigentCodexPackage(wrapper, "linux", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	omnigentCodexFixtureWrite(t, mainPath, append(raw, '\n'))
	got, after, err := resolveOmnigentCodexPackage(wrapper, "linux", "arm64")
	if err != nil || got != native || before == after {
		t.Fatalf("metadata change did not revoke identity: %q %v", got, err)
	}
	t.Run("alias", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "codex")
		if err := os.Symlink(wrapper, alias); err != nil {
			t.Skipf("synthetic symlink unavailable: %v", err)
		}
		got, aliased, err := resolveOmnigentCodexPackage(alias, "linux", "arm64")
		if err != nil || got != native || aliased == after {
			t.Fatalf("selected alias not bound: %q %v", got, err)
		}
	})
	t.Run("native-escape", func(t *testing.T) {
		foreign := omnigentCodexFixtureWrite(t, filepath.Join(t.TempDir(), "codex"), omnigentCodexHeader("linux", "arm64"))
		if err := os.Remove(native); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(foreign, native); err != nil {
			t.Skipf("synthetic symlink unavailable: %v", err)
		}
		if got, _, err := resolveOmnigentCodexPackage(wrapper, "linux", "arm64"); err == nil || got != "" {
			t.Fatal("native alias escaped the selected package")
		}
	})
}
func TestOmnigentCodexRuntimeIdentityIncludesSelectionProvenance(t *testing.T) {
	o := omnigentRuntimeTestOptions(t)
	before, err := omnigentRuntimeIdentity(o)
	if err != nil {
		t.Fatal(err)
	}
	o.CodexSelectionFingerprint = "different-selected-wrapper-provenance"
	after, err := omnigentRuntimeIdentity(o)
	if err != nil || before == after {
		t.Fatal("wrapper provenance did not revoke runtime identity", err)
	}
}
func TestOmnigentCodexResolutionUsesOnlySelectedCLIAndPreservesErrors(t *testing.T) {
	path := omnigentCodexFixtureWrite(t, filepath.Join(t.TempDir(), "codex"), omnigentCodexHeader("linux", runtime.GOARCH))
	calls := 0
	rt := clientLaunchRuntime{platform: "linux", resolve: func(id, custom string) (string, error) {
		calls++
		if id != "codex-cli" || custom != "" {
			t.Fatal("unrelated installation lookup")
		}
		return path, nil
	}}
	got, _, err := resolveOmnigentCodexSelection(rt)
	if err != nil || got != path || calls != 1 {
		t.Fatalf("wrong selection: %q %v calls=%d", got, err, calls)
	}
	expected := errors.New("selected path is unavailable")
	rt.resolve = func(string, string) (string, error) { return "", expected }
	if _, err := resolveOmnigentCodexCLI(rt); !errors.Is(err, expected) {
		t.Fatal("selection failure changed", err)
	}
	for _, bad := range []string{"relative", path + "\n", filepath.Dir(path)} {
		if got, _, err := resolveOmnigentCodexPackage(bad, "linux", runtime.GOARCH); err == nil || strings.TrimSpace(got) != "" {
			t.Fatal("invalid selected executable accepted", bad)
		}
	}
}
