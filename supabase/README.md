# Supabase log usage backend

This directory contains the private ingestion path and public daily statistics
API for name-based log volume reporting. It stores batch metadata and exact
`key_name` labels, but it must never receive a raw API key, access token,
authorization header, or raw log content.

## Data contract

Send an event with `POST /functions/v1/ingest-log-usage` and these headers:

```http
Authorization: Bearer <LOG_STATS_INGEST_TOKEN>
Content-Type: application/json
```

See [`examples/ingest-event.json`](examples/ingest-event.json) for a complete
request. The request contains:

- `schema_version`: must be `1`.
- `event_id`: immutable idempotency key for one uploaded batch.
- `target_id`, `object_key`, `archive_sha256`, and `manifest_sha256`:
  uploaded-object identity.
- `hour_start`, `timezone`, and `usage_date`: `usage_date` must be the date of
  `hour_start` in the named IANA timezone. `hour_start` must include `Z` or a
  numeric UTC offset.
- `source_count`, `source_bytes`, and `jsonl_bytes`: nonnegative batch totals
  that must equal the sums of the `usage` rows.
- `compressed_bytes`: nonnegative compressed archive size.
- `test_mode`: optional boolean that marks synthetic data and defaults to
  `false`. External `is_test` is rejected; `is_test` is only the internal table
  column.
- `usage`: unique exact `key_name` plus provider rows with nonnegative
  `source_count`, `source_bytes`, and `jsonl_bytes`.

The top-level object and every `usage` object reject unknown fields. Rejection
messages are generic and do not include submitted values. `object_key` must be a
relative object-store key: URLs, URI schemes, query strings, fragments, absolute
paths, backslashes, and `..` traversal segments are rejected. `key_name` remains
an exact display label, but API-key prefixes, bearer tokens, JWT-shaped strings,
and long token-like ASCII strings are rejected.

The edge function computes SHA-256 over the exact raw request body and passes
that digest to `ingest_log_usage_v1`. Repeating the same `event_id` and digest
returns:

```json
{ "status": "duplicate", "event_id": "..." }
```

Reusing an `event_id` with different content returns HTTP 409. Invalid JSON
returns 400, missing or incorrect ingest authorization returns 401, and contract
validation failures return 422. The function does not log the request body or
authorization token.

Supported providers and public dashboard fields are:

| Ingest provider | Daily cell field | Meaning                                |
| --------------- | ---------------- | -------------------------------------- |
| `codex`         | `gpt_bytes`      | Sum of per-name normalized JSONL bytes |
| `fable5`        | `claude_bytes`   | Sum of per-name normalized JSONL bytes |
| `grok45`        | `grok_bytes`     | Sum of per-name normalized JSONL bytes |

Accepted `key_name` values are stored and returned exactly as submitted.

## Public dashboard API

`GET /functions/v1/log-usage-dashboard` serves the generated dashboard HTML. The
committed `dashboard_html.ts` is a safe deployment error page and is intended to
be overwritten by the frontend build.

`GET /functions/v1/log-usage-dashboard?api=daily` returns JSON. Required and
optional query parameters are:

- `from=YYYY-MM-DD` and `to=YYYY-MM-DD`: required, inclusive, maximum 366 days.
- `search`: optional name substring, maximum 100 characters.
- `page`: optional integer greater than or equal to 1; default `1`.
- `page_size`: optional integer from 1 to 20; default `20`.

The compact response contains `timezone`, `from`, `to`, `using_test_data`,
`pagination`, `names`, `days`, `cells`, and `latest_sync_at`. `pagination`
contains `page`, `page_size`, and the pre-pagination name `total`. `names` is
the requested page. `days` contains every date in the requested range. Each cell
contains `date`, `key_name`, total `jsonl_bytes`, provider totals `gpt_bytes`,
`claude_bytes`, and `grok_bytes`, plus `batch_count`, which counts distinct
events for that name/date. Recorded all-zero cells are retained; a date with no
event has no cell entry while remaining present in `days`.

The database applies a global live-data switch. While no batch stored with the
internal `is_test=false` value exists, the public RPC shows synthetic rows. As
soon as any live batch exists, all test rows are excluded from public results,
including test rows on other dates.

## Security model

Both tables have RLS enabled and direct table privileges are revoked from `anon`
and `authenticated`.

- `ingest_log_usage_v1` is `SECURITY DEFINER` with a fixed search path and is
  executable only by `service_role`.
- `get_public_daily_usage` is `SECURITY DEFINER` with a fixed search path and is
  executable only by `anon` and `authenticated`.
- Edge platform JWT verification is disabled for both functions. Ingestion uses
  the independent `LOG_STATS_INGEST_TOKEN`; the dashboard is intentionally
  public and reads through the restricted RPC.
- `SUPABASE_URL`, `SUPABASE_ANON_KEY`, and `SUPABASE_SERVICE_ROLE_KEY` are
  supplied by the Supabase Edge Functions environment. Do not commit or embed
  their values.

## Local verification

From the repository root:

```bash
deno test --allow-env --allow-read supabase/functions supabase/tests/backend_contract_test.ts
supabase start
supabase db reset --local --sql-paths seed.sql
psql "postgresql://postgres:postgres@127.0.0.1:54322/postgres" \
  -v ON_ERROR_STOP=1 \
  -f supabase/tests/log_usage_assertions.sql
```

The SQL assertion script runs in a transaction and rolls back its own test rows.
`seed.sql` deterministically creates synthetic `test_mode=true` events for the
dashboard range `2026-08-01` through `2026-08-07`. It covers 张三, 李四, and
王五 across all three providers, includes an explicit zero cell, and
intentionally omits the entire `2026-08-04` event date while `days` still
returns all seven dates. Nonzero JSONL byte values span multiple orders of
magnitude.

## Deployment

The following commands document deployment to project `anloatxlyajorkfhbaak`.
Review the migration and set a new high-entropy token before running them:

```bash
supabase link --project-ref anloatxlyajorkfhbaak
supabase db push
supabase secrets set LOG_STATS_INGEST_TOKEN='<long-random-value>'
supabase functions deploy ingest-log-usage --no-verify-jwt
supabase functions deploy log-usage-dashboard --no-verify-jwt
```

No deployment is performed by the repository tests.
