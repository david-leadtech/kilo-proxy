# Terminal commands

`kilo-codex`, `kilo-claude`, `kilo-opencode` and `kilo-omp` launch your installed Codex CLI, Claude Code, OpenCode or Oh My Pi through Kilo Proxy in the terminal and project folder you are already using. `kilo-muse` follows the Muse harness connection: Kilo shared models in a separate profile, or your own Meta account. These commands are available on macOS, Linux and Windows (PowerShell).

## Install once

1. Open Kilo Proxy and install the CLI you want to use. For Codex CLI, Claude Code, OpenCode or Oh My Pi (`omp`), save your Kilo account and organization under **Settings** and choose your shared library in **Models**. For Muse, choose the connection in **Harnesses → Muse Code → gear**. Its Kilo connection requires saved shared models; **Own Meta account** uses its native login/catalog instead.
2. Open **Settings → Terminal commands** and click **Install terminal commands** on macOS/Linux or **Install PowerShell functions** on Windows. The Codex CLI, Claude Code, OpenCode, Oh My Pi and Muse Code cards also link here from the **Terminal commands** row on that harness's settings page (**Open in Settings**).
3. Open a new terminal so it reads the updated shell configuration. Change to your project folder and run the command for your installed CLI.

On macOS/Linux, the installer writes all five commands into `~/.local/bin` for your user. It requires zsh, bash or fish as your login shell and manages a marked PATH block in that shell's startup files. You can install the commands even if you only use one of those agents. Settings shows the install location and startup files. Existing shell settings outside the marked block are preserved.

On Windows, the installer adds a marked block of functions to the **CurrentUserAllHosts** profile of each detected PowerShell edition (Windows PowerShell 5.1 and PowerShell 7). It queries the shell's actual profile path, including redirected Documents/OneDrive folders, without loading your profiles. Settings shows those paths. Existing profile content and encoding are preserved, with backups when existing files change. There is no PATH or execution-policy change. Profiles must be allowed by your existing policy; signed profiles require manual editing and re-signing. These functions are for PowerShell, including PowerShell in Windows Terminal, rather than Command Prompt.

**Update terminal commands** (or **Update PowerShell functions**) refreshes the managed commands after an application update or move. **Check installation** refreshes their status. Existing unrelated files or detected PowerShell definitions with the same names are not replaced.

If you installed terminal commands before `kilo-muse` was available, click **Install terminal commands** again to add it; complete installations show **Update terminal commands** instead. Installing a newer Kilo Proxy build alone does not create the new wrapper.

## Set up Zsh or Bash manually

If you prefer to edit your shell configuration, open **Settings → Terminal commands → Manual setup · Zsh / Bash**. Copy the function for any of `kilo-codex`, `kilo-claude`, `kilo-opencode`, `kilo-omp` or `kilo-muse`, or use **Copy all**.

Paste the copied block into your Zsh configuration (`~/.zshrc`, or `$ZDOTDIR/.zshrc` when you use a custom Zsh configuration directory) or your Bash configuration (`~/.bashrc`). Open a new terminal, or reload that file in an existing terminal. Bash login shells must source `.bashrc` from their login configuration for its functions to be available.

Each block already contains this installation's application and Kilo configuration paths. It runs the same terminal entrypoint as the installer, using your current directory and arguments. Kilo connections prepare the saved models; Muse's native Meta connection uses its own catalog. No wrapper installation or PATH change is required. Copying a block does not modify shell files, launch an agent or save credentials. Install each underlying CLI separately. Keep Kilo Proxy open for Kilo connections; only Muse's native Meta connection also works with it closed.

The manual blocks are shell functions for Zsh and Bash; Fish users can use the automatic installer. If you previously defined an alias with the same name, remove that alias before using the function. To remove a manual shortcut later, delete its function from your shell configuration and open a new terminal. If you move Kilo Proxy, reopen it at the new location and replace your copied blocks with freshly generated ones.

## Set up PowerShell manually

On Windows, open **Settings → Terminal commands → Manual setup · PowerShell**. Copy one function or **Copy all**, and paste it into `$PROFILE` in the PowerShell edition you use. Create the parent directory and file if they do not exist. Open a new PowerShell window after saving. Windows PowerShell 5.1 and PowerShell 7 have separate profiles; copy the functions into each edition you use, or use the automatic installer to configure both.

The functions contain the current app and configuration paths, without API keys. They need no wrapper installation or PATH modification. They wait for the CLI and leave you in the same shell when it exits, with its exit code in `$LASTEXITCODE`. Empty arguments, Unicode and quoted strings are encoded before passing through PowerShell's native argument boundary. Existing execution policy still applies; Kilo Proxy never changes it. See Microsoft's [PowerShell profile documentation](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_profiles).

For output capture or redirection, add an explicit PowerShell pipeline. The normal interactive invocation inherits the console directly, so a bare assignment or `>` cannot capture its output:

```powershell
$models = kilo-opencode models kilo-local | Write-Output
kilo-opencode models kilo-local | Set-Content models.txt
```

Explicit pipes use PowerShell's text-stream conversion. Input text is encoded as UTF-8 without adding a byte order mark, preserving Unicode prompts in PowerShell 5.1 and 7 without changing the caller's `$OutputEncoding` preference. These pipes are suitable for textual CLI output, not byte-for-byte binary transport. Native `.exe` clients preserve literal arguments; `.cmd`/`.bat` clients reject embedded double quotes and newlines. Use the agent's native executable for those arguments. Keep Kilo Proxy open for the Kilo-backed commands and install the underlying CLI first. `kilo-muse` works with Kilo Proxy closed only when its saved connection is **Own Meta account**.

## Use your current terminal

```sh
cd /path/to/your/project
kilo-codex
```

Or run Claude Code:

```sh
kilo-claude
```

Or run OpenCode:

```sh
kilo-opencode
```

Or run Oh My Pi:

```sh
kilo-omp
```

Muse follows its saved connection. For the native Meta login, choose **Own Meta account** in its harness first:

```sh
kilo-muse
kilo-muse login
```

Muse Kilo needs Kilo Proxy open and saved shared models. The native Meta choice does not require that gateway connection. See [Muse Code setup](muse-code.md).

For `kilo-codex`, `kilo-claude`, `kilo-opencode` and `kilo-omp`, keep Kilo Proxy open while working; its window can be closed to the system tray or menu bar. If the saved proxy connection is stopped, the command starts it before launching the agent. If the app has been quit, reopen it and run the command again.

Each Kilo-backed invocation prepares that agent's latest **saved** model pack if one is assigned under **Models → Packs**, or the shared models, names, default and supported reasoning preferences otherwise. Finish any pending save in the app before launching. Changes made after an agent starts apply on its next launch.

The commands forward the arguments you supply to the underlying CLI. For example, select a previous conversation in the Kilo profile:

```sh
kilo-codex resume
kilo-claude --resume
kilo-opencode --continue
kilo-omp --resume
```

These are the native resume commands documented by [OpenAI](https://learn.chatgpt.com/docs/developer-commands?surface=cli#codex-resume), [Claude Code](https://code.claude.com/docs/en/sessions#resume-a-session), [OpenCode](https://opencode.ai/docs/cli/) and [Oh My Pi v18.2.9](https://github.com/can1357/oh-my-pi/blob/v18.2.9/packages/coding-agent/src/commands/launch-help.ts). OpenCode also accepts `--session <id>` for a specific session. OMP's bare `--resume` opens its session picker. Argument behavior depends on your installed CLI version.

OpenCode subcommands and model overrides work too:

```sh
kilo-opencode models kilo-local
kilo-opencode run "Summarize this project"
kilo-opencode --model kilo-local/vendor/model-id
```

No additional terminal window opens. The CLI uses the current working directory and the terminal's input/output; when it exits, you return to your shell.

## Profiles and authentication

`kilo-muse` follows the Muse connection and executable selected in Kilo, including custom versions. In Kilo mode it verifies the saved shared selection and prepares a separate profile through the running Kilo app. In native Meta mode it reads the selection locally and uses Muse's existing login, configuration and native sessions, without preparing a Kilo profile or starting the proxy. It clears an inherited `KILO_LOCAL_API_KEY` for the Muse child and disables Muse self-update for that launch. Automatic selection follows Kilo's verified installed copy; an explicit custom path remains selected until changed. See [Muse Code](muse-code.md).

`kilo-codex` prepares `~/.codex-kilo-cli`; `kilo-claude` prepares `~/.claude-kilo`; `kilo-omp` prepares `~/.omp-kilo`. These are the same isolated profiles used by their **Open** buttons in Agents. Their saved sessions belong to those profiles. Normal `codex`, `claude` and `omp` keep their existing configuration and authentication. See [Oh My Pi setup](oh-my-pi.md) for its model picker, reasoning and image MCP support.

`kilo-opencode` refreshes the OpenCode 1 profile at `~/.opencode-kilo/opencode.json`, the configuration used by **Open OpenCode** for OpenCode 1, with its assigned pack or shared models, display names, default, reasoning levels and context limits. OpenCode 2 uses the profile described under **Choosing the OpenCode version** below. It also updates the managed `kilo_images` MCP entry from the image-generation setting while preserving unrelated settings and MCP servers. The command sets `OPENCODE_CONFIG` for its child process and clears inherited `OPENCODE_CONFIG_CONTENT`. It does not edit your ordinary OpenCode configuration or authentication; global/project settings still merge, and OpenCode's usual session storage is shared. Those settings can affect the effective configuration. See [OpenCode setup](opencode-and-zed.md).

### Choosing the OpenCode version

`kilo-opencode` follows the version selected in **Apps → OpenCode**; you do not need to reinstall the terminal command when switching. OpenCode 1 keeps the `~/.opencode-kilo` profile described above and is launched through `OPENCODE_CONFIG`. OpenCode 2 uses a private home and XDG directories at `<Kilo data>/opencode2/home`, so its accounts and chats stay separate from OpenCode 1. Only the local proxy key is passed to the OpenCode 2 child, through `KILO_OPENCODE2_API_KEY`.

OpenCode 2 treats `--standalone` as a per-command flag, so `kilo-opencode` places it where each command accepts it:

- **TUI launch** (no arguments, root flags such as `--continue`, or a project folder): first, as in `opencode --standalone --continue`.
- **`run`, `models`, `stats`, `mini`, `reload`, `api`**: after the command, as in `opencode models --standalone kilo-local`. A folder with the same name in your project does not change this.
- **`session <subcommand>`, `auth <subcommand>`**: after both words, as in `opencode session list --standalone`. A bare `session` or `auth`, which only prints help, is passed through unchanged.
- **Global logging flags** (`--print-logs`, `--log-level`) before a command are kept in place, and the flag follows the command.

OpenCode 2 does not accept this flag for `mcp`, `debug`, `plugin`, `acp`, `serve`, `service`, `pair`, `upgrade` or `uninstall`. For those and unknown subcommands, `kilo-opencode` passes the arguments through unchanged and prints a warning on stderr: without the flag, the command may wait for or conflict with your regular OpenCode background service.

The single **OpenCode** model pack slot applies to both versions, for the desktop card and `kilo-opencode`. Without an assigned pack, both use the shared model library.

The installed commands and manual functions contain no API keys. Kilo-backed commands contact the running local Kilo Proxy app, which prepares the profile and supplies the local connection credential to the child process. Muse reads its saved executable selection locally. Your personal Kilo key is not placed in the shell command or shell startup files. Profile compatibility and model protocol requirements are described in [client setup](clients.md).

## Troubleshooting

- **Command not found:** Open a new terminal after installation. On macOS/Linux, check the install directory and PATH in Settings. On Windows, check the listed profile for your PowerShell edition; profiles do not load with `-NoProfile`, and your execution policy must permit them. Existing aliases with the same names can take precedence over functions.
- **Kilo Proxy is unavailable:** Reopen the app with the same configuration directory used when installing the commands. Save a working account and organization in Settings, then try again.
- **Agent not found:** Install the matching client: Codex CLI, Claude Code, OpenCode, Oh My Pi (`omp`) or Muse Code. For Muse, also check its saved executable path under **CLI installations**. The terminal commands launch existing clients; they do not install them.
- **Commands stopped working after moving the app:** Open Kilo Proxy from its new location and use **Update terminal commands**, or replace your manual functions with newly copied blocks.
- **OpenCode version mismatch:** If preparation reports "Choose an OpenCode 1.x installation, or select OpenCode 2 in Kilo settings.", select the matching version under **Apps → OpenCode**, install that major version, refresh detection, and make sure `opencode` or `opencode2` on PATH reports the expected major version. An explicit executable path takes priority over detection.
- **Model selection is empty or stale:** For Kilo connections, including Muse Kilo, save models in the common library and launch again. Muse's native Meta connection uses its own official catalog and login. An already running agent does not automatically reload the selection.

## Verification

Automated tests cover installation, preserved shell startup files, repeated updates, argument quoting, long prompts, current directory, stdin/stdout, exit codes, saved model changes and local authentication. They use temporary homes, fake clients and a local app instance, without Kilo inference.

Manual setup tests also execute the generated functions in available Zsh, Bash and PowerShell shells, verify that the caller's shell survives, and check that snippets remain available when automatic installation is blocked. PowerShell installer tests use temporary profiles to cover redirected Documents paths, encodings, idempotency, backups and conflicts. Native interface tests cover copying individual functions and the complete block before installation.

Before packaging, GitHub Actions runs the production executable's terminal entrypoint on both architectures of macOS, Linux and Windows. Windows runs the GUI-subsystem executable through both PowerShell 5.1 and PowerShell 7, using temporary profiles and synthetic clients. Separate Windows tests check console handles, redirected streams and Ctrl+C. To repeat the Unix production check locally:

```sh
KILO_TEST_TERMINAL_BINARY=/absolute/path/to/kilo-proxy go test . -run '^TestTerminalAgentRunsInCurrentTerminal$' -count=1
```

On Windows, with both PowerShell editions installed:

```powershell
$env:KILO_TEST_TERMINAL_BINARY = 'C:\path\to\Kilo Proxy.exe'
go test . -run '^TestTerminalAgentWindowsProductionPowerShell$' -count=1
```

For an optional smoke check with an installed OpenCode CLI, set both executable paths. This lists two synthetic shared models through the actual wrapper without model inference, using temporary configuration and data directories:

```sh
KILO_TEST_TERMINAL_BINARY=/absolute/path/to/kilo-proxy \
KILO_TEST_OPENCODE_BINARY=/absolute/path/to/opencode \
go test . -run '^TestTerminalOpenCodeInstalledCLILoadsSharedModels$' -count=1
```
