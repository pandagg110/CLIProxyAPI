-- Intended dashboard query range: 2026-08-01 through 2026-08-07.
-- Full event-date gap: 2026-08-04 intentionally has no event.
do $$
declare
  seed_date date;
  seed_usage jsonb;
  seed_payload jsonb;
  total_source_count bigint;
  total_source_bytes bigint;
  total_jsonl_bytes bigint;
begin
  for seed_date, seed_usage in
    select *
    from (
      values
        (
          date '2026-08-01',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'codex', 'source_count', 1, 'source_bytes', 1, 'jsonl_bytes', 1),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'fable5', 'source_count', 1, 'source_bytes', 80, 'jsonl_bytes', 100),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'grok45', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0)
          )
        ),
        (
          date '2026-08-02',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'fable5', 'source_count', 2, 'source_bytes', 800, 'jsonl_bytes', 1000),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'codex', 'source_count', 3, 'source_bytes', 8000, 'jsonl_bytes', 10000)
          )
        ),
        (
          date '2026-08-03',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'grok45', 'source_count', 4, 'source_bytes', 80000, 'jsonl_bytes', 100000),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'fable5', 'source_count', 5, 'source_bytes', 800000, 'jsonl_bytes', 1000000)
          )
        ),
        (
          date '2026-08-05',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'grok45', 'source_count', 6, 'source_bytes', 8000000, 'jsonl_bytes', 10000000),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'codex', 'source_count', 1, 'source_bytes', 400, 'jsonl_bytes', 500)
          )
        ),
        (
          date '2026-08-06',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'grok45', 'source_count', 2, 'source_bytes', 4000, 'jsonl_bytes', 5000),
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'codex', 'source_count', 3, 'source_bytes', 40000, 'jsonl_bytes', 50000)
          )
        ),
        (
          date '2026-08-07',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'fable5', 'source_count', 4, 'source_bytes', 400000, 'jsonl_bytes', 500000),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'codex', 'source_count', 5, 'source_bytes', 4000000, 'jsonl_bytes', 5000000)
          )
        )
    ) as synthetic(usage_date, usage)
  loop
    select
      coalesce(pg_catalog.sum((entry ->> 'source_count')::bigint), 0::numeric),
      coalesce(pg_catalog.sum((entry ->> 'source_bytes')::bigint), 0::numeric),
      coalesce(pg_catalog.sum((entry ->> 'jsonl_bytes')::bigint), 0::numeric)
    into total_source_count, total_source_bytes, total_jsonl_bytes
    from pg_catalog.jsonb_array_elements(seed_usage) as entries(entry);

    seed_payload := pg_catalog.jsonb_build_object(
      'schema_version', 1,
      'event_id', 'seed-' || pg_catalog.to_char(seed_date, 'YYYY-MM-DD'),
      'target_id', 'synthetic-seed',
      'object_key', 'seed/' || pg_catalog.to_char(seed_date, 'YYYY/MM/DD') || '/usage.jsonl.zst',
      'archive_sha256', pg_catalog.repeat('a', 64),
      'manifest_sha256', pg_catalog.repeat('b', 64),
      'hour_start', pg_catalog.to_char(seed_date, 'YYYY-MM-DD') || 'T00:00:00+08:00',
      'timezone', 'Asia/Shanghai',
      'usage_date', pg_catalog.to_char(seed_date, 'YYYY-MM-DD'),
      'source_count', total_source_count,
      'source_bytes', total_source_bytes,
      'jsonl_bytes', total_jsonl_bytes,
      'compressed_bytes', total_jsonl_bytes / 3,
      'test_mode', true,
      'usage', seed_usage
    );

    perform public.ingest_log_usage_v1(
      seed_payload,
      pg_catalog.lpad(pg_catalog.to_char(seed_date, 'YYYYMMDD'), 64, '0')
    );
  end loop;
end;
$$;
