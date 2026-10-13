# Install and update agent tools

Kilo Proxy can check installed versions and prepare a separate, private installation of supported tools on macOS and Linux. Use **Check version**, choose an available channel where offered, then **Install** or **Update**. Downloads come from fixed upstream feeds and must match the SHA256 published in the release, native package metadata, or APT package index.

Managed copies live inside Kilo Proxy's profile under `tools`. Installing one does not overwrite an existing application, a Homebrew/npm/APT installation, a login, or a chat workspace. Kilo uses the managed copy after its package has been validated. A custom Codex application path keeps priority until you explicitly choose **Use Kilo copy**. Updating the executable does not sign in to a provider or install its system libraries. Windows installations and updates remain manual.

## Packages

`x64` means AMD64/Intel; `ARM64` includes Apple Silicon. Availability below follows the packages upstream actually publishes; a missing architecture never falls back to an incompatible executable. Linux Electron applications also require the host's sandbox support described below.

| Tool | macOS x64 / ARM64 | Linux x64 / ARM64 | Feed and package |
| --- | --- | --- | --- |
| Codex desktop / ChatGPT | Both | Both | macOS: the current [ChatGPT Homebrew cask metadata](https://formulae.brew.sh/api/cask/chatgpt.json), downloading the official OpenAI ZIP. Linux: the [official signed APT repository](https://persistent.oaistatic.com/codex-app-prod/linux/deb/dists/stable/Release), downloading the versioned `chatgpt` DEB. |
| Codex CLI | Both | Both | [OpenAI GitHub releases](https://github.com/openai/codex/releases): complete `codex-package-*.tar.gz` packages, including their bundled runtime. Linux uses the upstream musl package. |
| Claude desktop | Both, universal ZIP | Both | macOS: [Claude cask metadata](https://formulae.brew.sh/api/cask/claude.json), downloading the official Anthropic ZIP. Linux: the [official desktop APT repository](https://code.claude.com/docs/en/desktop-linux), downloading the versioned `claude-desktop` DEB. |
| Claude Code | Both | Both | [Official native binaries](https://code.claude.com/docs/en/setup), with SHA256 from [stable](https://formulae.brew.sh/api/cask/claude-code.json) or [latest](https://formulae.brew.sh/api/cask/claude-code@latest.json) Homebrew metadata. No global npm install or Node requirement for the native binary. |
| Muse Code | Both | Both | [Official Muse stable channel](https://api.meta.ai/muse-code/channels/muse-stable) and Meta release manifest: native executables, verified against the manifest size and SHA256. The complete version includes its numeric `R` build. |
| OpenCode | Both | Both | [OpenCode 1.x GitHub releases](https://github.com/anomalyco/opencode/releases). Intel packages use upstream's baseline build. Linux uses the glibc package. The current adapter blocks an automatic change to OpenCode 2.x. |
| Oh My Pi | Both | Both | [Official OMP GitHub releases](https://github.com/can1357/oh-my-pi/releases): native standalone binaries. No Bun installation is required for this method. Linux uses the glibc package. |
| Open Design | Both | No desktop binary published | [Official GitHub releases](https://github.com/nexu-io/open-design/releases): macOS DMG. The Linux source installation is manual. |
| OpenMausBot | Both | x64 only | [Official GitHub releases](https://github.com/milind-soni/OpenMausBot/releases): macOS ZIP or Linux AppImage. |
| T3 Code | Both | Both | [Official GitHub releases](https://github.com/pingdotgg/t3code/releases): macOS ZIP or Linux AppImage. Stable and nightly are separate choices. |
| Synara | Both | x64 only | [Official GitHub releases](https://github.com/Emanuele-web04/synara/releases): macOS ZIP or Linux AppImage. Stable and Beta are separate choices. |
| Zed | Both | Both | [Official GitHub releases](https://github.com/zed-industries/zed/releases): macOS DMG or Linux tarball. Stable and preview are separate choices. |
| Xcode Chat / Codex / Claude | Manual, same Xcode installation | Not available | Use the [Mac App Store](https://apps.apple.com/app/xcode/id497799835) or [Apple Developer Downloads](https://developer.apple.com/xcode/resources/). Apple account, license acceptance and OS requirements remain managed by Apple. |

The Homebrew API is used as public, pinned download metadata; Kilo does not install Homebrew or run `brew`, `npm`, `apt`, vendor shell installers, or `curl | sh` to manage these copies. A private DEB extraction does not execute package maintainer scripts, register a system APT repository, or install Debian dependencies. GitHub releases must provide an asset SHA256, and no package with an unchecked digest is accepted.

## Channels and integrations

An existing installation's recognized channel is kept when checking for an update. A fresh Synara installation defaults to Beta; the selector also offers stable. T3 offers stable and nightly. Only channels with official packaged releases are offered: internal Synara Canary/Cua development flavors and nonexistent alpha/beta/nightly feeds are not presented as downloadable releases. A missing release or package produces an error without silently switching channels.

Synara's stable package identifies itself as `synara-desktop` with flavor `production`; Beta identifies itself as `synara-desktop-beta` with flavor `beta`. These identities differ from version numbers. Kilo still checks the installed runtime's structural integration contracts. After updating Synara, close its Kilo workspace, prepare it again, and reopen it; its chats are preserved. The four Codex Normal/Kilo and Claude Normal/Kilo accounts remain separate. Claude Kilo's Models settings require a compatible Claude Code CLI and closing/reopening the workspace after a change. Installing a newer application does not replace those CLI or authentication requirements. Voice still requires an authenticated ChatGPT Codex session.

Muse Code uses its own Meta login and official catalog. Its terminal command and optional native T3 provider share the executable selected under **CLI installations**. Kilo disables Muse self-update for these launches; use **Install or update** to activate another verified copy. See [Muse Code](muse-code.md).

T3, Synara and Open Design need the CLIs used by their selected agents. Install or update those CLI entries separately. Installing a desktop tool does not implicitly install every provider or authenticate an account.

## System requirements

Desktop packages keep their vendor's requirements. In particular, the [OpenAI Linux preview](https://learn.chatgpt.com/docs/linux/linux-app.md) supports Ubuntu 24.04/26.04 LTS and the other distributions listed by OpenAI. [Claude desktop for Linux](https://code.claude.com/docs/en/desktop-linux) requires a compatible Debian/Ubuntu system, its desktop libraries and keyring support. Kilo does not install GPU drivers, portals, keyrings, kernel features, or privileged Cowork/QEMU/KVM dependencies. [Zed on Linux](https://zed.dev/docs/linux) requires Vulkan and the documented desktop portals. Native glibc packages are not a substitute for musl packages on Alpine.

[Ubuntu 24.04 and later restrict unprivileged user namespaces through AppArmor](https://documentation.ubuntu.com/security/security-features/privilege-restriction/apparmor/). Before downloading a Linux Electron desktop package, Kilo checks for host restrictions that prevent a private sandbox. If AppArmor restricts unprivileged user namespaces, or the host disables those namespaces, installation remains manual through the official installer; the UI explains the requirement. This applies to Codex/ChatGPT desktop, Claude desktop, T3, Synara and OpenMausBot, including their AppImage packages. Version checks and managed CLI installations/updates remain available.

The OpenAI DEB installs an AppArmor profile for `/usr/lib/chatgpt/ChatGPT`; Claude's DEB configures `/usr/lib/claude-desktop/claude-desktop`. Both paths differ from a private Kilo installation. Extracting a DEB does not install its profile or give a private sandbox helper root ownership. Kilo 0.61 does not perform privileged system package installation, modify AppArmor/sysctls, or disable Electron's sandbox. Its managed copies therefore require a host that already permits their sandbox. An archive/hash check alone does not demonstrate a working Linux GUI.

The downloader verifies package integrity before extraction. This is separate from Apple Developer ID/notarization or Windows SmartScreen reputation; managing a download does not change the vendor's code signature or remove operating-system checks.
