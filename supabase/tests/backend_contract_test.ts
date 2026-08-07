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
      "target text not null",
      "object_key text not null",
      "archive_sha256 text not null",
      "manifest_sha256 text not null",
      "hour timestamptz not null",
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

Deno.test("ingest SQL validates immutable content, timezone dates, sums, uniqueness, and bigint ranges", async () => {
  const sql = compact(await read("migrations/20260808000000_log_usage.sql"));

  assert.match(
    sql,
    /create or replace function public\.ingest_log_usage_v1\(payload jsonb, payload_sha256 text\)/,
  );
  assert.match(sql, /security definer set search_path = pg_catalog, public/);
  assert.match(sql, /schema_version/);
  assert.match(sql, /pg_catalog\.pg_timezone_names/);
  assert.match(sql, /usage_date must match hour in timezone/);
  assert.match(sql, /9223372036854775807/);
  assert.match(sql, /unique key_name and provider pairs/);
  assert.match(sql, /if _provider is null or _provider not in/);
  assert.match(sql, /batch totals must equal usage totals/);
  assert.match(sql, /on conflict \(event_id\) do nothing/);
  assert.match(sql, /event_id_conflict/);
  assert.match(sql, /jsonb_build_object\('status', 'duplicate'/);
  assert.match(sql, /jsonb_build_object\('status', 'inserted'/);
});

Deno.test("public daily SQL hides test rows after live data and maps providers to compact cells", async () => {
  const sql = compact(await read("migrations/20260808000000_log_usage.sql"));

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
      "from",
      "to",
      "using_test_data",
      "total_names",
      "names",
      "days",
      "cells",
      "last_synced_at",
      "gpt_bytes",
      "claude_bytes",
      "grok_bytes",
    ]
  ) {
    assert.ok(sql.includes(`'${key}'`), `missing response key: ${key}`);
  }
});

Deno.test("seed is deterministic test data with seven dates, three names, three providers, zeroes, and gaps", async () => {
  const seed = await read("seed.sql");

  assert.match(seed, /ingest_log_usage_v1/g);
  for (const name of ["张三", "李四", "王五"]) {
    assert.ok(seed.includes(name));
  }
  for (const provider of ["codex", "fable5", "grok45"]) {
    assert.ok(seed.includes(`'provider', '${provider}'`));
  }
  for (let day = 1; day <= 7; day += 1) {
    assert.ok(seed.includes(`2026-08-0${day}`));
  }
  assert.match(seed, /'jsonl_bytes', 0/);
  assert.match(seed, /'is_test', true/);
  assert.match(seed, /intentional gap/i);
});

Deno.test("runnable SQL assertions cover behavior and privileges", async () => {
  const assertions = compact(await read("tests/log_usage_assertions.sql"));

  assert.match(assertions, /begin;/);
  assert.match(assertions, /rollback;/);
  assert.match(assertions, /has_table_privilege/);
  assert.match(assertions, /has_function_privilege/);
  assert.match(assertions, /event_id_conflict/);
  assert.match(assertions, /using_test_data/);
  assert.match(assertions, /gpt_bytes/);
  assert.match(assertions, /claude_bytes/);
  assert.match(assertions, /grok_bytes/);
});
