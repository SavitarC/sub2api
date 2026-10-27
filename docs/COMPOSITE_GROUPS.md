# Composite Groups

Composite groups are an admin routing layer for API keys that should choose a
concrete provider from the requested model instead of binding the key to a
single provider group. They support both built-in model detection and an
admin-configured model route registry for public model aliases. When accounts
on multiple platforms explicitly map the same public model, compatible HTTP
requests automatically select a provider and fail over when it cannot serve them.

## Supported Providers

Composite groups can route to these concrete account platforms:

- Anthropic
- Gemini
- OpenAI
- Antigravity
- Grok
- Kimi
- Zhipu GLM
- DeepSeek
- MiniMax
- OpenCode
- Command Code
- Cline
- TypeSafe / Jev (System One endpoint)

The selected concrete platform is used for account selection, user platform
quota checks, post-usage billing, ops error platform attribution, channel
mapping/pricing lookup, and platform usage reporting.

## Route Registry

Admins can configure routes on a composite group from the group list's
`Routes` action or through the admin API:

- `GET /api/v1/admin/groups/:id/composite-routes`
- `POST /api/v1/admin/groups/:id/composite-routes`
- `PUT /api/v1/admin/groups/:id/composite-routes/:route_id`
- `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id`
- `POST /api/v1/admin/groups/:id/composite-routes/preview`

Each route belongs to one composite group and contains:

- `public_model`: model identifier the client sends.
- `match_type`: `exact` or `prefix`.
- `target_platform`: concrete provider platform.
- `upstream_model`: model identifier sent upstream. If omitted, the public
  model is reused.
- `endpoint`: `any`, `messages`, `count_tokens`, `responses`,
  `chat_completions`, `embeddings`, `images`, or `gemini`.
- `priority`: lower values win after match specificity.
- `enabled`: disabled routes are ignored by runtime resolution but remain
  visible to admins.

Resolution order is explicit route first, then exact account model mappings,
then built-in detection. An explicit route pins its target platform. When more
than one explicit route matches, exact matches beat prefix matches,
endpoint-specific routes beat `any`, longer prefixes beat shorter prefixes,
then lower `priority`, then lower route id.

For JSON-body endpoints, the gateway rewrites the request `model` field to the
route's `upstream_model` before dispatch. For Gemini native paths such as
`/v1beta/models/{model}:generateContent`, the gateway resolves `{model}` and
the handler forwards the resolved upstream model.

Codex Alpha Search and Live requests use the `responses` route domain. Live
requests resolve the model from `session.model`, including multipart `session`
payloads, and apply the configured `upstream_model` before dispatch.
Codex model manifest requests reuse the existing OpenAI account selection and
failover path within the Composite group.

Responses WebSocket resolves the first `response.create` model in the
`responses` route domain (including `any` routes). Only OpenAI and Grok targets
are supported. The public model remains the admission and response identity;
the route's upstream model is applied before channel/account mapping. Subsequent
turns can repeat or omit the public model. Changing it requires reconnecting so
the gateway can resolve the route and select an account again.

## Automatic Model Routing and Failover

Assign the provider accounts to the same composite group and configure an exact,
nonempty `model_mapping` entry for the public model on each account. For example:

| Account platform | Public model | Account's upstream model |
| --- | --- | --- |
| Anthropic | `shared-claude` | `claude-sonnet-4-6` |
| Command Code | `shared-claude` | `claude-sonnet-4-6` |
| Cline | `shared-claude` | `anthropic/claude-sonnet-4-6` |

Without an explicit route for `shared-claude`, these accounts become candidates
for compatible requests. Wildcard mappings and accounts with an unrestricted
default catalog do not claim an automatic public alias. The route preview shows
the candidate platforms and the API returns their supported endpoints.

Provider order uses the lowest account priority on each platform; lower numbers
are preferred. Equal-priority platforms share traffic with a stable order for
explicit client sessions. Each provider retains its existing account scheduler,
health checks, rate limits, account quotas, and account-level session affinity.
Expired accounts cannot be selected, even while ownership metadata is cached.

Automatic provider failover covers HTTP Messages, Chat Completions, Responses,
their count-token operations, and native Gemini generation when the account
supports that protocol. The actual account selects the upstream model, protocol,
credentials, channel mapping, pricing, and platform quota. Aggregate providers
can therefore serve Claude through Messages or Chat Completions, and Gemini
through their supported content or Chat Completions endpoint. OpenCode Zen's
native Gemini adapter supports an explicit `api_base_urls.gemini` base URL;
custom account URLs remain on the configured upstream.

Exhausted account pools, account concurrency, platform quota limits, and existing
retryable upstream failures can advance to another candidate before any response
is committed. All actual account switches share one request budget. User RPM is
counted once; balance, API key limits, and the actual platform quota are checked
for each attempt. Request errors, user-level limits, client cancellation, and
committed responses stop failover. Only recorded upstream usage is billed.

`previous_response_id` continuations resolve and pin the original authorized
account, including when that account is temporarily unavailable. They are never
sent to a different provider. Background jobs are not replayed across providers.
Gemini explicit caches keep their existing restriction to concrete Gemini groups.
Native Gemini requests use Gemini-capable accounts; Chat-only providers are not
eligible for that native endpoint. Requests requiring unsupported adapter features
are excluded from that adapter's account candidates, including Responses-only
operations and Gemini structured-output conversions.

## Built-In Detection

Composite routing detects common public model IDs and provider-prefixed IDs:

- `claude-*` and `anthropic/claude-*` route to Anthropic.
- `gemini-*` and `google/gemini-*` route to Gemini.
- `gpt-*`, `o*`, `codex-*`, `text-embedding-*`, `dall-e-*`, and
  `openai/*` route to OpenAI.
- `grok-*` and `xai/grok-*` route to Grok.
- `kimi-*`, `moonshot-*`, and `k3` route to Kimi.
- `glm-*` routes to Zhipu GLM.
- `deepseek-*` routes to DeepSeek.
- `minimax-*`, `abab*`, and `minimax/*` route to MiniMax.

Unknown models without an explicit route or exact account mapping fail closed.

## Admin Workflows

- Admins can create a group with platform `composite`.
- Admins can add, edit, delete, and preview composite model routes.
- Composite groups can copy accounts from concrete provider groups.
- Concrete provider accounts can be assigned directly to composite groups from
  account create/edit and bulk account workflows.
- Subscription payment plans can bind to a composite group when that group's
  `subscription_type` is `subscription`. The plan grants access to the
  composite group; each request is still billed and quota-checked against the
  resolved concrete provider platform.
- Channel configuration exposes composite groups in concrete provider sections.
  The channel `group_ids` payload is still flat; provider-specific model
  mapping and pricing remain keyed by concrete platform.

## Bucket 2 Setup: OpenAI + Claude + Gemini + Grok

Use one composite subscription group when one customer-facing plan should expose
model aliases across OpenAI, Claude, Gemini, and Grok without issuing separate
keys per provider.

1. Create concrete provider groups for the upstream account pools, for example
   `OpenAI Paid`, `Claude Paid`, `Gemini Paid`, and `Grok Paid`.
2. Create a `composite` group with `subscription_type` set to `subscription`.
3. Assign provider accounts directly to the composite group, or copy accounts
   from the concrete provider groups during group creation.
4. Add explicit routes for public aliases that should not rely on built-in
   model detection:

   | Public model | Endpoint | Target platform | Upstream model |
   | --- | --- | --- | --- |
   | `all/gpt-5` | `responses` | `openai` | `gpt-5` |
   | `all/claude-sonnet` | `messages` | `anthropic` | `claude-sonnet-4-6` |
   | `all/gemini-pro` | `gemini` | `gemini` | `gemini-2.5-pro` |
   | `all/grok` | `responses` | `grok` | `grok-4.3` |

5. Configure channel pricing and model mapping under the concrete platforms
   named in each route. Composite routing does not create pricing records.
6. Create a subscription payment plan for the composite group.

The same composite group can also rely on built-in detection for standard model
names such as `gpt-*`, `claude-*`, `gemini-*`, and `grok-*`. Explicit routes are
recommended for bundled plan aliases because they make endpoint, provider, and
upstream model attribution reviewable in the admin UI.

## Limits

Composite routes choose a concrete provider and upstream model; they do not
create synthetic model metadata, pricing, or upstream capability records by
themselves. Keep channel pricing/model mapping configured for the concrete
provider platforms that the routes target.

Automatic cross-provider failover applies to the HTTP text endpoints above.
WebSocket connections, Live sessions, media jobs, and System One retain their
dedicated provider/owner behavior. It selects providers for the configured public
model; it does not substitute unrelated models for an abstract task.
