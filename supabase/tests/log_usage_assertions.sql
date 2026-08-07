begin;

do $assert_privileges$
declare
  table_name text;
begin
  foreach table_name in array array['log_upload_batches', 'log_upload_usage']
  loop
    if not exists (
      select 1
      from pg_catalog.pg_class as classes
      join pg_catalog.pg_namespace as namespaces on namespaces.oid = classes.relnamespace
      where namespaces.nspname = 'public'
        and classes.relname = table_name
        and classes.relrowsecurity
    ) then
      raise exception 'RLS is not enabled on public.%', table_name;
    end if;
    if pg_catalog.has_table_privilege('anon', 'public.' || table_name, 'select')
      or pg_catalog.has_table_privilege('authenticated', 'public.' || table_name, 'select') then
      raise exception 'direct table access is available on public.%', table_name;
    end if;
  end loop;

  if pg_catalog.has_function_privilege(
    'anon',
    'public.ingest_log_usage_v1(jsonb,text)',
    'execute'
  ) or pg_catalog.has_function_privilege(
    'authenticated',
    'public.ingest_log_usage_v1(jsonb,text)',
    'execute'
  ) then
    raise exception 'ingest RPC is executable by a public role';
  end if;
  if not pg_catalog.has_function_privilege(
    'service_role',
    'public.ingest_log_usage_v1(jsonb,text)',
    'execute'
  ) then
    raise exception 'service_role cannot execute ingest RPC';
  end if;
  if not pg_catalog.has_function_privilege(
    'anon',
    'public.get_public_daily_usage(date,date,text,integer,integer)',
    'execute'
  ) or not pg_catalog.has_function_privilege(
    'authenticated',
    'public.get_public_daily_usage(date,date,text,integer,integer)',
    'execute'
  ) then
    raise exception 'public dashboard RPC is missing an intended grant';
  end if;
  if pg_catalog.has_function_privilege(
    'service_role',
    'public.get_public_daily_usage(date,date,text,integer,integer)',
    'execute'
  ) then
    raise exception 'service_role unexpectedly has public dashboard RPC execute';
  end if;
end;
$assert_privileges$;

do $assert_behavior$
declare
  test_payload jsonb;
  live_payload jsonb;
  result jsonb;
  daily jsonb;
  conflict_seen boolean := false;
  validation_seen boolean := false;
  provider_validation_seen boolean := false;
begin
  delete from public.log_upload_batches
  where event_id like 'sql-assert-%';

  test_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-test',
    'target', 'assertions',
    'object_key', 'assertions/test.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('a', 64),
    'manifest_sha256', pg_catalog.repeat('b', 64),
    'hour', '2026-01-01T00:00:00+08:00',
    'timezone', 'Asia/Shanghai',
    'usage_date', '2026-01-01',
    'source_count', 3,
    'source_bytes', 60,
    'jsonl_bytes', 600,
    'compressed_bytes', 200,
    'is_test', true,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-test', 'provider', 'codex', 'source_count', 1, 'source_bytes', 10, 'jsonl_bytes', 100),
      pg_catalog.jsonb_build_object('key_name', 'sql-test', 'provider', 'fable5', 'source_count', 1, 'source_bytes', 20, 'jsonl_bytes', 200),
      pg_catalog.jsonb_build_object('key_name', 'sql-test', 'provider', 'grok45', 'source_count', 1, 'source_bytes', 30, 'jsonl_bytes', 300)
    )
  );

  result := public.ingest_log_usage_v1(test_payload, pg_catalog.repeat('c', 64));
  if result ->> 'status' <> 'inserted' then
    raise exception 'expected inserted result, got %', result;
  end if;
  result := public.ingest_log_usage_v1(test_payload, pg_catalog.repeat('c', 64));
  if result ->> 'status' <> 'duplicate' then
    raise exception 'expected duplicate result, got %', result;
  end if;

  begin
    perform public.ingest_log_usage_v1(test_payload, pg_catalog.repeat('d', 64));
  exception
    when sqlstate 'P0001' then
      if sqlerrm <> 'event_id_conflict' then
        raise;
      end if;
      conflict_seen := true;
  end;
  if not conflict_seen then
    raise exception 'event_id_conflict was not raised';
  end if;

  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('source_count', 9223372036854775808),
      pg_catalog.repeat('e', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'bigint overflow validation was not raised';
  end if;

  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object(
        'event_id', 'sql-assert-missing-provider',
        'source_count', 1,
        'source_bytes', 10,
        'jsonl_bytes', 100,
        'usage', pg_catalog.jsonb_build_array(
          pg_catalog.jsonb_build_object(
            'key_name', 'sql-missing-provider',
            'source_count', 1,
            'source_bytes', 10,
            'jsonl_bytes', 100
          )
        )
      ),
      pg_catalog.repeat('f', 64)
    );
  exception
    when sqlstate '22023' then
      provider_validation_seen := true;
  end;
  if not provider_validation_seen then
    raise exception 'missing provider validation was not raised';
  end if;

  daily := public.get_public_daily_usage(
    date '2026-01-01',
    date '2026-01-01',
    '',
    1,
    20
  );
  if (daily ->> 'using_test_data')::boolean is distinct from true then
    raise exception 'expected using_test_data before live rows: %', daily;
  end if;
  if daily #>> '{cells,0,gpt_bytes}' <> '100'
    or daily #>> '{cells,0,claude_bytes}' <> '200'
    or daily #>> '{cells,0,grok_bytes}' <> '300' then
    raise exception 'provider mapping is incorrect: %', daily;
  end if;

  live_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-live',
    'target', 'assertions',
    'object_key', 'assertions/live.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('1', 64),
    'manifest_sha256', pg_catalog.repeat('2', 64),
    'hour', '2026-01-01T01:00:00+08:00',
    'timezone', 'Asia/Shanghai',
    'usage_date', '2026-01-01',
    'source_count', 1,
    'source_bytes', 40,
    'jsonl_bytes', 400,
    'compressed_bytes', 100,
    'is_test', false,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-live', 'provider', 'codex', 'source_count', 1, 'source_bytes', 40, 'jsonl_bytes', 400)
    )
  );
  perform public.ingest_log_usage_v1(live_payload, pg_catalog.repeat('3', 64));

  daily := public.get_public_daily_usage(
    date '2026-01-01',
    date '2026-01-01',
    '',
    1,
    20
  );
  if (daily ->> 'using_test_data')::boolean is distinct from false then
    raise exception 'live rows did not disable using_test_data: %', daily;
  end if;
  if daily -> 'names' ? 'sql-test' or not (daily -> 'names' ? 'sql-live') then
    raise exception 'test rows were not hidden after live ingestion: %', daily;
  end if;
end;
$assert_behavior$;

rollback;
