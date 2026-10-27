# Cline upstream routing

Create or edit a Cline API-key account and use **Upstream routing** to refresh the
model catalog, add a model, discover its upstream providers, and verify a rule.
Discovery and verification send small inference requests and may consume quota.
Provider lists are candidates; verification reports the actual provider when
Cline includes routing metadata.

Rules match the **final upstream model ID**, after model mapping. Keep the
cline-pass/ prefix for subscription models: choosing a provider does not change
the model's billing wallet.

## Account rules

The account's credentials.cline_upstream_pins stores a map keyed by model ID:

    {
      "cline_upstream_pins": {
        "cline-pass/deepseek-v4.1-flash-1017": {
          "pipeline": "planner",
          "upstream": "deepseek",
          "has": ["vision", "implicit-caching"]
        },
        "cline-pass/glm-5.3-flash": {
          "pipeline": "direct",
          "upstream": "z-ai"
        }
      }
    }

- upstream selects one provider strictly. Omit it for automatic routing.
- pipeline is planner (Vercel), direct (OpenRouter), or unknown. Discovery
  identifies it from the response; it can also be set in advanced settings.
  An unknown pipeline sends both routing forms and needs verification.
- has adds capability requirements for the Vercel pipeline. Supported values
  are vision and implicit-caching; multiple values must all be satisfied.
  Existing client requirements are retained and merged.
- Selecting or discovering OpenRouter hides and clears the capability
  requirements. The server also removes them when saving or forwarding a direct
  rule. Switching back to Vercel does not restore cleared selections.

Vercel requests use providerOptions.gateway.only and .has. OpenRouter requests
use provider.only with allow_fallbacks: false. All supported ingress protocols
use these rules, including Chat Completions, Responses, Anthropic Messages, and
Responses-shaped Chat requests. Normal account connection tests use the same
request preparation.

An absent model rule leaves normal routing unchanged. Remove the rule to clear
it; an empty map or explicit null clears all rules. Account credential edits use
the existing full-object update convention, so retain unrelated credential
fields when calling the admin API directly.

Rules apply to the current account. If a group must always use the same
provider, configure its other eligible accounts consistently: ordinary account
failover uses the next account's settings.

## Discovery and verification

These admin endpoints share a connection request:

    {
      "account_id": 123,
      "base_url": "https://api.cline.bot/api/v1",
      "proxy_id": 0,
      "model": "cline-pass/deepseek-v4.1-flash-1017"
    }

- POST /api/v1/admin/accounts/cline/upstreams/discover
- POST /api/v1/admin/accounts/cline/upstreams/verify

For a new account, omit account_id and supply api_key. For an existing account,
the server reuses its saved key; an optional api_key overrides it for the
request. base_url and proxy_id can test unsaved edits; a proxy ID of zero means
no proxy, while omission retains the saved proxy.

Verification also accepts pin (one rule object) and stream (boolean). It returns
matched, mismatch, unknown, or unavailable, together with completed, the target
and actual provider IDs, and the check time. Missing actual-provider metadata is
unknown. An incomplete stream is not a successful verification. Routing
verification does not prove image recognition or a prompt-cache hit.

Discovery ignores saved pins and capability filters, then uses Cline's routing
metadata and a deliberately invalid provider probe to discover candidates.
When the full list is unavailable, the result includes a warning and manual
provider entry remains available. Informational snapshots are stored under
extra.cline_upstream_discovery only when the saved connection was used.

The existing /api/v1/admin/accounts/models/sync-upstream-preview endpoint also
accepts account_id and proxy_id for Cline, so model refresh uses the same unsaved
connection settings without returning the saved key to the browser.
