import assert from "node:assert/strict";

const root = new URL("../", import.meta.url);

async function read(relativePath: string): Promise<string> {
  return await Deno.readTextFile(new URL(relativePath, root));
}

function compact(value: string): string {
  return value.toLowerCase().replace(/\s+/g, " ");
}

Deno.test("config disables platform JWT verification for both edge functions", async () => {
  const config = await read("config.toml");

  assert.match(
    config,
    /\[functions\.ingest-log-usage\][\s\S]*?verify_jwt\s*=\s*false/i,
  );
  assert.match(
    config,
    /\[functions\.log-usage-dashboard\][\s\S]*?verify_jwt\s*=\s*false/i,
  );
});

Deno.test("migration defines private batch and name-provider usage tables", async () => {
  const sql = compact(await read("migrations/20260808000000_log_usage.sql"));

  assert.match(sql, /create table public\.log_upload_batches/);
  for (
    const column of [
      "event_id text primary key",
      "target_id text not null",
      "object_key text not null",
      "archive_sha256 text not null",
      "manifest_sha256 text not null",
      "hour_start timestamptz not null",
      "timezone text not null",
      "usage_date date not null",
      "source_count bigint not null",
      "source_bytes bigint not null",
      "jsonl_bytes bigint not null",
      "compressed_bytes bigint not null",
      "payload_sha256 text not null",
      "is_test boolean not null",
      "ingested_at timestamptz not null default now()",
    ]
  ) {
    assert.ok(sql.includes(column), `missing batch column: ${column}`);
  }

  assert.match(sql, /create table public\.log_upload_usage/);
  assert.match(sql, /primary key \(event_id, key_name, provider\)/);
  assert.match(
    sql,
    /foreign key \(event_id\) references public\.log_upload_batches \(event_id\) on delete cascade/,
  );
  assert.match(sql, /provider in \('codex', 'fable5', 'grok45'\)/);
  for (const column of ["source_count", "source_bytes", "jsonl_bytes"]) {
    assert.match(sql, new RegExp(`check \\(${column} >= 0\\)`));
  }
});

Deno.test("incremental migration adds history precision and defensive name constraints", async () => {
  const sql = compact(
    await read(
      "migrations/20260809000000_harden_ingest_and_history_precision.sql",
    ),
  );

  assert.match(
    sql,
    /add column usage_precision text not null default 'exact'/,
  );
  assert.match(sql, /usage_precision in \('exact', 'batch_only'\)/);
  assert.match(sql, /alter column jsonl_bytes drop not null/);
  assert.match(sql, /jsonl_bytes is null or jsonl_bytes >= 0/);
  assert.match(
    sql,
    /(?:pg_catalog\.)?char_length\((?:pg_catalog\.)?btrim\(key_name\)\) <= 48/,
  );
  assert.match(
    sql,
    /(?:pg_catalog\.)?lower\(\s*(?:pg_catalog\.)?left\((?:pg_catalog\.)?btrim\(key_name\), 4\)\s*\) <> 'cpa_'/,
  );
});

Deno.test("forward migration hides unnamed public usage before search and pagination", async () => {
  const previousSql = compact(
    await read(
      "migrations/20260809000000_harden_ingest_and_history_precision.sql",
    ),
  );
  let migrationSource = "";
  try {
    migrationSource = await read(
      "migrations/20260810000000_hide_unnamed_key_usage.sql",
    );
  } catch (error) {
    if (!(error instanceof Deno.errors.NotFound)) {
      throw error;
    }
  }
  const sql = compact(migrationSource);
  const signature = "create or replace function public.get_public_daily_usage(";
  const filters = [
    "and u.key_name <> 'unauthenticated'",
    "and u.key_name !~* '^key-[0-9a-f]{12}$'",
    "and pg_catalog.btrim(u.key_name) <> ''",
  ];

  assert.match(
    sql,
    /create or replace function public\.get_public_daily_usage\(\s*p_from date, p_to date, p_search text, p_page integer, p_page_size integer\s*\)/,
    "missing forward public daily RPC migration",
  );

  const nameTotalsIndex = sql.indexOf("name_totals as (");
  const searchIndex = sql.indexOf("_search = ''", nameTotalsIndex);
  const groupByIndex = sql.indexOf("group by u.key_name", nameTotalsIndex);
  const pagedNamesIndex = sql.indexOf("paged_names as (", groupByIndex);
  assert.ok(nameTotalsIndex >= 0, "missing name_totals CTE");
  assert.ok(searchIndex > nameTotalsIndex, "search must be in name_totals");
  assert.ok(groupByIndex > searchIndex, "grouping must follow search");
  assert.ok(pagedNamesIndex > groupByIndex, "pagination must follow grouping");

  let unfilteredSql = sql;
  for (const filter of filters) {
    const filterIndex = sql.indexOf(filter, nameTotalsIndex);
    assert.ok(
      filterIndex > nameTotalsIndex && filterIndex < searchIndex,
      `public-name filter must precede search and grouping: ${filter}`,
    );
    unfilteredSql = unfilteredSql.replace(filter, "");
  }

  const previousDefinitionIndex = previousSql.indexOf(signature);
  assert.ok(previousDefinitionIndex >= 0, "missing prior public daily RPC");
  assert.equal(
    compact(unfilteredSql).trim(),
    previousSql.slice(previousDefinitionIndex).trim(),
    "forward migration must preserve the complete RPC definition and ACL",
  );
});

Deno.test("migration enables RLS, revokes direct reads, and grants only intended RPC roles", async () => {
  const sql = compact(await read("migrations/20260808000000_log_usage.sql"));

  for (const table of ["log_upload_batches", "log_upload_usage"]) {
    assert.ok(
      sql.includes(`alter table public.${table} enable row level security`),
    );
    assert.match(
      sql,
      new RegExp(
        `revoke all on table public\\.${table} from anon, authenticated`,
      ),
    );
  }
  assert.match(
    sql,
    /revoke all on function public\.ingest_log_usage_v1\(jsonb, text\) from public, anon, authenticated/,
  );
  assert.match(
    sql,
    /grant execute on function public\.ingest_log_usage_v1\(jsonb, text\) to service_role/,
  );
  assert.match(
    sql,
    /revoke all on function public\.get_public_daily_usage\(date, date, text, integer, integer\) from public, service_role/,
  );
  assert.match(
    sql,
    /grant execute on function public\.get_public_daily_usage\(date, date, text, integer, integer\) to anon, authenticated/,
  );
});

Deno.test("ingest migration source declares strict validation and bigint ranges", async () => {
  const sql = compact(
    await read(
      "migrations/20260809000000_harden_ingest_and_history_precision.sql",
    ),
  );

  assert.match(
    sql,
    /create or replace function public\.ingest_log_usage_v1\(payload jsonb, payload_sha256 text\)/,
  );
  assert.match(sql, /security definer set search_path = pg_catalog, public/);
  assert.match(sql, /schema_version/);
  assert.match(sql, /jsonb_object_keys/);
  assert.match(sql, /payload contains unsupported fields/);
  assert.match(sql, /usage entries contain unsupported fields/);
  assert.match(sql, /event_id must be a safe non-secret identifier/);
  assert.match(sql, /target_id must be a safe non-secret identifier/);
  assert.match(sql, /object_key must be a safe relative object key/);
  assert.match(sql, /key_name must be a display label, not a secret/);
  assert.match(sql, /test_mode/);
  assert.match(sql, /usage_precision/);
  assert.match(sql, /batch_only/);
  assert.match(sql, /(?:pg_catalog\.)?char_length\(_trimmed_key_name\) > 48/);
  assert.match(
    sql,
    /_trimmed_key_name ~\* '\^cpa_'/,
  );
  assert.doesNotMatch(sql, /payload -> 'is_test'/);
  assert.match(sql, /pg_catalog\.pg_timezone_names/);
  assert.match(sql, /usage_date must match hour_start in timezone/);
  assert.match(sql, /9223372036854775807/);
  assert.match(sql, /unique key_name and provider pairs/);
  assert.match(
    sql,
    /jsonb_typeof\(_usage_item -> 'provider'\) is distinct from 'string'/,
  );
  assert.match(sql, /if _provider not in/);
  assert.match(sql, /batch totals must equal usage totals/);
  assert.match(sql, /on conflict \(event_id\) do nothing/);
  assert.match(sql, /event_id_conflict/);
  assert.match(sql, /jsonb_build_object\('status', 'duplicate'/);
  assert.match(sql, /jsonb_build_object\('status', 'inserted'/);
});

Deno.test("public daily migration source declares live filtering and the cell contract", async () => {
  const sql = compact(
    await read(
      "migrations/20260810000000_hide_unnamed_key_usage.sql",
    ),
  );

  assert.match(
    sql,
    /create or replace function public\.get_public_daily_usage\(\s*p_from date, p_to date, p_search text, p_page integer, p_page_size integer\s*\)/,
  );
  assert.match(sql, /security definer set search_path = pg_catalog, public/);
  assert.match(
    sql,
    /not exists \( select 1 from public\.log_upload_batches where is_test = false \)/,
  );
  assert.match(sql, /b\.is_test = _using_test_data/);
  assert.match(sql, /b\.usage_date between p_from and p_to/);
  assert.match(sql, /where u\.provider = 'codex'/);
  assert.match(sql, /where u\.provider = 'fable5'/);
  assert.match(sql, /where u\.provider = 'grok45'/);
  for (
    const key of [
      "timezone",
      "metric_basis",
      "from",
      "to",
      "using_test_data",
      "pagination",
      "page",
      "page_size",
      "total",
      "names",
      "days",
      "cells",
      "latest_sync_at",
      "key_name",
      "source_bytes",
      "gpt_source_bytes",
      "claude_source_bytes",
      "grok_source_bytes",
      "usage_precision",
      "jsonl_bytes",
      "gpt_bytes",
      "claude_bytes",
      "grok_bytes",
      "batch_count",
    ]
  ) {
    assert.ok(sql.includes(`'${key}'`), `missing response key: ${key}`);
  }
  for (
    const field of [
      "source_bytes",
      "gpt_source_bytes",
      "claude_source_bytes",
      "grok_source_bytes",
    ]
  ) {
    assert.match(
      sql,
      new RegExp(`'${field}', cells\\.${field}::text`),
      `${field} must be serialized as exact decimal text`,
    );
  }
  assert.match(sql, /'metric_basis', 'source_bytes'/);
  assert.match(
    sql,
    /when cells\.all_exact then cells\.jsonl_bytes::text else null end/,
  );
  assert.match(sql, /order by names\.total_source_bytes desc, names\.key_name/);
  assert.match(
    sql,
    /offset \(\(p_page::bigint - 1::bigint\) \* p_page_size::bigint\)/,
  );
});

Deno.test("daily summary migration adds page-independent filtered rollups and exact ACL", async () => {
  const sql = compact(
    await read(
      "migrations/20260810010000_usage_dashboard_drilldown.sql",
    ),
  );

  assert.match(
    sql,
    /create or replace function public\.get_public_daily_usage\(\s*p_from date, p_to date, p_search text, p_page integer, p_page_size integer\s*\)/,
  );
  assert.match(sql, /security definer set search_path = pg_catalog, public/);

  const dailyFunctionStart = sql.indexOf(
    "create or replace function public.get_public_daily_usage",
  );
  const hourlyFunctionStart = sql.indexOf(
    "create or replace function public.get_public_hourly_usage",
  );
  const dailyBody = sql.slice(dailyFunctionStart, hourlyFunctionStart);
  assert.match(dailyBody, /eligible_batches as not materialized \(/);

  const eligibleBatchesIndex = sql.indexOf(
    "eligible_batches as not materialized (",
  );
  const filteredUsageIndex = sql.indexOf("filtered_usage as (");
  const summaryValuesIndex = sql.indexOf("summary_values as (");
  const dayValuesIndex = sql.indexOf("day_values as (");
  const dailyTotalValuesIndex = sql.indexOf("daily_total_values as (");
  const nameTotalsIndex = sql.indexOf("name_totals as (");
  const pagedNamesIndex = sql.indexOf("paged_names as (");
  const cellValuesIndex = sql.indexOf("cell_values as (");

  assert.ok(eligibleBatchesIndex >= 0, "missing eligible_batches CTE");
  assert.ok(
    filteredUsageIndex > eligibleBatchesIndex,
    "filtered_usage must follow eligible_batches",
  );
  assert.ok(
    summaryValuesIndex > filteredUsageIndex,
    "summary must follow filtered_usage",
  );
  assert.ok(dayValuesIndex > summaryValuesIndex, "days must follow summary");
  assert.ok(
    dailyTotalValuesIndex > dayValuesIndex,
    "daily totals must follow generated days",
  );
  assert.ok(
    nameTotalsIndex > dailyTotalValuesIndex,
    "name totals must follow page-independent rollups",
  );
  assert.ok(pagedNamesIndex > nameTotalsIndex, "pagination must follow names");
  assert.ok(cellValuesIndex > pagedNamesIndex, "cells must follow pagination");

  const eligibleBatches = sql.slice(
    eligibleBatchesIndex,
    filteredUsageIndex,
  );
  for (
    const column of [
      "event_id",
      "usage_date",
      "hour_start",
      "ingested_at",
      "usage_precision",
    ]
  ) {
    assert.ok(
      eligibleBatches.includes(`b.${column}`),
      `eligible_batches is missing column: ${column}`,
    );
  }
  assert.ok(
    eligibleBatches.includes("from public.log_upload_batches as b"),
  );
  assert.ok(
    eligibleBatches.includes("where b.is_test = _using_test_data"),
  );

  const filteredUsage = sql.slice(filteredUsageIndex, summaryValuesIndex);
  assert.ok(filteredUsage.includes("b.ingested_at"));
  assert.ok(
    filteredUsage.includes(
      "from public.log_upload_usage as u join eligible_batches as b on b.event_id = u.event_id",
    ),
  );
  for (
    const predicate of [
      "b.usage_date between p_from and p_to",
      "u.key_name <> 'unauthenticated'",
      "u.key_name !~* '^key-[0-9a-f]{12}$'",
      "pg_catalog.btrim(u.key_name) <> ''",
      "_search = ''",
      "pg_catalog.replace(_search, e'\\\\', e'\\\\\\\\')",
      "'%', e'\\\\%'",
      "'_', e'\\\\_'",
      "escape e'\\\\'",
    ]
  ) {
    assert.ok(
      filteredUsage.includes(predicate),
      `filtered_usage is missing predicate: ${predicate}`,
    );
  }

  const summaryValues = sql.slice(summaryValuesIndex, dayValuesIndex);
  const dailyTotalValues = sql.slice(dailyTotalValuesIndex, nameTotalsIndex);
  assert.ok(summaryValues.includes("from filtered_usage"));
  assert.ok(dailyTotalValues.includes("left join filtered_usage"));
  assert.doesNotMatch(summaryValues, /paged_names/);
  assert.doesNotMatch(dailyTotalValues, /paged_names/);
  assert.match(
    summaryValues,
    /pg_catalog\.count\(distinct filtered\.event_id\) as archive_count/,
  );
  assert.match(
    summaryValues,
    /pg_catalog\.count\(distinct filtered\.hour_start\) as archive_hour_count/,
  );
  assert.match(
    dailyTotalValues,
    /pg_catalog\.count\(distinct filtered\.event_id\) as archive_count/,
  );
  assert.match(
    dailyTotalValues,
    /pg_catalog\.count\(distinct filtered\.hour_start\) as archive_hour_count/,
  );
  assert.match(sql, /\(p_to - p_from \+ 1\)::integer as day_count/);
  assert.match(
    sql,
    /left join filtered_usage as filtered on filtered\.usage_date = days\.usage_date/,
  );

  for (
    const key of [
      "summary",
      "daily_totals",
      "source_bytes",
      "archive_count",
      "archive_hour_count",
      "active_key_count",
      "day_count",
      "source_count",
    ]
  ) {
    assert.ok(sql.includes(`'${key}'`), `missing response key: ${key}`);
  }
  assert.match(sql, /'source_bytes', summary\.source_bytes::text/);
  assert.match(sql, /'source_bytes', totals\.source_bytes::text/);
  assert.match(sql, /'source_count', cells\.source_count::text/);
  assert.match(
    sql,
    /coalesce\(\s*pg_catalog\.sum\(filtered\.source_count\),\s*0::numeric\s*\) as source_count/,
  );
  assert.match(
    sql,
    /pg_catalog\.count\(distinct filtered\.event_id\) as batch_count/,
  );
  const latestSyncAtIndex = sql.indexOf("'latest_sync_at'");
  assert.ok(latestSyncAtIndex > cellValuesIndex, "missing latest_sync_at");
  const nextFunctionIndex = sql.indexOf(
    "create or replace function public.get_public_hourly_usage",
    latestSyncAtIndex,
  );
  const latestSyncAt = sql.slice(
    latestSyncAtIndex,
    nextFunctionIndex < 0 ? undefined : nextFunctionIndex,
  );
  assert.ok(latestSyncAt.includes("from eligible_batches as batches"));
  assert.doesNotMatch(latestSyncAt, /public\.log_upload_batches/);

  assert.match(
    sql,
    /revoke all on function public\.get_public_daily_usage\(date, date, text, integer, integer\) from public, service_role/,
  );
  assert.match(
    sql,
    /revoke all on function public\.get_public_daily_usage\(date, date, text, integer, integer\) from anon, authenticated/,
  );
  const grants = sql.match(
    /grant execute on function public\.get_public_daily_usage\(date, date, text, integer, integer\) to [^;]+/g,
  ) ?? [];
  assert.deepEqual(grants, [
    "grant execute on function public.get_public_daily_usage(date, date, text, integer, integer) to anon, authenticated",
  ]);
});

Deno.test("hourly drill-down migration restricts exact-key archive metrics and ACL", async () => {
  const sql = compact(
    await read(
      "migrations/20260810010000_usage_dashboard_drilldown.sql",
    ),
  );
  const schemaSql = compact(
    await read("migrations/20260808000000_log_usage.sql"),
  );

  assert.match(
    sql,
    /create index log_upload_batches_mode_date_hour_event_idx on public\.log_upload_batches \(is_test, usage_date, hour_start, event_id\)/,
  );
  assert.match(
    sql,
    /create index log_upload_batches_mode_ingested_at_idx on public\.log_upload_batches \(is_test, ingested_at desc\)/,
  );
  assert.match(
    sql,
    /create index log_upload_batches_mode_hour_event_timezone_idx on public\.log_upload_batches \(is_test, hour_start desc, event_id desc\) include \(timezone\)/,
  );
  assert.match(
    sql,
    /create or replace function public\.get_public_hourly_usage\(\s*p_date date, p_key_name text\s*\)/,
  );
  assert.match(sql, /security definer set search_path = pg_catalog, public/);
  assert.match(
    sql,
    /grant execute on function public\.get_public_hourly_usage\(date, text\) to anon, authenticated/,
  );

  const hourlyBody = sql.slice(
    sql.indexOf("create or replace function public.get_public_hourly_usage"),
  );
  assert.match(hourlyBody, /eligible_batches as not materialized \(/);
  assert.match(
    hourlyBody,
    /if p_date is null then raise exception using errcode = '22023', message = 'validation_error: date is required'/,
  );
  assert.match(
    hourlyBody,
    /if p_key_name is null or pg_catalog\.btrim\(p_key_name\) = '' then raise exception using errcode = '22023', message = 'validation_error: key_name is required'/,
  );
  assert.match(
    hourlyBody,
    /if pg_catalog\.char_length\(pg_catalog\.btrim\(p_key_name\)\) > 48 then raise exception using errcode = '22023', message = 'validation_error: key_name must not exceed 48 characters'/,
  );
  assert.match(
    hourlyBody,
    /not exists \( select 1 from public\.log_upload_batches where is_test = false \)/,
  );
  assert.match(hourlyBody, /where b\.is_test = _using_test_data/);
  assert.match(
    hourlyBody,
    /where b\.is_test = _using_test_data order by b\.hour_start desc, b\.event_id desc limit 1/,
  );
  assert.match(hourlyBody, /_timezone := coalesce\(_timezone, 'utc'\)/);
  assert.match(hourlyBody, /b\.usage_date = p_date/);
  assert.match(hourlyBody, /u\.key_name = p_key_name/);
  assert.doesNotMatch(
    hourlyBody,
    /u\.key_name\s*=\s*(?:pg_catalog\.)?btrim\(p_key_name\)/,
  );
  for (
    const predicate of [
      "u.key_name <> 'unauthenticated'",
      "u.key_name !~* '^key-[0-9a-f]{12}$'",
      "pg_catalog.btrim(u.key_name) <> ''",
    ]
  ) {
    assert.ok(
      hourlyBody.includes(predicate),
      `hourly RPC is missing public-name filter: ${predicate}`,
    );
  }
  assert.doesNotMatch(hourlyBody, /generate_series/);
  assert.match(hourlyBody, /group by filtered\.hour_start/);
  assert.match(
    hourlyBody,
    /pg_catalog\.jsonb_agg\(hour_json order by hour_start\)/,
  );

  assert.match(
    hourlyBody,
    /coalesce\(\s*pg_catalog\.sum\(filtered\.source_count\),\s*0::numeric\s*\) as source_count/,
  );
  assert.match(
    hourlyBody,
    /coalesce\(\s*pg_catalog\.sum\(filtered\.source_bytes\),\s*0::numeric\s*\) as source_bytes/,
  );
  for (const provider of ["codex", "fable5", "grok45"]) {
    assert.match(
      hourlyBody,
      new RegExp(
        `pg_catalog\\.sum\\(filtered\\.source_bytes\\) filter \\(where filtered\\.provider = '${provider}'\\)`,
      ),
    );
  }
  assert.match(
    schemaSql,
    /provider in \('codex', 'fable5', 'grok45'\)/,
  );
  assert.match(
    hourlyBody,
    /pg_catalog\.count\(distinct filtered\.event_id\) as batch_count/,
  );
  assert.match(
    hourlyBody,
    /pg_catalog\.bool_and\( filtered\.usage_precision = 'exact' and filtered\.jsonl_bytes is not null \) as all_exact/,
  );
  assert.match(
    hourlyBody,
    /pg_catalog\.jsonb_build_object\( 'hour_start', pg_catalog\.to_char\(hours\.hour_start, 'yyyy-mm-dd"t"hh24:mi:sstzh:tzm'\), 'source_count', hours\.source_count::text, 'source_bytes', hours\.source_bytes::text, 'gpt_source_bytes', hours\.gpt_source_bytes::text, 'claude_source_bytes', hours\.claude_source_bytes::text, 'grok_source_bytes', hours\.grok_source_bytes::text, 'batch_count', hours\.batch_count, 'usage_precision', case when hours\.all_exact then 'exact' else 'batch_only' end \) as hour_json/,
  );
  assert.match(
    hourlyBody,
    /pg_catalog\.jsonb_build_object\( 'metric_basis', 'source_bytes', 'timezone', _timezone, 'date', p_date, 'key_name', p_key_name, 'latest_sync_at', \(select pg_catalog\.max\(batches\.ingested_at\) from eligible_batches as batches\), 'hours', coalesce\(\(select pg_catalog\.jsonb_agg\(hour_json order by hour_start\) from rendered_hours\), '\[\]'::jsonb\) \)/,
  );

  for (
    const forbidden of [
      "jsonl_bytes",
      "compressed_bytes",
      "object_key",
      "archive_sha256",
      "manifest_sha256",
    ]
  ) {
    assert.ok(
      !hourlyBody.includes(`'${forbidden}'`),
      `hourly RPC exposes ${forbidden}`,
    );
  }
  assert.match(
    hourlyBody,
    /revoke all on function public\.get_public_hourly_usage\(date, text\) from public, service_role/,
  );
  assert.match(
    hourlyBody,
    /revoke all on function public\.get_public_hourly_usage\(date, text\) from anon, authenticated/,
  );
  const grants = hourlyBody.match(
    /grant execute on function public\.get_public_hourly_usage\(date, text\) to [^;]+/g,
  ) ?? [];
  assert.deepEqual(grants, [
    "grant execute on function public.get_public_hourly_usage(date, text) to anon, authenticated",
  ]);
});

Deno.test("public daily response example has exact summaries and page data", async () => {
  const daily = JSON.parse(await read("examples/daily-response.json"));
  const cell = daily.cells[0];

  assert.deepEqual(Object.keys(daily).sort(), [
    "cells",
    "daily_totals",
    "days",
    "from",
    "latest_sync_at",
    "metric_basis",
    "names",
    "pagination",
    "summary",
    "timezone",
    "to",
    "using_test_data",
  ]);
  assert.equal(daily.metric_basis, "source_bytes");
  assert.ok(daily.summary, "missing daily summary");
  assert.ok(Array.isArray(daily.daily_totals), "missing daily totals");
  assert.equal(typeof daily.summary.source_bytes, "string");
  assert.equal(daily.daily_totals.length, daily.days.length);
  assert.deepEqual(
    daily.daily_totals.map((total: { date: string }) => total.date),
    daily.days,
  );
  assert.deepEqual(daily.days, [...daily.days].sort());
  assert.equal(
    daily.daily_totals.reduce(
      (sum: bigint, total: { source_bytes: string }) =>
        sum + BigInt(total.source_bytes),
      0n,
    ).toString(),
    daily.summary.source_bytes,
  );
  assert.equal(daily.summary.day_count, daily.days.length);
  for (const total of daily.daily_totals) {
    assert.deepEqual(Object.keys(total).sort(), [
      "active_key_count",
      "archive_count",
      "archive_hour_count",
      "date",
      "source_bytes",
    ]);
    assert.equal(typeof total.source_bytes, "string");
  }

  assert.deepEqual(Object.keys(cell).sort(), [
    "batch_count",
    "claude_bytes",
    "claude_source_bytes",
    "date",
    "gpt_bytes",
    "gpt_source_bytes",
    "grok_bytes",
    "grok_source_bytes",
    "jsonl_bytes",
    "key_name",
    "source_bytes",
    "source_count",
    "usage_precision",
  ]);
  assert.equal(cell.source_bytes, "7007199254740993");
  assert.equal(cell.jsonl_bytes, null);
  for (
    const field of [
      "source_bytes",
      "gpt_source_bytes",
      "claude_source_bytes",
      "grok_source_bytes",
    ]
  ) {
    assert.equal(typeof cell[field], "string");
  }
  assert.equal(typeof daily.cells[0].source_count, "string");
  assert.equal(
    (
      BigInt(cell.gpt_source_bytes) +
      BigInt(cell.claude_source_bytes) +
      BigInt(cell.grok_source_bytes)
    ).toString(),
    cell.source_bytes,
  );
  assert.equal(cell.usage_precision, "batch_only");
  assert.equal(typeof cell.batch_count, "number");
  assert.equal(typeof daily.pagination.page, "number");
  assert.equal(typeof daily.pagination.page_size, "number");
  assert.equal(typeof daily.pagination.total, "number");
});

Deno.test("public hourly response example is sparse and exposes source metrics only", async () => {
  let hourlySource: string | null = null;
  try {
    hourlySource = await read("examples/hourly-response.json");
  } catch (error) {
    if (!(error instanceof Deno.errors.NotFound)) {
      throw error;
    }
  }
  assert.ok(hourlySource, "missing hourly response example");
  const hourly = JSON.parse(hourlySource);

  assert.deepEqual(Object.keys(hourly).sort(), [
    "date",
    "hours",
    "key_name",
    "latest_sync_at",
    "metric_basis",
    "timezone",
  ]);
  assert.equal(hourly.metric_basis, "source_bytes");
  assert.ok(Array.isArray(hourly.hours));
  assert.ok(hourly.hours.length >= 2, "hourly example must show sparsity");

  const hourStarts = hourly.hours.map(
    (hour: { hour_start: string }) => hour.hour_start,
  );
  assert.deepEqual(hourStarts, [...hourStarts].sort());
  assert.ok(
    Date.parse(hourStarts[1]) - Date.parse(hourStarts[0]) > 60 * 60 * 1000,
    "hourly example must omit an archive-free hour",
  );

  for (const hour of hourly.hours) {
    assert.deepEqual(Object.keys(hour).sort(), [
      "batch_count",
      "claude_source_bytes",
      "gpt_source_bytes",
      "grok_source_bytes",
      "hour_start",
      "source_bytes",
      "source_count",
      "usage_precision",
    ]);
    assert.equal(typeof hour.source_count, "string");
    assert.equal(typeof hour.source_bytes, "string");
    for (
      const field of [
        "gpt_source_bytes",
        "claude_source_bytes",
        "grok_source_bytes",
      ]
    ) {
      assert.equal(typeof hour[field], "string");
    }
    assert.equal(
      (
        BigInt(hour.gpt_source_bytes) +
        BigInt(hour.claude_source_bytes) +
        BigInt(hour.grok_source_bytes)
      ).toString(),
      hour.source_bytes,
    );
    assert.ok(["exact", "batch_only"].includes(hour.usage_precision));
    assert.equal(typeof hour.batch_count, "number");
    for (
      const forbidden of [
        "jsonl_bytes",
        "compressed_bytes",
        "object_key",
        "archive_sha256",
        "manifest_sha256",
      ]
    ) {
      assert.ok(!(forbidden in hour), `hourly example exposes ${forbidden}`);
    }
  }
  assert.deepEqual(
    hourly.hours.map((hour: { usage_precision: string }) =>
      hour.usage_precision
    ),
    ["batch_only", "exact"],
  );

  const daily = JSON.parse(await read("examples/daily-response.json"));
  const dailyCell = daily.cells.find(
    (cell: { date: string; key_name: string }) =>
      cell.date === hourly.date && cell.key_name === hourly.key_name,
  );
  assert.ok(dailyCell, "hourly example must drill into the daily example");
  assert.equal(hourly.timezone, daily.timezone);
  assert.equal(hourly.latest_sync_at, daily.latest_sync_at);
  for (
    const field of [
      "source_count",
      "source_bytes",
      "gpt_source_bytes",
      "claude_source_bytes",
      "grok_source_bytes",
    ]
  ) {
    assert.equal(
      hourly.hours.reduce(
        (sum: bigint, hour: Record<string, string>) =>
          sum + BigInt(hour[field]),
        0n,
      ).toString(),
      dailyCell[field],
      `hourly ${field} must reconcile with the daily cell`,
    );
  }
  assert.equal(
    hourly.hours.reduce(
      (sum: number, hour: { batch_count: number }) => sum + hour.batch_count,
      0,
    ),
    dailyCell.batch_count,
  );
  assert.equal(
    hourly.hours.every(
        (hour: { usage_precision: string }) => hour.usage_precision === "exact",
      )
      ? "exact"
      : "batch_only",
    dailyCell.usage_precision,
  );

  assert.match(
    hourly.latest_sync_at,
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/,
  );
  assert.ok(!Number.isNaN(Date.parse(hourly.latest_sync_at)));
  assert.ok(
    hourly.latest_sync_at.endsWith("+00:00"),
    "latest_sync_at must show the UTC session serialization used by the example",
  );
  for (const hourStart of hourStarts) {
    assert.match(
      hourStart,
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/,
    );
    assert.ok(!Number.isNaN(Date.parse(hourStart)));
    assert.ok(
      hourStart.endsWith("+00:00"),
      "hour_start must show the UTC session serialization used by the example",
    );
    const dateParts = Object.fromEntries(
      new Intl.DateTimeFormat("en", {
        timeZone: hourly.timezone,
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      }).formatToParts(new Date(hourStart)).map((part) => [
        part.type,
        part.value,
      ]),
    );
    assert.equal(
      `${dateParts.year}-${dateParts.month}-${dateParts.day}`,
      hourly.date,
      "hour_start must belong to the requested date in the archive timezone",
    );
  }
});

Deno.test("README documents the public daily and hourly RPC contracts", async () => {
  const readme = compact(await read("README.md"));

  for (
    const phrase of [
      "get_public_daily_usage",
      "exact source-byte summary",
      "daily totals",
      "independent of the 20-name transport page",
      "get_public_hourly_usage",
      "exact key_name",
      "sparse archive hours",
      "not request-event hours",
      "does not fabricate per-key jsonl or compressed bytes",
      "underlying tables remain private with rls",
      "jwt verification is disabled for both edge functions",
    ]
  ) {
    assert.ok(readme.includes(phrase), `README is missing: ${phrase}`);
  }
  assert.match(
    readme,
    /get-content -raw -literalpath 'supabase\/tests\/log_usage_assertions\.sql' \| docker exec -i supabase_db_cliproxyapi psql -u postgres -d postgres -v on_error_stop=1/,
  );
  assert.ok(
    !readme.includes("because the host does not provide `psql`"),
    "README must not assume every host lacks psql",
  );
  assert.ok(readme.includes("when `psql` is unavailable on the host"));
  assert.ok(
    readme.includes(
      "postgresql://postgres:postgres@127.0.0.1:54322/postgres",
    ),
  );
});

Deno.test("seed is deterministic test data with a seven-day query range and a full missing event date", async () => {
  const seed = await read("seed.sql");

  assert.match(seed, /ingest_log_usage_v1/g);
  for (const name of ["张三", "李四", "王五"]) {
    assert.ok(seed.includes(name));
  }
  for (const provider of ["codex", "fable5", "grok45"]) {
    assert.ok(seed.includes(`'provider', '${provider}'`));
  }
  assert.ok(seed.includes("2026-08-01"));
  assert.ok(seed.includes("2026-08-07"));
  assert.doesNotMatch(
    seed,
    /\(\s*date '2026-08-04',\s*pg_catalog\.jsonb_build_array/,
  );
  assert.match(seed, /'jsonl_bytes', 0/);
  assert.match(seed, /'test_mode', true/);
  assert.doesNotMatch(seed, /'is_test'/);
  assert.match(seed, /full event-date gap/i);

  const nonzeroJSONLBytes = Array.from(
    seed.matchAll(/'jsonl_bytes',\s*([0-9]+)/g),
    (match) => Number(match[1]),
  ).filter((value) => value > 0);
  assert.ok(nonzeroJSONLBytes.length > 0);
  assert.ok(
    Math.max(...nonzeroJSONLBytes) / Math.min(...nonzeroJSONLBytes) >= 1_000,
    "seed jsonl_bytes must span at least three orders of magnitude",
  );
});

Deno.test("SQL assertion source declares behavior and privilege coverage", async () => {
  const assertions = compact(await read("tests/log_usage_assertions.sql"));

  assert.match(assertions, /begin;/);
  assert.match(assertions, /rollback;/);
  assert.match(assertions, /has_table_privilege/);
  for (const privilege of ["select", "insert", "update", "delete"]) {
    assert.match(assertions, new RegExp(`'${privilege}'`));
  }
  assert.match(assertions, /has_function_privilege/);
  assert.match(assertions, /event_id_conflict/);
  assert.match(assertions, /payload contains unsupported fields/);
  assert.match(assertions, /event_id must be a safe non-secret identifier/);
  assert.match(assertions, /target_id must be a safe non-secret identifier/);
  assert.match(assertions, /object_key must be a safe relative object key/);
  assert.match(assertions, /key_name must be a display label, not a secret/);
  assert.match(assertions, /2026-01-01t16:30:00z/);
  assert.match(assertions, /2026-01-02t02:30:00z/);
  assert.match(assertions, /2026-01-02/);
  assert.match(assertions, /2026-01-01/);
  assert.match(assertions, /using_test_data/);
  assert.match(assertions, /usage_precision/);
  assert.match(assertions, /batch_only/);
  assert.match(assertions, /source_bytes/);
  assert.match(assertions, /source_count/);
  assert.match(assertions, /gpt_source_bytes/);
  assert.match(assertions, /claude_source_bytes/);
  assert.match(assertions, /grok_source_bytes/);
  assert.match(assertions, /jsonl_bytes/);
  assert.match(assertions, /batch_count/);
  assert.match(assertions, /gpt_bytes/);
  assert.match(assertions, /claude_bytes/);
  assert.match(assertions, /grok_bytes/);
  assert.match(assertions, /2147483647/);
  assert.match(assertions, /9007199254740993/);
  assert.match(assertions, /literal search failed/);
  assert.match(assertions, /daily summary is incorrect/);
  assert.match(assertions, /daily totals are incorrect/);
  assert.match(assertions, /daily rollups changed across pages/);
  assert.match(assertions, /daily source_count lost exact precision/);
  assert.match(assertions, /filtered private fixture rows were modified/);
  assert.match(assertions, /pg_catalog\.pg_indexes/);
  assert.match(assertions, /log_upload_batches_mode_date_hour_event_idx/);
  assert.match(assertions, /log_upload_batches_mode_ingested_at_idx/);
  assert.match(assertions, /log_upload_batches_mode_hour_event_timezone_idx/);
  assert.match(assertions, /get_public_hourly_usage/);
  assert.match(
    assertions,
    /public unexpectedly has hourly dashboard rpc execute/,
  );
  assert.match(
    assertions,
    /service_role unexpectedly has hourly dashboard rpc execute/,
  );
  assert.match(assertions, /hourly rows are not sparse and ascending/);
  assert.match(assertions, /hourly batch-only provider totals are incorrect/);
  assert.match(assertions, /hourly exact provider totals are incorrect/);
  assert.match(assertions, /hourly provider bytes do not sum to source_bytes/);
  assert.match(assertions, /hidden direct key leaked hourly rows/);
  assert.match(assertions, /hourly exact key matching was rewritten/);
  assert.match(assertions, /hourly null date validation was not raised/);
  assert.match(assertions, /hourly null key validation was not raised/);
  assert.match(assertions, /hourly empty key validation was not raised/);
  assert.match(assertions, /hourly overlong key validation was not raised/);
  assert.match(assertions, /hourly live metadata is not current-mode global/);
  assert.match(assertions, /hourly test fallback metadata is incorrect/);
  assert.match(assertions, /live batches remain before hourly test fallback/);
  assert.match(assertions, /lost a public usage rpc grant/);
  for (
    const marker of [
      "private fallback rows were modified",
      "hidden-only public query leaked fallback names",
      "fallback names changed public aggregate",
      "fallback names changed public pagination",
      "fallback search leaked hidden names",
      "blank-only public query leaked blank names",
    ]
  ) {
    assert.ok(
      assertions.includes(marker),
      `missing fallback filter marker: ${marker}`,
    );
  }
});
