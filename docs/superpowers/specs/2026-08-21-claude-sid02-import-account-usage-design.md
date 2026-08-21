# Claude sid02 Import and Account Usage Integration Design

**Status:** Approved for specification review  
**Date:** 2026-08-21  
**Repository:** CLIProxyAPI

## Goal

Add a management-only bulk import flow that converts Claude sid02 session
keys into normal Claude OAuth credentials, stores them in CPA's existing Claude
auth JSON format, and exposes a stable account catalog that an external
account-seller page can join with the existing Token Usage Statistics plugin.

The Token Usage Statistics plugin remains the source of truth for token
persistence, model pricing, cache pricing, dollar totals, time filtering, and
export. CPA supplies credential acquisition, safe persistence, stable account
identity, and complete usage-record identity/token fields. CPA must not create a
second cost ledger or silently produce a competing dollar total.

## Scope and boundaries

### In scope

1. A Claude authentication service method for the synchronous
   sid02 -> organization -> PKCE authorization -> OAuth token exchange.
2. A management API for newline/JSON bulk import with per-line results.
3. Reuse of ClaudeTokenStorage, saveTokenRecord, the configured token store,
   and the existing watcher/auth-manager reload path.
4. An authenticated Claude account catalog API containing non-secret account
   metadata and stable join keys.
5. Usage integration guarantees: AuthID, AuthIndex, account source, and
   Claude's uncached input/cache-read/cache-write/output buckets reach the
   existing usage plugin. The Redis usage payload also carries auth_id for
   consumers that use the queue rather than the in-process plugin callback.
6. An external management-center page contract and visual specification based
   on the supplied reference image. The CPA repository does not gain a new
   frontend application.

### Out of scope

- Storing raw sid02 values, even encrypted, in auth files, usage records,
  logs, or API responses.
- Reimplementing the Token Usage Statistics plugin's storage, price table,
  cost arithmetic, charting, export, or reset behavior.
- Adding a second CPA-owned durable cost database.
- Bypassing the existing management authentication middleware.
- Browser OAuth callback handling for this flow; the existing browser flow
  remains unchanged.
- Changing provider translators or inference-time timeout behavior.

## Existing context and constraints

CPA already has the pieces needed for the persistence half of this feature:

- internal/auth/claude/ClaudeAuth owns PKCE, token exchange, profile lookup,
  refresh, device IDs, and ClaudeTokenStorage creation.
- internal/api/handlers/management/Handler.saveTokenRecord routes saves
  through the configured file/object/Postgres/git token store and existing
  post-auth hooks.
- sdk/cliproxy/usage.Record and sdk/pluginapi.UsageRecord already include
  AuthID, AuthIndex, Source, and the canonical token breakdown.
- The plugin management router can serve a plugin-owned usage page under the
  management center. The supplied page already presents date filters, pricing,
  token totals, dollar estimates, and export controls.

Repository rules that shape the implementation:

- Credential-acquisition requests may use a context deadline; inference
  connections must not acquire a new timeout.
- Logs and errors must not contain secrets or full OAuth tokens.
- New Go code is formatted with gofmt, tested with go test ./..., and compiled
  with go build -o test-output ./cmd/server && rm test-output.

## Chosen architecture

The feature has two bounded units joined by auth_index/auth_id:

1. **CPA Claude session importer.** A synchronous service method performs the
   three control-plane calls and returns a normal ClaudeAuthBundle. A
   management handler validates a batch, persists each successful bundle, and
   returns only redacted status data.
2. **Plugin-backed account usage projection.** CPA exposes a catalog of Claude
   accounts and emits stable usage identity/buckets. The existing Token Usage
   Statistics plugin aggregates and prices those records. The external page
   joins catalog rows to the plugin's usage view; no CPA cost calculation is
   introduced.

### Why this boundary

Keeping pricing in one component prevents drift when Anthropic changes model,
cache, region, or mode prices. Keeping sid02 handling in the Claude auth
package makes it reusable by the management API and testable without Gin. The
catalog API is deliberately independent of the plugin's storage schema, so a
plugin update cannot expose OAuth secrets or require auth-file parsing in the
frontend.

## Data flow

    flowchart LR
        UI["External seller page textarea"] -->|management auth| IMPORT["POST /v0/management/claude/session-import"]
        IMPORT --> PARSE["Validate lines and redact errors"]
        PARSE --> EXCHANGE["ClaudeAuth.ExchangeSessionKey"]
        EXCHANGE --> STORAGE["ClaudeTokenStorage"]
        STORAGE --> SAVE["saveTokenRecord and configured token store"]
        SAVE --> WATCH["watcher/auth manager"]
        WATCH --> EXEC["Claude request executor"]
        EXEC --> RECORD["UsageRecord with AuthID/AuthIndex and token buckets"]
        RECORD --> PLUGIN["Token Usage Statistics plugin"]
        CATALOG["GET /v0/management/claude/accounts"] --> UI
        PLUGIN -->|auth_index keyed usage/cost| UI

The imported sid02 is only an input to EXCHANGE. It is never passed to
STORAGE, RECORD, PLUGIN, or the response serializer.

## sid02 exchange design

### Public service contract

Add a method on ClaudeAuth with a narrow result contract:

    type SessionKeyScope string

    const (
        SessionKeyScopeFull      SessionKeyScope = "full"
        SessionKeyScopeInference SessionKeyScope = "inference"
    )

    func (a *ClaudeAuth) ExchangeSessionKey(
        ctx context.Context,
        sessionKey string,
        scope SessionKeyScope,
    ) (*ClaudeAuthBundle, error)

full is the default because the target is Claude Code and maps to CPA's
existing ClaudeOAuthScope value (`user:profile user:inference
user:sessions:claude_code user:mcp_servers user:file_upload`). inference maps to
the narrow inference scope used by the Claude control plane. The method accepts
an optional proxy through the existing NewClaudeAuthWithProxyURL constructor;
it does not accept or return a raw cookie jar.

### Control-plane calls

The method performs these calls in order, with the same HTTP client and an
explicit cookie on each request because the existing Anthropic client has no
cookie jar:

1. GET https://claude.ai/api/organizations
   - Cookie: sessionKey set to the submitted session key
   - Select the first usable organization returned by Claude. An empty or
     malformed organization list is a typed import failure.
2. Generate a fresh PKCE verifier/challenge and cryptographically random
   state. POST https://claude.ai/v1/oauth/{organization_uuid}/authorize
   - Cookie: sessionKey set to the submitted session key
   - Body contains the public Claude OAuth client ID, organization UUID,
     redirect URI https://platform.claude.com/oauth/code/callback, selected
     scope, state, and S256 challenge.
   - Parse the returned redirect URI and require both a non-empty code and a
     matching state fragment. A state mismatch is rejected before token
     exchange.
3. POST https://platform.claude.com/v1/oauth/token
   - Exchange the code with grant_type=authorization_code, the platform
     redirect URI, client ID, verifier, and state.
   - Parse access token, refresh token, expiry, organization, and account
     fields using the existing token response type.

The authorize response is expected to contain a redirect URI (the exact JSON
envelope may vary); the parser extracts its code and the state fragment from
that URI without logging the URI. A response with no usable redirect URI is an
authorization_failed result.

The existing code-to-token helper currently hard-codes the browser callback
redirect URI. Refactor it into an internal helper that accepts a redirect URI;
the existing browser flow continues to pass its current localhost URI while
the sid02 flow passes the platform callback URI. This avoids a redirect-URI
mismatch without changing browser login behavior.

After exchange, reuse the existing advisory profile/roles lookups and device
pool generation. Profile data may fill missing email/account/organization
fields but must not make a successful token exchange fail solely because an
advisory lookup is unavailable.

### Validation and secrecy

- Trim surrounding whitespace and reject an empty line before any network call.
- Accept only the expected Claude session-key form; do not echo the value in
  validation errors.
- Wrap each item in a credential-acquisition context deadline of 60 seconds.
- Use a bounded worker count of two to reduce rate-limit pressure while still
  allowing useful batch throughput. Results retain input order.
- Never log request bodies, cookies, authorization codes, session keys, or
  access/refresh tokens. The import handler marks the request as excluded from
  Gin body logging before reading it.
- Return stable error codes: invalid_input, organization_lookup_failed,
  authorization_failed, token_exchange_failed, identity_missing, and
  save_failed. Human-readable messages contain no secret or upstream response
  body.

## Auth JSON persistence and idempotency

For each successful bundle, construct the same ClaudeTokenStorage shape used by
the browser and CLI login paths:

- access_token, refresh_token, expired, and last_refresh;
- email, account_uuid, organization_uuid, and organization_name;
- generated claude_device_ids and type: claude.

Metadata includes auth_kind: oauth plus the identity/device fields already used
by the executor and synthesizer. It never includes sid02.

When a request item supplies a label, the label is stored through the existing
Auth.Label/metadata path so a subsequent catalog response can display it without
re-reading the submitted session key.

### File naming and duplicate policy

1. Use account_uuid as the primary idempotency key.
2. If an existing Claude auth has that account UUID, update its token fields
   in place and preserve operator metadata through the existing metadata merge
   path. The result status is updated.
3. Otherwise use claude-[normalized-email].json. If that name belongs to a
   different account, append -[first-eight-account-uuid-characters] before
   .json. If email is absent, use the account UUID in the base name.
4. If both account UUID and email are absent, do not save the credential and
   return identity_missing.

The handler calls saveTokenRecord rather than writing files directly. This
preserves configured storage backends, file permissions, hooks, and watcher
reload behavior. The API never returns OAuth token fields; it returns only a
redacted account label, file name, status, and stable join identifiers.

## Management API

All routes live under the existing management group and therefore inherit the
availability and management-password middleware.

### POST /v0/management/claude/session-import

Request body (one of text or items is required):

    {
      "text": "sk-ant-sid02-...\\nsk-ant-sid02-...",
      "items": [
        {"sid02": "sk-ant-sid02-...", "label": "supplier-a"}
      ],
      "scope": "full",
      "proxy_url": ""
    }

Rules:

- Maximum body size: 1 MiB; maximum entries: 500.
- items takes precedence when both forms are present; blank text lines are
  ignored, but a non-empty malformed line receives its own failure result.
- scope defaults to full and accepts only full or inference.
- proxy_url is optional and is used only for credential acquisition; it is not
  automatically persisted as account metadata.

Response shape:

    {
      "total": 2,
      "succeeded": 1,
      "failed": 1,
      "items": [
        {
          "line": 1,
          "status": "saved",
          "auth_id": "claude-user@example.com.json",
          "auth_index": "9f3b1a2c7d8e4f10",
          "file_name": "claude-user@example.com.json",
          "account": "u***@example.com",
          "organization": "Example Org"
        },
        {
          "line": 2,
          "status": "failed",
          "error_code": "token_exchange_failed",
          "message": "Claude token exchange failed"
        }
      ]
    }

auth_id is a non-secret file identity, not an OAuth token. Failed results
contain only the input line number and redacted error data.

### GET /v0/management/claude/accounts

Query parameters:

- search: case-insensitive search over masked account, organization, file name,
  and auth index;
- auth_index: exact lookup;
- status: active, disabled, or all (default all);
- page and page_size, bounded to protect the management process.

Response shape:

    {
      "accounts": [
        {
          "auth_id": "claude-user@example.com.json",
          "auth_index": "9f3b1a2c7d8e4f10",
          "file_name": "claude-user@example.com.json",
          "account": "u***@example.com",
          "organization": "Example Org",
          "provider": "claude",
          "status": "active",
          "disabled": false,
          "created_at": "2026-08-21T09:00:00Z",
          "updated_at": "2026-08-21T09:00:00Z",
          "usage_join_key": "auth_index"
        }
      ],
      "total": 1,
      "page": 1,
      "page_size": 50
    }

This endpoint is a catalog, not a cost calculator. The external page joins
usage_join_key to the existing plugin's account-level usage response. If the
deployed plugin exposes its data under a plugin management route, the page
uses that route directly; CPA does not copy or transform the plugin's durable
ledger.

### Per-account usage API ownership

The approved per-account dollar API is the Token Usage Statistics plugin's
management API, discovered from the plugin management registration rather than
hard-coded into CPA. Its account query contract is:

    {
      "accounts": [
        {
          "auth_index": "9f3b1a2c7d8e4f10",
          "requests": 119,
          "failed": 5,
          "tokens": {
            "uncached_input": 1195,
            "cache_read": 13852161,
            "cache_write": 0,
            "output": 161527
          },
          "cost": {
            "currency": "USD",
            "total": "30.26",
            "status": "estimated"
          },
          "pricing": {
            "version": "plugin-defined",
            "effective_at": "2026-08-21T00:00:00Z"
          }
        }
      ]
    }

The external page joins this response to the CPA account catalog by
auth_index. The plugin may report an exact or estimated status when a usage
record lacks cache-TTL, region, fast-mode, or other price modifiers. CPA does
not reinterpret that status. If the deployed plugin currently exposes the
same data under a different route or field name, its adapter maps to this
contract; no second price calculation is added to CPA.

## Usage plugin contract

### Identity

For every Claude request, UsageReporter must continue to emit:

- AuthID: the auth record ID/file identity;
- AuthIndex: the stable index used by the management catalog;
- Source: the account email or other non-secret account label;
- AuthType: oauth;
- model and requested alias.

The external page uses AuthIndex as its primary join key and displays the
redacted catalog account. AuthID is retained as a secondary diagnostic key.

### Token buckets

The plugin receives the canonical Claude buckets without double counting:

- uncached input tokens;
- cache-read input tokens;
- cache-write input tokens;
- total output tokens, including thinking output.

Detail.InputTokens is the uncached input count for Claude; it must not be
added to the cache buckets a second time. The plugin remains responsible for
its official-price table and for labeling estimates when cache TTL or other
price modifiers are not present in the record.

### Queue compatibility

The built-in Redis usage payload is extended with an optional auth_id field.
Existing consumers that ignore unknown JSON fields remain compatible. The
existing auth_index, access_token_sha256, and token breakdown fields stay
unchanged. Raw access/refresh token values are never serialized.

## External page contract and visual behavior

The page is implemented by the external management center/plugin, not by a new
CPA frontend. It follows the supplied reference image:

1. **Import card:** title “批量导入 Claude sid02”, multiline textarea,
   Claude OAuth/scope selector, optional label mode, and a blue submit button.
   A warning states that sid02 is used only for one-time exchange and is not
   stored.
2. **Result summary:** total, succeeded, failed, and a retry-failed action.
3. **Account table:** masked account, organization, status, real-time USD total
   from the token plugin, created time, and a detail link. Search, refresh, date
   range, and export controls match the existing plugin style.
4. **Detail action:** opens the existing Token Usage Statistics page filtered
   to the selected auth_index; no token material is shown.

The page must render partial batch failures without losing successful rows and
must never place the submitted textarea value into browser history, analytics,
or error telemetry.

## Error handling and operational behavior

- A malformed line does not abort other lines.
- A failed exchange does not create a partial auth JSON file.
- A save failure is reported separately from an upstream exchange failure.
- If the process is cancelled, in-flight credential exchanges stop and no
  incomplete credential is persisted.
- Duplicate account imports are safe to retry and produce updated rather than
  an additional active credential.
- Logging includes only batch size, line number, error code, and aggregate
  success/failure counts. It never includes session keys, authorization codes,
  tokens, cookies, or raw upstream bodies.

## Testing strategy

### Claude auth package

Use a fake http.RoundTripper to test the complete exchange without network
access:

- organization GET has the expected session cookie and rejects empty lists;
- authorize POST has the expected organization, scope, PKCE challenge, and
  state, and returns a code/state redirect;
- token POST uses the platform redirect URI and verifier;
- state mismatch and malformed redirect responses fail safely;
- profile/roles advisory failures do not discard a valid token exchange;
- no test error or log string contains the supplied session key.

Retain all existing browser OAuth tests, especially the fixed localhost
redirect body/order assertions.

### Management handler

Add handler tests for:

- JSON and newline batch parsing;
- bounded per-line partial success;
- idempotent update of an existing account;
- file naming collision behavior;
- save failure and upstream failure error codes;
- management authentication and request-body logging exclusion;
- response JSON containing no sid02/access/refresh token substrings.

### Usage integration

Add tests that:

- publish a Claude usage record and assert AuthID, AuthIndex, Source, and all
  four token buckets;
- serialize a Redis usage payload and assert auth_id is present while token
  secrets are absent;
- verify Claude input/cache buckets are not double counted.

### Verification commands

After implementation, run:

    Run gofmt on every changed Go file.
    go test ./...
    go build -o test-output ./cmd/server && rm test-output

## Rollout and compatibility

The new routes are additive and protected by the existing management secret.
Existing browser OAuth, auth-file upload, refresh, and plugin routes remain
unchanged. The Redis payload addition is optional and backward-compatible for
JSON consumers. If an older Token Usage Statistics plugin does not yet group by
auth_index, it can continue to display its existing aggregate view while the
account catalog remains usable; per-account dollar rows become available after
the plugin consumes the already-present AuthID/AuthIndex fields.
