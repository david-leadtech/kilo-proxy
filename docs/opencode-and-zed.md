# OpenCode and Zed helpers

The native **OpenCode** and **Zed** cards use the same [shared model library](shared-models.md). Edit up to 50 IDs, names, a default model and token limits in **Models**. Unknown models start with a 200,000-token context assumption; review it against provider metadata. An output limit of zero is unspecified. Both integrations use Chat Completions; OpenCode 1 and OpenCode 2 expose each model's supported reasoning levels as variants, while Zed reasoning remains automatic. Model listing does not certify inference compatibility.

The optional browser helper remains a separate interface with independent per-client model selections and explicit profile preparation.

## OpenCode 1

Choose **OpenCode 1** in the OpenCode harness settings to use this integration. The executable must be version 1.x.

Each major version has its own optional absolute executable path in Kilo Settings. Leave it empty for automatic detection: OpenCode 1 uses Kilo's verified installed copy before the system installation, while OpenCode 2 uses its separately installed executable. An explicit path takes priority and never falls back silently when unavailable. Updating a Kilo copy preserves the explicit choice. The actual major version is checked before preparing or opening. T3 shares these paths but keeps its own major-version selector.

`kilo-opencode` follows this version selection and uses the profile for the selected version, so switching versions does not require reinstalling the command. The single **OpenCode** pack assignment covers OpenCode 1 and OpenCode 2, for the desktop card and the terminal command; without an assigned pack, both use the shared library.

1. Install OpenCode separately and configure Kilo Proxy in **Settings**.
2. Choose models, names, a default and any token limits in **Models**. These library changes save automatically.
3. On **Harnesses**, use the general **Project folder** or OpenCode's settings page under **When opening → Folder** to override it, then **Open OpenCode**. It prepares `~/.opencode-kilo/opencode.json` and `kilo-models.json` (`%USERPROFILE%\.opencode-kilo` on Windows), then opens an interactive terminal.
4. Use `/models` to switch models. Edit the common library and reopen the agent to apply changes. Manual preparation is under **Advanced → Prepare without opening** on that harness's settings page.

The launcher selects the profile through `OPENCODE_CONFIG`, clears an inherited inline configuration override for this child, and pins the initial model with `--model`.

The custom provider includes the local proxy key in its protected configuration, so `/connect` is unnecessary for this profile. It never includes the upstream Kilo credential. The initial model also supplies `small_model` for internal requests. Context/output limits are emitted together when an output limit is known.

Reasoning variants come from Kilo model metadata, plus any custom levels saved in the shared model library. The saved or resolved reasoning preference is emitted as the model's default `options.reasoningEffort`, and each declared level becomes a variant that OpenCode exposes with `--variant <level>` or its variant picker. Unknown levels are ignored by OpenCode. The official OpenCode 1.19.4 CLI sent the selected level to a synthetic local gateway.

This is a separate configuration file, not a complete OpenCode sandbox: global and project configurations still merge, and project settings can override this file. The helper does not change ordinary global OpenCode settings, auth storage, permissions, plugins, or project files. Check the resolved settings if a repository overrides the Kilo provider.

The optional configuration export contains the local key; keep it private. The launch command contains only the configuration path and model ID. It restores the parent shell environment on Unix and PowerShell.

## OpenCode 2

In the OpenCode harness settings, select **OpenCode 2**. Install [OpenCode 2](https://dev.opencode.ai/v2/docs/) version 2.0.18 or newer within major version 2 manually, then refresh detection. Kilo looks for `opencode2`, `opencode-v2`, then `opencode`, and verifies the executable's actual version before preparing or launching. Automatic installation for this major version is not available.

Opening prepares the saved shared Models library in a separate home at `<Kilo data>/opencode2/home`. This uses OpenCode 2's `providers` configuration format, its OpenAI-compatible package and exact gateway `modelID` values. The local proxy key is passed only to the child environment. Kilo starts the executable with `--standalone`, preserving the chosen project folder. User MCP settings, other providers and large-number literals in this private config are retained.

The separate HOME and XDG directories prevent OpenCode 2 from migrating OpenCode 1's normal database. Accounts and chats are separate; changing the selector does not move either version's chats. Project configuration can still merge, so this is not an operating-system sandbox.

Adding OpenCode to T3 is a separate choice under **Settings → T3 Code**. Its native T3 driver uses its own authentication and model catalog. Select its major version there independently of this standalone Kilo connection.

`kilo-opencode` follows the selected major version and prepares this same private home and XDG directories, so terminal launches share the separate accounts and chats. With no pack assigned to **OpenCode**, OpenCode 2 uses the shared library; an assigned pack applies to both major versions and to the terminal command.

The official OpenCode 2.0.27 CLI completed a streaming request against a synthetic local gateway using the generated v2 config, exact model ID and local bearer key. No paid inference or personal account was used.

Each model's declared reasoning levels are exposed as variants, selected as `kilo-local/<id>#<level>`. The Default option uses the saved reasoning preference, or a resolved declared level when that preference is not available. Internal/title requests use the lowest declared level when `none` is available, and otherwise the model's default level. A profile prepared by an older version re-prepares once after upgrading, and again after a catalog refresh that changes model names or declared levels. The official OpenCode 2.1.3 CLI sent the selected level to a synthetic local gateway.

## Zed

1. Edit models, names, limits and the default in **Models**.
2. On **Harnesses**, click **Open Zed**. It saves the local proxy key in your system credential store, prepares the `kilo-local` OpenAI-compatible provider and Agent default model in your user settings, then opens Zed. Other providers, settings and open projects are retained.
3. Select a model in Zed's Agent panel. No key paste is needed for normal setup. Generation still depends on the model and gateway.

Preparation uses macOS Keychain, Windows Credential Manager or the Linux Secret Service. Only the **local proxy key** is stored; the upstream Kilo credential stays in Kilo Proxy. The operating system may ask you to unlock or allow access to its credential store. A credential-store failure stops preparation and shows an error instead of opening an unconfigured provider.

Zed associates saved keys with the exact provider URL and caches loaded credentials. The helper therefore uses a local URL such as `http://127.0.0.1:8877/zed/<credential-version>/v1`. The version is a short digest of the random local key, not the key itself. Migrating an older setup or rotating the local key changes this URL, so an already-open Zed reloads the credential and refreshes its available models. The endpoint still requires the local bearer key; knowing its URL does not grant access. This follows Zed's [URL-bound credential loading](https://github.com/zed-industries/zed/blob/v1.18.1/crates/language_model/src/api_key.rs#L135) and [provider settings updates](https://github.com/zed-industries/zed/blob/v1.18.1/crates/language_models/src/provider/api_compatible.rs#L84).

Settings locations are `~/.config/zed/settings.json` on macOS/Linux, `$XDG_CONFIG_HOME/zed/settings.json` when configured on Linux, and `%APPDATA%\Zed\settings.json` on Windows. A neighboring `kilo-models.json` stores the helper selection. This configures Zed Agent, not edit prediction or external agents.

The optional **Copy configuration** export contains the same managed URL and models, without a key. Copying JSON alone does not provision credentials: use **Prepare without opening** first, or set the local key in Zed's `kilo-local` provider settings yourself. Older standalone exports using `/v1` still need a local key saved for that exact URL.

### Recovering an older setup

Click **Open Zed** again to prepare the current connection and models. Zed hides models from providers without credentials, so **Credentials Missing** and an empty model selector can have the same cause. See Zed's [model selector authentication filter](https://github.com/zed-industries/zed/blob/v1.18.1/crates/agent_ui/src/language_model_selector.rs#L432).

If Zed still reports missing credentials after preparation, open its **Settings → AI → LLM Providers → kilo-local** to retry loading the saved key. This matters after a denied credential-store request: preparing an unchanged key and port does not change the URL again. Close settings and reopen the model selector after loading. **Zed settings page → Advanced → Copy key for Zed (recovery)** remains available if you need to enter the local key manually in that provider's settings. Do not paste your upstream Kilo API key there.

If you previously launched Zed with `KILO_LOCAL_API_KEY`, that environment value takes precedence over its credential store. Remove a stale override and restart Zed once; updating the keychain cannot change an existing process's environment. This is only needed for such manual overrides, not the normal **Open Zed** flow. Zed documents this [environment precedence](https://zed.dev/docs/ai/use-api-access#api-keys-and-environment-variables).

## Updates and backups

Native library edits save automatically, but generated editor files update only when that agent is opened or prepared. Zed watches its settings and refreshes the provider after preparation; an existing OpenCode process may need to be reopened to load changes. Editing the library alone does not update either editor's model picker. Use **Models → Import an existing agent selection** to review an older saved OpenCode/Zed profile before replacing the common library. The browser helper retains its explicit Load/Prepare flow.

JSON comments, trailing commas, unrelated settings, and exact large-number literals are preserved. Only Kilo-managed fields and the selected default are updated. Changed files receive exact `.bak` backups; a no-op leaves existing backups intact. Invalid or duplicate-key JSON, non-object settings sections, symbolic-link destinations, and unsafe backups are rejected. Writes use temporary files and restore earlier writes if a later write fails; this is not a crash-atomic transaction. If your settings use symlinks, use the optional export to merge the configuration yourself.

The saved model selections contain no credentials. Files written by Kilo Proxy use private file permissions on Unix and inherit the profile directory's ACL on Windows. Do not publish generated OpenCode profiles or backups containing local credentials.

## Verified coverage

- Automated Go tests cover multi-model validation, JSONC preservation, exact backups, idempotent saves, updates, unsafe paths, and profile load/save.
- Native checks cover shared-library propagation, automatic saving, project persistence and preparation-to-open transitions. Browser checks separately cover its independent tabs, names, initial models, copying, dirty-state handling, English/Spanish and narrow layouts.
- The installed **OpenCode 1.4.0** loaded both generated models and completed a streaming request against a synthetic local gateway with the expected local bearer key. Internal requests stayed within the selected Kilo provider. No paid Kilo inference was performed.
- Installed **Zed 1.18.1 on macOS** loaded the helper-written local credential from Keychain and displayed all six configured models in its selector, without restarting Zed or Kilo Proxy. Credential storage, URL changes and model visibility also match its official source. Automated checks cover generated profiles, credential-store failures, key rotation and responsive inference during credential prompts. This does not certify paid Kilo inference or every model's tool support.

Repeat the optional installed-client check with:

```sh
KILO_TEST_OPENCODE=/absolute/path/to/opencode go test -run TestInstalledOpenCode -v -count=1
```

This uses temporary config/data/cache directories and a synthetic local gateway, without Kilo credentials. It may download public OpenCode/provider dependencies.

References: [OpenCode configuration precedence](https://opencode.ai/docs/config/), [OpenCode custom providers](https://opencode.ai/docs/providers/#custom-provider), [Zed API access and key storage](https://zed.dev/docs/ai/use-api-access), and [Zed settings](https://zed.dev/docs/reference/all-settings).
