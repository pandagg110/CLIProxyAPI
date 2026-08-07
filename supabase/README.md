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
- `target`, `object_key`, `archive_sha256`, and `manifest_sha256`:
  uploaded-object identity.
- `hour`, `timezone`, and `usage_date`: `usage_date` must be the date of `hour`
  in the named IANA timezone.
- `source_count`, `source_bytes`, and `jsonl_bytes`: nonnegative batch totals
  that must equal the sums of the `usage` rows.
- `compressed_bytes`: nonnegative compressed archive size.
- `is_test`: marks synthetic data.
- `usage`: unique exact `key_name` plus provider rows with nonnegative
  `source_count`, `source_bytes`, and `jsonl_bytes`.

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

`key_name` is stored and returned exactly as submitted. It is a
display/statistics label, not an API key value.

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
`total_names`, `names`, `days`, `cells`, and `last_synced_at`. `names` is the
requested page. `days` contains every date in the requested range. `cells`
contains recorded name/date combinations; an explicit all-zero cell is retained,
while a gap has no cell entry.

The database applies a global live-data switch. While no batch with
`is_test=false` exists, the public RPC shows synthetic rows. As soon as any live
batch exists, all test rows are excluded from public results, including test
rows on other dates.

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
`seed.sql` deterministically creates seven days of synthetic `is_test=true` data
for 张三, 李四, and 王五 across all three providers, with both explicit zeroes
and missing-day gaps.

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
