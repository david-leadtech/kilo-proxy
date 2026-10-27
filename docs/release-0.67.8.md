# Kilo Proxy 0.67.8

T3 Code nightly compatibility update.

- Recognizes the chat-scoped Providers panel asset introduced in T3 Code 0.1.3-nightly.20261026.3710, while keeping compatibility with the previous asset name.
- Restores preparation of the managed T3 Code workspace when that asset is present. Existing account and model patch validation, source integrity checks, and rejection of ambiguous Providers panels remain in place.

Includes PR #80.
