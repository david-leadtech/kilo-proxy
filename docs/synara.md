# Synara

Kilo Proxy opens **Synara · Kilo**, a separate workspace. Choose its accounts and model catalogs in **Harnesses → Synara → gear → Accounts and models**:

| Agent | Connection |
| --- | --- |
| Codex | Existing Codex CLI login |
| Kilo Proxy · Codex | Official OpenAI/ChatGPT models or your shared models through the local proxy |
| Claude | Existing Claude Code login |
| Kilo Proxy · Claude | Opus 5.5, Sonnet 5.5 and Fable 5.1, or your shared models through the local proxy |

The **Kilo Proxy** names and green account indicators distinguish the Kilo accounts. Your regular Synara workspace and its chats stay separate and can remain open.

Enable or disable each account in the gear panel. New configurations offer official models for the Kilo Codex and Claude accounts; existing prepared workspaces keep their shared-model choice and enabled accounts. Changes save automatically and require closing **Synara · Kilo** and preparing again. A failed save stays unconfirmed and blocks preparation and opening until you retry or reload the saved settings. Disabling **Claude** does not invalidate **Kilo Proxy · Claude** or require its normal CLI login.

Compatible installed builds also expose **Cursor**, **Grok**, **OpenCode**, **Antigravity** and **Pi**. Other providers use their own installations and logins. OpenCode can use its own account/catalog or **Kilo Proxy · shared models**, with one selected major version: OpenCode 1 or OpenCode 2 (2.0.18 or newer). OpenCode 2 uses a separate profile that may require its own login. Choices absent from a newer build stay visible so you can turn them off; they cannot be silently replaced or enabled without the required capability. **Muse is not offered in Synara:** its upstream app has no compatible Muse driver.

Codex CLI, Claude Code and both OpenCode majors use the executable paths selected in Kilo's **Application and CLI installations**, including custom or verified managed versions. Synara's own OpenCode major is independent of the standalone OpenCode choice. Path changes invalidate preparation and are checked again before opening.

## Requirements

- Install [Synara stable or Beta](https://github.com/Emanuele-web04/synara/releases). From Kilo Proxy **0.61.0**, both official stable and Beta channels are supported without a version allowlist; the detected version is shown for information. Choose the release channel in **Installation and updates** on macOS/Linux. See Synara's requirements for [Codex](https://www.trysynara.com/docs/providers/codex) and [Claude Code](https://www.trysynara.com/docs/providers/claude-code).
- Install the native CLIs required by your enabled accounts. Desktop app login is separate from CLI login. The normal agents require an existing CLI session.
- **Codex** requires file-based Codex authentication: Synara does not support Codex's `keyring` or `auto` credential-store modes. Kilo Proxy does not convert or copy your credentials.
- For Kilo accounts, connect Kilo Proxy and save models in **Models**. Shared Codex/Claude catalogs accept at most 32 models per account; official scopes do not impose that limit on unrelated shared models. Kilo OpenCode uses the shared catalog. Choose the initial model and compatible reasoning defaults in Models. When every enabled account uses its own connection, preparation does not require Kilo Models or the gateway.

## Installation selection on Linux

Kilo distinguishes a detected installation from one that is ready to open. An AppImage candidate can appear as **Installed** while **Open** remains disabled. Detection never executes the AppImage. Its filename and AppImage header do not verify its publisher, version or release channel.

Automatic detection checks the usual persistent extracted installations and bounded lists of Synara AppImage filenames in `~/Applications`, `~/.local/bin`, `~/Downloads` and `~/Descargas`. It does not inspect running processes or their environments. An open regular Synara window is separate from the private Kilo workspace.

To select another installation, use **Settings → Application and CLI installations → Synara** and save its absolute path. On Linux, select the native executable in a persistent extracted installation with its unchanged `resources/app.asar` tree. A raw AppImage may also be selected, but remains unavailable for opening until you install a verified Kilo copy or select an extracted executable. Temporary `/tmp/.mount_*` and `/proc` paths are not installations you can save.

The saved path takes priority over automatic detection. A missing or invalid selected installation reports an error rather than silently switching to another copy. Installing a Kilo copy keeps that explicit choice. Select **Use Kilo copy** in Synara's installation settings, or **Automatic detection** in the path settings, to clear it; Kilo confirms the saved change before switching. An empty path chooses the verified current Kilo copy first and then a system installation. Close **Synara · Kilo** and prepare again after changing the installation; chats are kept.

Use **Installation and updates** to download a separate official stable or Beta copy. Kilo verifies the official package's size and SHA256 before extraction, then checks the extracted tree, CPU, version and channel before activating it. Your original AppImage and regular Synara profiles stay unchanged. Choosing an external extracted installation does not give it the managed download's cryptographic verification.

Ubuntu AppArmor and user-namespace policies may block a private Electron copy even when the original AppImage opens normally. Kilo preserves the sandbox and does not change AppArmor, sysctl, setuid helpers or privileged dependencies. Follow a vendor-supported packaging path if the private copy is blocked. File detection or extraction does not establish that the Linux GUI, account-aware delegation or audio works; the corrected private runtime for account-aware delegation remains validated on macOS only.

## Open the workspace

1. Open **Harnesses → Synara** in Kilo Proxy and use its gear to choose **Accounts and models**.
2. Select **Open Synara**. Kilo Proxy prepares the saved choices and starts the proxy when a Kilo account needs it.
3. Start a new chat, choose an enabled account and check its model.

In the Synara gear, **Prepare** performs the same preparation without launching Synara. The web tab provides **Accounts and models** and **Advanced → Prepare without opening**. The local web interface also has a **Synara · Kilo** tab.

Close only **Synara · Kilo** before changing the shared models or proxy connection, then reopen it from Kilo Proxy. Its app data and Kilo CLI profiles are private. The normal accounts refer to your usual CLI homes; Kilo Proxy does not read or copy their login credentials or change the normal account. Synara's own CLI drivers may write session metadata and link `auth.json` into a normal Codex overlay, or copy that file if linking fails.

## Models and reasoning

The prepared list reflects each enabled Kilo account’s official or shared scope. **Kilo Proxy · Claude** shows only its prepared models. Compatible native Claude names map to the exact gateway ID, including a dated ID when it is the selected match; other shared choices retain their gateway IDs.

The filtered **Kilo Proxy · Claude** picker is offered only when Kilo verifies the installed private macOS runtime and picker contract. If this capability is unavailable, Kilo blocks preparing/opening the Kilo Claude account for both official and shared scopes; choose the native **Claude** account or a compatible macOS build. Selecting shared models does not bypass that requirement. **Kilo Proxy · OpenCode** also requires a compatible private macOS runtime to keep its picker and delegation catalog restricted to the exact shared models and variants. Its native connection and OpenCode 1/2 choices remain available when the installed Synara driver supports them. The original signed application stays unchanged. Linux and Windows do not acquire filtered-picker support from package detection or extraction.

Compatible native Claude models use the reasoning levels selected in Kilo's **Models** in the private picker and delegation. Other shared models keep their compatible reasoning defaults. Close and reopen the Kilo workspace after changing them. Saved per-model levels require **Claude Code 2.1.251 or newer**; Opus/Sonnet 5.5 defaults require **2.1.267 or newer**. Explicit effort selected for a built-in Claude choice follows Synara's driver behavior. Automatic and unsupported Claude levels do not receive a native effort override. Generation, tools and available reasoning levels depend on the model, CLI and provider.

An existing chat keeps its account. Create a new chat to switch between a normal account and its Kilo counterpart; changing the account for a new chat does not convert earlier chats.

## Delegating to another account

On macOS, the private workspace's Synara tools list the enabled accounts separately, with their enabled/authentication status and their own model catalog. Choose **Codex**, **Kilo Proxy · Codex**, **Claude** or **Kilo Proxy · Claude** when asking an agent to create another chat. The tools retain that account's `instanceId` for single and batch creation. A request that omits an ambiguous account, selects a disabled account or uses a model outside its catalog fails before creating the chat; Synara does not silently substitute a different account.

The private macOS runtime checks the backend integration points before applying this correction, preserving the installed app and the regular workspace. If a release changes those integration points, the private launch reports the incompatibility before starting Synara. Account-aware tool delegation has not been validated for the native Windows or Linux desktop yet.

**Delegar a otra cuenta:** indica Codex, Codex Kilo, Claude o Claude Kilo al pedir que se abra otro chat. Cada cuenta tiene su catálogo y estado propios. Si falta elegir la cuenta o está desactivada, la petición se detiene antes de crear el chat. Esta corrección de las herramientas de delegación está validada para el workspace privado en macOS.

## Voice notes

Synara transcribes voice notes through a ChatGPT-authenticated **Codex** account, including when the chat uses **Kilo Proxy · Claude**. Keep Codex enabled and sign in through `codex login` using file-based authentication. A Kilo API key or Kilo Proxy's own ChatGPT connection does not provide this Synara voice session. Allow Synara to access the microphone when prompted.

The private Codex adapter makes the CLI's ChatGPT login status readable by Synara. Codex still verifies its real ChatGPT session before Synara sends audio for transcription; the adapter does not read or copy login credentials.

**Notas de voz:** Synara transcribe usando **Codex** autenticado con ChatGPT, incluso si el chat usa **Kilo Proxy · Claude**. Mantén Codex activado, inicia sesión con `codex login` y autenticación en archivo, y concede acceso al micrófono. La clave de Kilo y la conexión ChatGPT de Kilo Proxy no sustituyen esa sesión. Puedes desactivar **Claude** sin bloquear Claude Kilo.

## Troubleshooting

- **Synara or a CLI was not found:** install Synara stable or Beta and the CLIs required by your enabled accounts, then use **Check again** on the Harnesses list.
- **Synara is installed but Open is disabled:** read the reason shown below its row. A detected AppImage needs a verified Kilo installation or a persistent extracted executable. Missing native CLIs, incompatible package metadata and Linux sandbox restrictions are separate readiness failures.
- **A Claude model is unavailable:** check that the composer selected the exact prepared gateway ID rather than a built-in Claude entry.
- **Close Synara · Kilo first:** quit its private window and prepare again. Your normal Synara window can remain open.
- **Models or credentials changed:** close and reopen the private workspace so the prepared profiles receive the current settings.
- **Windows environment overrides:** before opening Synara, Kilo Proxy checks the variable names in the registry's user and machine Environment scopes without reading their values or changing the registry. Synara can restore these global overrides into its process, so saved provider, development or `SYNARA_*` variables block opening the private workspace. Remove those overrides from Windows **Environment Variables** and try again. If the inspection cannot be completed, Synara is not opened.

Only the local proxy connection is stored for the Kilo accounts. Upstream Kilo and ChatGPT credentials remain managed by Kilo Proxy.
