# Muse Code

Kilo Proxy 0.62.1 opens [Muse Code](https://dev.meta.ai/products/muse-code) in a project terminal and connects its native provider to compatible T3 Code installations. Muse uses its own Meta login and official model catalog. Kilo's shared models, model packs and gateway credentials do not configure this connection.

## Installation and executable selection

Open **Harnesses → Muse Code** to check the installation. On macOS and Linux, **Install or update** offers Kilo's private copy from Meta's official release manifest. Kilo verifies the download size, SHA-256, operating system, architecture and version before activating it. On Windows, install the official executable manually.

In **Settings → CLI installations**, the optional **Muse Code executable path** selects a particular installation. An empty path follows Kilo's current verified copy, including a custom version installed through Kilo, then the system installation when there is no managed copy. An explicit path must be absolute and available; an invalid or removed executable stops the launch rather than silently choosing another version. Use **Automatic detection** to follow the managed copy again. Installing a new Kilo copy does not erase an explicit path.

On Windows, Kilo recognizes Meta's official `muse.cmd` launcher and resolves the active versioned `.exe` from that same installation. This keeps quotes and multiline prompts intact. For other custom Windows wrappers, select the underlying Muse `.exe` directly.

The terminal wrapper, the Muse harness and T3 use this same selection. Save path changes before opening. Close and prepare the private T3 workspace again after changing its resolved Muse executable. Kilo launches Muse with automatic self-update disabled so a running CLI cannot replace the verified installation; update through Kilo or your chosen external installation instead.

## Terminal

Install or update the wrappers in **Settings → Terminal commands**. Open a new terminal and run:

```sh
cd /path/to/your/project
kilo-muse
```

The command uses the current directory, forwards arguments and preserves the CLI's exit code. It also works when the Kilo Proxy app is closed; it reads the saved executable selection locally. It does not start the Kilo gateway or require a Kilo account or shared model selection.

Sign in through Muse's own flow:

```sh
kilo-muse login
```

The available models and permissions come from your Muse account. Choose a model in Muse, or pass an exact model ID that your account supports:

```sh
kilo-muse --model <official-model-id> --reasoning-effort high
kilo-muse resume --last
```

Use `kilo-muse --help` and command-specific `--help` for the options supported by your installed version. Kilo does not replace the selected model with a similarly named gateway model, write Meta credentials or alter your normal Muse session storage. Muse's tool approvals and sandbox defaults remain active.

**Open Muse Code** in Harnesses starts a new terminal in the general project folder or the Muse-specific override. Its settings show the native login and catalog, without Kilo profile preparation or credential export. See [terminal commands](terminal-commands.md) for wrapper installation and manual shell setup.

## T3 Code

In **Settings → T3 Code** in Kilo Proxy, enable the optional **Muse Code** provider, close **T3 Code · Kilo**, and prepare or open it again. The installed T3 package must include a compatible native Muse driver and SDK. A missing or incompatible driver produces a preparation error.

To use only native accounts, disable both **Kilo Proxy · Codex** and **Kilo Proxy · Claude**. This mode does not require a Kilo gateway connection or shared models and does not create Kilo CLI profiles or proxy secrets. Enabling either Kilo connection restores its connection and shared-model requirements.

T3 receives the selected Muse executable. Its native driver obtains the model catalog from Muse's session protocol; Kilo does not freeze a model list or inject its shared gateway models into Muse. Model availability, reasoning controls and authentication follow the installed Muse and T3 versions and your Meta account. The [official Muse SDK](https://github.com/meta-models/muse-code-sdk) uses a `muse serve` process; Meta documents the SDK and CLI versions as paired releases.

When switching providers in a compatible T3 Orchestrator V2 workspace, T3's existing bounded handoff applies. This is separate from Muse's native terminal chats. See [T3 Code](t3-code.md) for provider settings and private-workspace behavior.

## Troubleshooting

- **Muse was not found:** install it or select its executable under **CLI installations**, save and refresh detection.
- **Selected executable unavailable:** restore that exact installation or return to automatic detection. Kilo will not substitute another installed version.
- **Executable changed:** refresh installation detection before opening again; prepare the closed T3 workspace again if its resolved executable changed.
- **Login or models unavailable:** use Muse's own login and inspect its account access. Saving Kilo gateway credentials does not sign in to Meta.
- **T3 Muse driver unavailable:** use a T3 build that includes a compatible native Muse driver. Installing the CLI alone cannot add that driver to T3.
