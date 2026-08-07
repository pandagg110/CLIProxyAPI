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
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'codex', 'source_count', 2, 'source_bytes', 100, 'jsonl_bytes', 120),
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'grok45', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'fable5', 'source_count', 1, 'source_bytes', 80, 'jsonl_bytes', 95),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'grok45', 'source_count', 3, 'source_bytes', 200, 'jsonl_bytes', 240)
          )
        ),
        (
          date '2026-08-02',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'fable5', 'source_count', 2, 'source_bytes', 130, 'jsonl_bytes', 155),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'codex', 'source_count', 1, 'source_bytes', 70, 'jsonl_bytes', 84)
            -- Intentional gap: 王五 has no row on this date.
          )
        ),
        (
          date '2026-08-03',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'grok45', 'source_count', 1, 'source_bytes', 90, 'jsonl_bytes', 108),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'fable5', 'source_count', 2, 'source_bytes', 160, 'jsonl_bytes', 192),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'codex', 'source_count', 1, 'source_bytes', 75, 'jsonl_bytes', 90)
          )
        ),
        (
          date '2026-08-04',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'codex', 'source_count', 3, 'source_bytes', 210, 'jsonl_bytes', 252),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'fable5', 'source_count', 2, 'source_bytes', 140, 'jsonl_bytes', 168)
            -- Intentional gap: 李四 has no row on this date.
          )
        ),
        (
          date '2026-08-05',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'grok45', 'source_count', 1, 'source_bytes', 110, 'jsonl_bytes', 132),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'codex', 'source_count', 2, 'source_bytes', 150, 'jsonl_bytes', 180)
            -- Intentional gap: 张三 has no row on this date.
          )
        ),
        (
          date '2026-08-06',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'fable5', 'source_count', 1, 'source_bytes', 85, 'jsonl_bytes', 102),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'codex', 'source_count', 2, 'source_bytes', 145, 'jsonl_bytes', 174),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'grok45', 'source_count', 1, 'source_bytes', 95, 'jsonl_bytes', 114)
          )
        ),
        (
          date '2026-08-07',
          pg_catalog.jsonb_build_array(
            pg_catalog.jsonb_build_object('key_name', '张三', 'provider', 'codex', 'source_count', 2, 'source_bytes', 170, 'jsonl_bytes', 204),
            pg_catalog.jsonb_build_object('key_name', '李四', 'provider', 'fable5', 'source_count', 1, 'source_bytes', 100, 'jsonl_bytes', 120),
            pg_catalog.jsonb_build_object('key_name', '王五', 'provider', 'grok45', 'source_count', 2, 'source_bytes', 180, 'jsonl_bytes', 216)
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
      'target', 'synthetic-seed',
      'object_key', 'seed/' || pg_catalog.to_char(seed_date, 'YYYY/MM/DD') || '/usage.jsonl.zst',
      'archive_sha256', pg_catalog.repeat('a', 64),
      'manifest_sha256', pg_catalog.repeat('b', 64),
      'hour', pg_catalog.to_char(seed_date, 'YYYY-MM-DD') || 'T00:00:00+08:00',
      'timezone', 'Asia/Shanghai',
      'usage_date', pg_catalog.to_char(seed_date, 'YYYY-MM-DD'),
      'source_count', total_source_count,
      'source_bytes', total_source_bytes,
      'jsonl_bytes', total_jsonl_bytes,
      'compressed_bytes', total_jsonl_bytes / 3,
      'is_test', true,
      'usage', seed_usage
    );

    perform public.ingest_log_usage_v1(
      seed_payload,
      pg_catalog.lpad(pg_catalog.to_char(seed_date, 'YYYYMMDD'), 64, '0')
    );
  end loop;
end;
$$;
