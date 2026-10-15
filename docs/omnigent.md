# Omnigent

Kilo Proxy 0.66.0 adds **Harnesses → Omnigent → gear → Accounts and models**. Kilo opens a separate local Omnigent workspace in your browser and manages its server and runner.

Enable **Codex** and/or **Claude**, then choose one connection for each:

- **Kilo Proxy** uses Kilo's saved Models selection through the local proxy. Choose the official model scope or all shared models. The effective list, exact IDs, default model and compatible reasoning come from the prepared selection.
- **Own account** uses that CLI's existing login and native model catalog. A Desktop application login alone does not establish a CLI login. Native-only configurations do not require a Kilo gateway or shared Models selection.

Codex's official scope includes selected OpenAI and ChatGPT models. Claude's official scope contains Opus 5.5, Sonnet 5.5 and Fable 5.1 when their exact destinations are available in the connected catalog. Choosing all shared models does not establish protocol, reasoning or tool support for every gateway model.

Use the shared **Models** page to change the default and reasoning settings. Save changes, stop **Omnigent · Kilo**, then prepare or open it again. Start a new chat after changing a harness's connection; existing chats must not be used to silently switch accounts.

Omnigent 0.18.0 provides its per-chat model and reasoning picker for native sessions. Its SDK chat interface does not expose that picker; configure the saved model and reasoning defaults in Kilo before preparing the workspace.

## Installation and executable choices

The initial integration supports macOS and Linux; the Windows launcher is not supported. It uses the official **Omnigent CLI 0.18.0**, with its local server and runner. It does not require a signed Desktop application. Select its installed executable in **Settings → Application and CLI installations** if automatic detection does not find it.

Codex and Claude use the same executable choices as Kilo's other integrations. An explicit custom path takes priority. Automatic detection follows the verified Kilo installation before the system copy. An unavailable selected executable blocks preparation or opening instead of silently selecting another installation. Disabled harnesses do not require their CLI. Verified official npm launchers resolve to their matching native Codex binary inside the selected package or its platform dependency, without using another Codex installation.

## Lifecycle and isolation

**Prepare without opening** writes the private profile. **Open Omnigent** prepares the chosen accounts and models, starts the owned local processes and opens their interface in your browser. An unchanged running workspace can be reopened; changing its profile first requires stopping it.

**Stop Omnigent · Kilo** stops only the server and runner belonging to this Kilo workspace. Its browser tab may remain open. Kilo keeps the normal Omnigent workspace and CLI login homes separate and does not copy upstream credentials into the Omnigent profile. Delegation to a harness using its own account requires that destination's native credentials; when they are unavailable, Kilo refuses the delegation without falling back to another account.

The initial integration supports Codex and Claude. OpenCode and Muse are outside its scope. Model availability, inference and tools still depend on the selected gateway and model. Validation of synthetic requests does not establish access to paid models or personal accounts.
