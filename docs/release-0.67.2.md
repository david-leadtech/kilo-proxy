# Kilo Proxy 0.67.2

- Muse Code compatibility checks recover after tool discovery finishes, so its launch button no longer remains stuck in the checking state.
- T3 Code nightly account lists support the explicit-instance shape used by 0.1.1-nightly.20261018.3335 while preserving native, custom and Kilo account identities and model settings.
- Windows desktop archive smoke checks retry only a sharing violation while cleaning their own temporary directory; executable exit, report and version checks remain required. Persistent cleanup locks still fail validation.

T3 compatibility was reproduced against the official macOS ARM64 nightly 3335 package in an isolated Linux ARM64 Cua. Package preparation does not establish native macOS Electron launch, personal-account authentication or support for future nightly builds. Muse checks use disposable state and synthetic discovery; no personal login or paid inference was used.
