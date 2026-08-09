# Supabase log usage backend

This directory contains the private ingestion path and public daily and hourly
APIs for name-based log volume reporting. It stores batch metadata and exact
`key_name` labels. The public per-name metric is exact `source_bytes`, matching
the server uploader's complete source-log accounting. It must never receive a raw API key, access token,
authorization header, or raw log content.

## Data contract

Send an event with `POST /functions/v1/ingest-log-usage` and these headers:

```http
Authorization: Bearer <LOG_STATS_INGEST_TOKEN>
Content-Type: application/json
```

See [`examples/ingest-event.json`](examples/ingest-event.json) for a live exact
request and [`examples/ingest-history-event.json`](examples/ingest-history-event.json)
for local-ledger history that intentionally has no per-name JSONL allocation.
The request contains:

- `schema_version`: must be `1`.
- `event_id`: immutable idempotency key for one uploaded batch. It must begin
  with an ASCII letter or digit and contain only ASCII letters, digits, `.`,
  `_`, `:`, or `-`.
- `target_id`: a target slug using the same restricted identifier characters.
- `object_key`, `archive_sha256`, and `manifest_sha256`: uploaded-object
  identity.
- `hour_start`, `timezone`, and `usage_date`: `usage_date` must be the date of
  `hour_start` in the named IANA timezone. `hour_start` must include `Z` or a
  numeric UTC offset.
- `source_count`, `source_bytes`, and `jsonl_bytes`: nonnegative batch totals
  that must equal the sums of the `usage` rows for exact events. Batch JSONL
  remains exact for history events even though per-name JSONL is unknown.
- `compressed_bytes`: nonnegative compressed archive size.
- `usage_precision`: optional `exact | batch_only`; defaults to `exact`.
  `exact` requires every usage row to include `jsonl_bytes` and validates all
  three sums. `batch_only` requires per-name `jsonl_bytes` to be omitted or
  `null`, and validates only `source_count` and `source_bytes` sums.
- `test_mode`: optional boolean that marks synthetic data and defaults to
  `false`. External `is_test` is rejected; `is_test` is only the internal table
  column.
- `usage`: unique exact `key_name` plus provider rows with nonnegative
  `source_count` and `source_bytes`, and precision-dependent `jsonl_bytes`.

The top-level object and every `usage` object reject unknown fields. Rejection
messages are generic and do not include submitted values. `object_key` must be a
relative object-store key: URLs, URI schemes, query strings, fragments, absolute
paths, backslashes, and `..` traversal segments are rejected. `key_name` remains
an exact display label, but API-key prefixes, bearer tokens, JWT-shaped strings,
and long token-like ASCII strings are rejected. After trimming for validation,
names must contain from 1 to 48 Unicode code points and must not start with
`cpa_` in any letter case. Rejection errors never echo the submitted name.

`event_id` and `target_id` also reject secret prefixes, JWT-shaped values, URLs,
paths, whitespace, and log-like content. These checks run independently in the
Edge function and SQL RPC.

The edge function computes SHA-256 directly over the exact raw request bytes
before decoding them as strict UTF-8 and parsing JSON, then passes that digest
to `ingest_log_usage_v1`. This preserves byte differences such as a UTF-8 BOM.
Repeating the same `event_id` and digest returns:

```json
{ "status": "duplicate", "event_id": "..." }
```

Reusing an `event_id` with different content returns HTTP 409. Invalid UTF-8 or
invalid JSON returns 400 without echoing the body, missing or incorrect ingest
authorization returns 401, and contract validation failures return 422. The
function does not log the request body or authorization token.

Supported providers and public dashboard fields are:

| Ingest provider | Public source field   | Meaning                         |
| --------------- | --------------------- | ------------------------------- |
| `codex`         | `gpt_source_bytes`    | Complete source-log bytes by name |
| `fable5`        | `claude_source_bytes` | Complete source-log bytes by name |
| `grok45`        | `grok_source_bytes`   | Complete source-log bytes by name |

Accepted `key_name` values are stored and returned exactly as submitted.

## Public dashboard API

`GET /functions/v1/log-usage-dashboard` serves the generated dashboard HTML. The
committed `dashboard_html.ts` is a safe deployment error page and is intended to
be overwritten by the frontend build.

The restricted database API exposes two public reporting RPCs:

- `get_public_daily_usage` returns an exact source-byte summary and daily totals
  for the requested range. These rollups are independent of the 20-name
  transport page, so pagination changes only `names` and `cells`.
- `get_public_hourly_usage` accepts one date and one exact key_name returned by
  the daily RPC. Its `hours` array contains sparse archive hours, not
  request-event hours, and does not synthesize archive-free hours.

`GET /functions/v1/log-usage-dashboard?api=daily` returns JSON. Required and
optional query parameters are:

- `from=YYYY-MM-DD` and `to=YYYY-MM-DD`: required, inclusive, maximum 366 days.
- `search`: optional case-insensitive literal name substring, maximum 100
  characters.
- `page`: optional integer from 1 to 2147483647; default `1`.
- `page_size`: optional integer from 1 to 20; default `20`.

The compact response contains `metric_basis: "source_bytes"`, `timezone`,
`from`, `to`, `using_test_data`, `pagination`, `names`, `days`, `cells`,
`summary`, `daily_totals`, and `latest_sync_at`. `pagination`
contains `page`, `page_size`, and the pre-pagination name `total`. `names` is
the requested page. `days` contains every date in the requested range. Each cell
contains `date`, `key_name`, exact total `source_bytes`, provider source totals
`gpt_source_bytes`, `claude_source_bytes`, and `grok_source_bytes`, plus exact
`source_count`, `usage_precision`, and `batch_count`. `summary` contains exact
source bytes and page-independent archive, archive-hour, active-key, and day
counts. `daily_totals` contains one ascending entry for every value in `days`,
including zero totals for dates with no matching event. Recorded all-zero cells
are retained; a date with no event has no cell entry while remaining present in
`days`.

For cells whose every contributing event is `exact`, `jsonl_bytes` and the
legacy provider JSONL fields `gpt_bytes`, `claude_bytes`, and `grok_bytes` are
exact decimal strings. If any contributing event is `batch_only`, all four
per-name JSONL fields are JSON `null`; they are never estimated or allocated.
Historical `batch_only` data does not fabricate per-key JSONL or compressed
bytes. The hourly RPC exposes source metrics only and never returns object keys
or checksum fields.

All available byte-total fields are base-10 JSON strings so values above
JavaScript's safe-integer limit remain exact. `batch_count` and all `pagination` fields
remain JSON numbers. Ingest request byte fields remain safe-integer JSON
numbers. See [`examples/daily-response.json`](examples/daily-response.json) and
[`examples/hourly-response.json`](examples/hourly-response.json) for complete
public RPC response examples.

The database applies a global live-data switch. While no batch stored with the
internal `is_test=false` value exists, the public RPC shows synthetic rows. As
soon as any live batch exists, all test rows are excluded from public results,
including test rows on other dates.

## Security model

Both underlying tables remain private with RLS enabled, and direct table
privileges are revoked from `anon` and `authenticated`.

- `ingest_log_usage_v1` is `SECURITY DEFINER` with a fixed search path and is
  executable only by `service_role`.
- `get_public_daily_usage` is `SECURITY DEFINER` with a fixed search path and is
  executable only by `anon` and `authenticated`.
- `get_public_hourly_usage` is `SECURITY DEFINER` with a fixed search path and is
  executable only by `anon` and `authenticated`.
- Edge platform JWT verification is disabled for both Edge Functions. Ingestion uses
  the independent `LOG_STATS_INGEST_TOKEN`; the dashboard is intentionally
  public and reads through the restricted RPC.
- `SUPABASE_URL`, `SUPABASE_ANON_KEY`, and `SUPABASE_SERVICE_ROLE_KEY` are
  supplied by the Supabase Edge Functions environment. Do not commit or embed
  their values.

## Local verification

From the repository root:

```powershell
deno test --allow-env --allow-read supabase/functions supabase/tests/backend_contract_test.ts
supabase start
supabase db reset --local --sql-paths seed.sql
Get-Content -Raw -LiteralPath 'supabase/tests/log_usage_assertions.sql' |
  docker exec -i supabase_db_CLIProxyAPI psql -U postgres -d postgres -v ON_ERROR_STOP=1
```

The PowerShell pipeline targets this repository's default local Supabase
container (`project_id = "CLIProxyAPI"`); use it when `psql` is unavailable on
the host. If `psql` is installed on the host, the equivalent portable command
is:

```bash
psql "postgresql://postgres:postgres@127.0.0.1:54322/postgres" \
  -v ON_ERROR_STOP=1 \
  -f supabase/tests/log_usage_assertions.sql
```

The Deno backend contract tests inspect migration and assertion source
contracts; they do not execute PostgreSQL. One of the `psql` commands is required
for runtime validation of migrations, privileges, pagination, exact aggregates,
and search behavior. The SQL assertion script runs in a transaction and rolls back
its own test rows. `seed.sql` deterministically creates synthetic
`test_mode=true` events for the dashboard range `2026-08-01` through
`2026-08-07`. It covers 张三, 李四, and 王五 across all three providers,
includes an explicit zero cell, and intentionally omits the entire `2026-08-04`
event date while `days` still returns all seven dates. Nonzero JSONL byte values
span multiple orders of magnitude.

## Deployment

Review the migration and set a new high-entropy token before running deployment:

```bash
supabase link --project-ref '<project-ref>'
supabase db push
supabase secrets set LOG_STATS_INGEST_TOKEN='<long-random-value>'
supabase functions deploy ingest-log-usage --no-verify-jwt
supabase functions deploy log-usage-dashboard --no-verify-jwt
```

No deployment is performed by the repository tests.
