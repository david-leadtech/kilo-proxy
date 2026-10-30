# Models with BYOK

Kilo Proxy displays BYOK when the current authenticated Kilo Gateway model catalog explicitly returns `hasUserByokAvailable: true` for a model. The Models with BYOK catalog section gathers these models for the selected account or team. Other models remain available in the catalog.

The same badge appears in the catalog, library and client model selection. Refreshing the catalog or changing account/team updates availability; a saved selection does not preserve an old BYOK badge. Native ChatGPT models do not inherit Kilo BYOK availability.

The flag indicates that a BYOK connection is available in the current context. It does not distinguish personal credentials from organization credentials, disclose a key, prove a commercial agreement or discount, or confirm which credential served an individual request. A missing or false flag does not show the badge.

BYOK configuration remains managed in Kilo. Kilo Proxy reads availability metadata from the catalog and does not need provider API keys to display it.
