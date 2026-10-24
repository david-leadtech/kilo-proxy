# Kilo Proxy 0.67.7

## OpenCode

- `kilo-opencode` follows the OpenCode major version selected in Apps, including its chosen executable and matching profile.
- OpenCode 1 and OpenCode 2 use the same assigned OpenCode model pack, falling back to the shared model library when no pack is assigned.
- Both versions expose declared reasoning levels and apply the saved model reasoning preference.
- OpenCode 2 terminal launches keep their private HOME and XDG directories, with command-specific placement of `--standalone`.

The private OpenCode 2 profile keeps its accounts and chats separate from OpenCode 1.
