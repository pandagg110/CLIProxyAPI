begin;

set local timezone = 'UTC';

do $assert_privileges$
declare
  role_name text;
  table_name text;
  privilege_name text;
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

    foreach role_name in array array['anon', 'authenticated']
    loop
      foreach privilege_name in array array['select', 'insert', 'update', 'delete']
      loop
        if pg_catalog.has_table_privilege(
          role_name,
          'public.' || table_name,
          privilege_name
        ) then
          raise exception '% has % on public.%', role_name, privilege_name, table_name;
        end if;
      end loop;
    end loop;
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
  if not pg_catalog.has_function_privilege(
    'anon',
    'public.get_public_hourly_usage(date,text)',
    'execute'
  ) or not pg_catalog.has_function_privilege(
    'authenticated',
    'public.get_public_hourly_usage(date,text)',
    'execute'
  ) then
    raise exception 'hourly dashboard RPC is missing an intended grant';
  end if;
  if pg_catalog.has_function_privilege(
    'service_role',
    'public.get_public_hourly_usage(date,text)',
    'execute'
  ) then
    raise exception 'service_role unexpectedly has hourly dashboard RPC execute';
  end if;
  if exists (
    select 1
    from pg_catalog.pg_proc as functions
    join pg_catalog.pg_namespace as namespaces on namespaces.oid = functions.pronamespace
    cross join lateral pg_catalog.aclexplode(
      coalesce(
        functions.proacl,
        pg_catalog.acldefault('f', functions.proowner)
      )
    ) as privileges
    where namespaces.nspname = 'public'
      and functions.proname = 'get_public_hourly_usage'
      and privileges.grantee = 0
      and privileges.privilege_type = 'EXECUTE'
  ) then
    raise exception 'PUBLIC unexpectedly has hourly dashboard RPC execute';
  end if;
end;
$assert_privileges$;

do $assert_hourly_index$
declare
  index_definition text;
  latest_index_definition text;
  timezone_index_definition text;
begin
  select indexes.indexdef
  into index_definition
  from pg_catalog.pg_indexes as indexes
  where indexes.schemaname = 'public'
    and indexes.tablename = 'log_upload_batches'
    and indexes.indexname = 'log_upload_batches_mode_date_hour_event_idx';

  if index_definition is null
    or index_definition !~ '\(is_test, usage_date, hour_start, event_id\)$' then
    raise exception 'hourly archive index columns are incorrect: %', index_definition;
  end if;

  select indexes.indexdef
  into latest_index_definition
  from pg_catalog.pg_indexes as indexes
  where indexes.schemaname = 'public'
    and indexes.tablename = 'log_upload_batches'
    and indexes.indexname = 'log_upload_batches_mode_ingested_at_idx';

  if latest_index_definition is null
    or latest_index_definition !~ '\(is_test, ingested_at DESC\)$' then
    raise exception 'latest sync index columns are incorrect: %', latest_index_definition;
  end if;

  select indexes.indexdef
  into timezone_index_definition
  from pg_catalog.pg_indexes as indexes
  where indexes.schemaname = 'public'
    and indexes.tablename = 'log_upload_batches'
    and indexes.indexname = 'log_upload_batches_mode_hour_event_timezone_idx';

  if timezone_index_definition is null
    or timezone_index_definition !~ '\(is_test, hour_start DESC, event_id DESC\) INCLUDE \(timezone\)$' then
    raise exception 'timezone lookup index columns are incorrect: %', timezone_index_definition;
  end if;
end;
$assert_hourly_index$;

do $assert_seed$
declare
  seed_daily jsonb;
  zero_cell jsonb;
  minimum_nonzero_jsonl numeric;
  maximum_nonzero_jsonl numeric;
begin
  if (
    select pg_catalog.count(*)
    from public.log_upload_batches
    where event_id like 'seed-%'
  ) <> 6 then
    raise exception 'expected six seed events with one full missing date';
  end if;
  if exists (
    select 1
    from public.log_upload_batches
    where event_id like 'seed-%'
      and is_test = false
  ) then
    raise exception 'seed event was not stored as test data';
  end if;
  if (
    select pg_catalog.array_agg(usage_date order by usage_date)
    from public.log_upload_batches
    where event_id like 'seed-%'
  ) <> array[
    date '2026-08-01',
    date '2026-08-02',
    date '2026-08-03',
    date '2026-08-05',
    date '2026-08-06',
    date '2026-08-07'
  ] then
    raise exception 'seed event dates are incorrect';
  end if;
  if (
    select pg_catalog.count(distinct usage_rows.provider)
    from public.log_upload_usage as usage_rows
    where usage_rows.event_id like 'seed-%'
  ) <> 3 then
    raise exception 'seed does not cover all providers';
  end if;

  select
    pg_catalog.min(usage_rows.jsonl_bytes),
    pg_catalog.max(usage_rows.jsonl_bytes)
  into minimum_nonzero_jsonl, maximum_nonzero_jsonl
  from public.log_upload_usage as usage_rows
  where usage_rows.event_id like 'seed-%'
    and usage_rows.jsonl_bytes > 0;
  if maximum_nonzero_jsonl / minimum_nonzero_jsonl < 1000 then
    raise exception 'seed JSONL bytes do not span three orders of magnitude';
  end if;

  seed_daily := public.get_public_daily_usage(
    date '2026-08-01',
    date '2026-08-07',
    '',
    1,
    20
  );
  if (seed_daily ->> 'using_test_data')::boolean is distinct from true
    or seed_daily ->> 'metric_basis' <> 'source_bytes'
    or seed_daily #>> '{pagination,total}' <> '3' then
    raise exception 'seed dashboard metadata is incorrect: %', seed_daily;
  end if;
  if seed_daily -> 'days' <> '["2026-08-01", "2026-08-02", "2026-08-03", "2026-08-04", "2026-08-05", "2026-08-06", "2026-08-07"]'::jsonb then
    raise exception 'seed dashboard days do not cover the full query range: %', seed_daily;
  end if;
  if (seed_daily -> 'cells') @> '[{"date":"2026-08-04"}]'::jsonb then
    raise exception 'the full seed event-date gap unexpectedly has a cell: %', seed_daily;
  end if;
  if not seed_daily -> 'names' ?& array['张三', '李四', '王五'] then
    raise exception 'seed dashboard names are incomplete: %', seed_daily;
  end if;

  select values.value
  into zero_cell
  from pg_catalog.jsonb_array_elements(seed_daily -> 'cells') as values(value)
  where values.value ->> 'date' = '2026-08-01'
    and values.value ->> 'key_name' = '王五';
  if zero_cell is null
    or zero_cell ->> 'source_bytes' <> '0'
    or zero_cell ->> 'gpt_source_bytes' <> '0'
    or zero_cell ->> 'claude_source_bytes' <> '0'
    or zero_cell ->> 'grok_source_bytes' <> '0'
    or zero_cell ->> 'usage_precision' <> 'exact'
    or zero_cell ->> 'jsonl_bytes' <> '0'
    or zero_cell ->> 'gpt_bytes' <> '0'
    or zero_cell ->> 'claude_bytes' <> '0'
    or zero_cell ->> 'grok_bytes' <> '0'
    or zero_cell ->> 'batch_count' <> '1' then
    raise exception 'explicit seed zero cell is incorrect: %', seed_daily;
  end if;
end;
$assert_seed$;

do $assert_public_name_filter$
declare
  hidden_only jsonb;
  daily jsonb;
  page_daily jsonb;
  search_daily jsonb;
  before_batch_count bigint;
  before_usage_count bigint;
  after_batch_count bigint;
  after_usage_count bigint;
  expected_name text;
  page_number integer;
begin
  perform public.ingest_log_usage_v1(
    pg_catalog.jsonb_build_object(
      'schema_version', 1,
      'event_id', 'sql-assert-hidden-only',
      'target_id', 'assertions',
      'object_key', 'assertions/hidden-only.jsonl.zst',
      'archive_sha256', pg_catalog.repeat('a', 64),
      'manifest_sha256', pg_catalog.repeat('b', 64),
      'hour_start', '2026-01-06T00:00:00Z',
      'timezone', 'UTC',
      'usage_date', '2026-01-06',
      'source_count', 3,
      'source_bytes', 2400,
      'jsonl_bytes', 2400,
      'compressed_bytes', 1000,
      'test_mode', true,
      'usage', pg_catalog.jsonb_build_array(
        pg_catalog.jsonb_build_object('key_name', 'unauthenticated', 'provider', 'codex', 'source_count', 1, 'source_bytes', 1000, 'jsonl_bytes', 1000),
        pg_catalog.jsonb_build_object('key_name', 'key-0123456789ab', 'provider', 'codex', 'source_count', 1, 'source_bytes', 800, 'jsonl_bytes', 800),
        pg_catalog.jsonb_build_object('key_name', 'KEY-ABCDEF012345', 'provider', 'codex', 'source_count', 1, 'source_bytes', 600, 'jsonl_bytes', 600)
      )
    ),
    pg_catalog.repeat('c', 64)
  );
  perform public.ingest_log_usage_v1(
    pg_catalog.jsonb_build_object(
      'schema_version', 1,
      'event_id', 'sql-assert-hidden-visible',
      'target_id', 'assertions',
      'object_key', 'assertions/hidden-visible.jsonl.zst',
      'archive_sha256', pg_catalog.repeat('d', 64),
      'manifest_sha256', pg_catalog.repeat('e', 64),
      'hour_start', '2026-01-07T00:00:00Z',
      'timezone', 'UTC',
      'usage_date', '2026-01-07',
      'source_count', 3,
      'source_bytes', 60,
      'jsonl_bytes', 60,
      'compressed_bytes', 30,
      'test_mode', true,
      'usage', pg_catalog.jsonb_build_array(
        pg_catalog.jsonb_build_object('key_name', 'key-operations', 'provider', 'codex', 'source_count', 1, 'source_bytes', 30, 'jsonl_bytes', 30),
        pg_catalog.jsonb_build_object('key_name', 'key-123', 'provider', 'codex', 'source_count', 1, 'source_bytes', 20, 'jsonl_bytes', 20),
        pg_catalog.jsonb_build_object('key_name', 'Unauthenticated Team', 'provider', 'codex', 'source_count', 1, 'source_bytes', 10, 'jsonl_bytes', 10)
      )
    ),
    pg_catalog.repeat('f', 64)
  );

  select pg_catalog.count(*) into before_batch_count
  from public.log_upload_batches
  where event_id like 'sql-assert-hidden-%';
  select pg_catalog.count(*) into before_usage_count
  from public.log_upload_usage
  where event_id like 'sql-assert-hidden-%';
  if before_batch_count <> 2 or before_usage_count <> 6 then
    raise exception 'private fallback rows were modified';
  end if;

  hidden_only := public.get_public_daily_usage(date '2026-01-06', date '2026-01-06', '', 1, 20);
  if hidden_only #>> '{pagination,total}' is distinct from '0'
    or hidden_only -> 'names' is distinct from '[]'::jsonb
    or hidden_only -> 'cells' is distinct from '[]'::jsonb then
    raise exception 'hidden-only public query leaked fallback names';
  end if;

  daily := public.get_public_daily_usage(date '2026-01-06', date '2026-01-07', '', 1, 20);
  if daily #>> '{pagination,total}' is distinct from '3'
    or daily -> 'names' is distinct from '["key-operations", "key-123", "Unauthenticated Team"]'::jsonb
    or pg_catalog.jsonb_array_length(daily -> 'cells') <> 3
    or exists (
      select 1
      from pg_catalog.jsonb_array_elements(daily -> 'cells') as cells(value)
      where cells.value ->> 'key_name' not in ('key-operations', 'key-123', 'Unauthenticated Team')
    ) then
    raise exception 'fallback names changed public aggregate';
  end if;

  for page_number in 1..4 loop
    page_daily := public.get_public_daily_usage(date '2026-01-06', date '2026-01-07', '', page_number, 1);
    expected_name := case page_number
      when 1 then 'key-operations'
      when 2 then 'key-123'
      when 3 then 'Unauthenticated Team'
      else null
    end;
    if page_daily #>> '{pagination,total}' is distinct from '3'
      or (expected_name is not null and (page_daily -> 'names' is distinct from pg_catalog.jsonb_build_array(expected_name) or pg_catalog.jsonb_array_length(page_daily -> 'cells') <> 1))
      or (expected_name is null and (page_daily -> 'names' is distinct from '[]'::jsonb or page_daily -> 'cells' is distinct from '[]'::jsonb)) then
      raise exception 'fallback names changed public pagination';
    end if;
  end loop;

  search_daily := public.get_public_daily_usage(date '2026-01-06', date '2026-01-07', 'key-', 1, 20);
  if search_daily #>> '{pagination,total}' is distinct from '2'
    or search_daily -> 'names' is distinct from '["key-operations", "key-123"]'::jsonb then
    raise exception 'fallback search leaked hidden names';
  end if;
  search_daily := public.get_public_daily_usage(date '2026-01-06', date '2026-01-07', 'unauthenticated', 1, 20);
  if search_daily #>> '{pagination,total}' is distinct from '1'
    or search_daily -> 'names' is distinct from '["Unauthenticated Team"]'::jsonb then
    raise exception 'fallback search leaked hidden names';
  end if;

  select pg_catalog.count(*) into after_batch_count
  from public.log_upload_batches
  where event_id like 'sql-assert-hidden-%';
  select pg_catalog.count(*) into after_usage_count
  from public.log_upload_usage
  where event_id like 'sql-assert-hidden-%';
  if after_batch_count <> before_batch_count or after_usage_count <> before_usage_count
    or after_batch_count <> 2 or after_usage_count <> 6 then
    raise exception 'private fallback rows were modified';
  end if;
end;
$assert_public_name_filter$;

do $assert_blank_public_names$
declare
  daily jsonb;
begin
  alter table public.log_upload_usage
    drop constraint log_upload_usage_key_name_not_blank;

  insert into public.log_upload_batches (
    event_id,
    target_id,
    object_key,
    archive_sha256,
    manifest_sha256,
    hour_start,
    timezone,
    usage_date,
    source_count,
    source_bytes,
    jsonl_bytes,
    compressed_bytes,
    payload_sha256,
    is_test,
    usage_precision
  ) values (
    'sql-assert-blank-only',
    'assertions',
    'assertions/blank-only.jsonl.zst',
    pg_catalog.repeat('a', 64),
    pg_catalog.repeat('b', 64),
    '2026-01-08T00:00:00Z',
    'UTC',
    date '2026-01-08',
    2,
    2,
    2,
    1,
    pg_catalog.repeat('c', 64),
    true,
    'exact'
  );

  insert into public.log_upload_usage (
    event_id,
    key_name,
    provider,
    source_count,
    source_bytes,
    jsonl_bytes
  ) values
    ('sql-assert-blank-only', '', 'codex', 1, 1, 1),
    ('sql-assert-blank-only', '   ', 'codex', 1, 1, 1);

  daily := public.get_public_daily_usage(
    date '2026-01-08',
    date '2026-01-08',
    '',
    1,
    20
  );
  if daily #>> '{pagination,total}' is distinct from '0'
    or daily -> 'names' is distinct from '[]'::jsonb
    or daily -> 'cells' is distinct from '[]'::jsonb then
    raise exception 'blank-only public query leaked blank names';
  end if;

  if (
    select pg_catalog.count(*)
    from public.log_upload_usage
    where event_id = 'sql-assert-blank-only'
      and pg_catalog.btrim(key_name) = ''
  ) <> 2 then
    raise exception 'blank public filtering removed private rows';
  end if;

  delete from public.log_upload_batches
  where event_id = 'sql-assert-blank-only';

  alter table public.log_upload_usage
    add constraint log_upload_usage_key_name_not_blank
    check (pg_catalog.btrim(key_name) <> '');
end;
$assert_blank_public_names$;

do $assert_behavior$
declare
  test_payload jsonb;
  history_payload jsonb;
  second_payload jsonb;
  boundary_payload jsonb;
  large_payload jsonb;
  live_payload jsonb;
  result jsonb;
  daily jsonb;
  max_page_daily jsonb;
  cell jsonb;
  rejected_value text := 'do-not-echo-this-secret';
  bad_object_key text;
  secret_key_name text;
  error_message text;
  bad_event_id text;
  bad_target_id text;
  search_term text;
  expected_search_name text;
  conflict_seen boolean := false;
  validation_seen boolean := false;
  rejection_seen boolean := false;
begin
  delete from public.log_upload_batches
  where event_id like 'sql-assert-%';

  test_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-test',
    'target_id', 'assertions',
    'object_key', 'assertions/test.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('a', 64),
    'manifest_sha256', pg_catalog.repeat('b', 64),
    'hour_start', '2026-01-01T00:00:00+08:00',
    'timezone', 'Asia/Shanghai',
    'usage_date', '2026-01-01',
    'source_count', 3,
    'source_bytes', 60,
    'jsonl_bytes', 600,
    'compressed_bytes', 200,
    'test_mode', true,
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
  if not exists (
    select 1
    from public.log_upload_batches
    where event_id = 'sql-assert-test'
      and usage_precision = 'exact'
  ) then
    raise exception 'missing usage_precision did not default to exact';
  end if;

  history_payload := test_payload || pg_catalog.jsonb_build_object(
    'event_id', 'sql-assert-history',
    'object_key', 'assertions/history.jsonl.zst',
    'hour_start', '2026-01-05T00:00:00Z',
    'timezone', 'UTC',
    'usage_date', '2026-01-05',
    'source_count', 3,
    'source_bytes', 60,
    'jsonl_bytes', 750,
    'compressed_bytes', 250,
    'usage_precision', 'batch_only',
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-history', 'provider', 'codex', 'source_count', 1, 'source_bytes', 10, 'jsonl_bytes', null),
      pg_catalog.jsonb_build_object('key_name', 'sql-history', 'provider', 'fable5', 'source_count', 2, 'source_bytes', 50)
    )
  );
  result := public.ingest_log_usage_v1(history_payload, pg_catalog.repeat('b', 64));
  if result ->> 'status' <> 'inserted'
    or not exists (
      select 1
      from public.log_upload_batches
      where event_id = 'sql-assert-history'
        and usage_precision = 'batch_only'
        and jsonl_bytes = 750
    )
    or exists (
      select 1
      from public.log_upload_usage
      where event_id = 'sql-assert-history'
        and jsonl_bytes is not null
    ) then
    raise exception 'batch_only history was not stored without per-name JSONL';
  end if;

  validation_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      history_payload || pg_catalog.jsonb_build_object(
        'event_id', 'sql-assert-history-jsonl',
        'usage', pg_catalog.jsonb_build_array(
          pg_catalog.jsonb_build_object('key_name', 'sql-history', 'provider', 'codex', 'source_count', 3, 'source_bytes', 60, 'jsonl_bytes', 750)
        )
      ),
      pg_catalog.repeat('d', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'batch_only usage accepted per-name JSONL';
  end if;

  validation_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object(
        'event_id', 'sql-assert-exact-missing-jsonl',
        'usage', pg_catalog.jsonb_build_array(
          pg_catalog.jsonb_build_object('key_name', 'sql-exact', 'provider', 'codex', 'source_count', 3, 'source_bytes', 60)
        )
      ),
      pg_catalog.repeat('d', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'exact usage accepted missing per-name JSONL';
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

  validation_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('jsonl_bytes', 601),
      pg_catalog.repeat('e', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'batch total validation was not raised';
  end if;

  validation_seen := false;
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
      validation_seen := sqlerrm = 'validation_error: provider must be codex, fable5, or grok45';
  end;
  if not validation_seen then
    raise exception 'missing provider validation was not raised';
  end if;

  rejection_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('raw_api_key', rejected_value),
      pg_catalog.repeat('1', 64)
    );
  exception
    when sqlstate '22023' then
      rejection_seen := true;
      error_message := sqlerrm;
  end;
  if not rejection_seen
    or error_message <> 'validation_error: payload contains unsupported fields'
    or pg_catalog.strpos(error_message, rejected_value) > 0 then
    raise exception 'unknown top-level field was not rejected safely';
  end if;

  rejection_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('is_test', true),
      pg_catalog.repeat('1', 64)
    );
  exception
    when sqlstate '22023' then
      rejection_seen := sqlerrm = 'validation_error: payload contains unsupported fields';
  end;
  if not rejection_seen then
    raise exception 'external is_test was not rejected';
  end if;

  foreach bad_event_id in array array[
    'sk-proj-abcdefghijklmnopqrstuvwxyz012345',
    'AIza' || pg_catalog.repeat('A', 35),
    'AKIA' || pg_catalog.repeat('A', 16),
    pg_catalog.repeat('A1b2', 10),
    pg_catalog.repeat('A1b2_', 10),
    'ghp_' || pg_catalog.repeat('A', 36),
    'xoxb-' || pg_catalog.repeat('A', 32),
    'sk_live_' || pg_catalog.repeat('A', 32),
    'Bearer-secret-material',
    'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_value',
    'https://example.test/events/123',
    E'C:\\logs\\event.jsonl',
    'event id with spaces',
    '{"level":"info","message":"raw log body"}'
  ]
  loop
    rejection_seen := false;
    error_message := null;
    begin
      perform public.ingest_log_usage_v1(
        test_payload || pg_catalog.jsonb_build_object('event_id', bad_event_id),
        pg_catalog.repeat('1', 64)
      );
    exception
      when sqlstate '22023' then
        rejection_seen := true;
        error_message := sqlerrm;
    end;
    if not rejection_seen
      or error_message <> 'validation_error: event_id must be a safe non-secret identifier'
      or pg_catalog.strpos(error_message, bad_event_id) > 0 then
      raise exception 'unsafe event identifier was not rejected safely';
    end if;
  end loop;

  foreach bad_target_id in array array[
    'AIza' || pg_catalog.repeat('A', 35),
    'ASIA' || pg_catalog.repeat('A', 16),
    pg_catalog.repeat('Z9y8', 10),
    pg_catalog.repeat('Z9y8-', 10),
    'github_pat_' || pg_catalog.repeat('A', 32),
    'xapp-' || pg_catalog.repeat('A', 32),
    'rk_test_' || pg_catalog.repeat('A', 32),
    'https://bucket.example/logs?X-Tos-Signature=secret',
    E'C:\\logs\\target',
    E'\\\\server\\share\\target',
    'sk-proj-abcdefghijklmnopqrstuvwxyz012345'
  ]
  loop
    rejection_seen := false;
    error_message := null;
    begin
      perform public.ingest_log_usage_v1(
        test_payload || pg_catalog.jsonb_build_object(
          'event_id', 'sql-assert-bad-target',
          'target_id', bad_target_id
        ),
        pg_catalog.repeat('1', 64)
      );
    exception
      when sqlstate '22023' then
        rejection_seen := true;
        error_message := sqlerrm;
    end;
    if not rejection_seen
      or error_message <> 'validation_error: target_id must be a safe non-secret identifier'
      or pg_catalog.strpos(error_message, bad_target_id) > 0 then
      raise exception 'unsafe target identifier was not rejected safely';
    end if;
  end loop;

  rejection_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      pg_catalog.jsonb_set(
        test_payload,
        '{usage,0}',
        (test_payload #> '{usage,0}') || pg_catalog.jsonb_build_object('access_token', rejected_value)
      ),
      pg_catalog.repeat('1', 64)
    );
  exception
    when sqlstate '22023' then
      rejection_seen := true;
      error_message := sqlerrm;
  end;
  if not rejection_seen
    or error_message <> 'validation_error: usage entries contain unsupported fields'
    or pg_catalog.strpos(error_message, rejected_value) > 0 then
    raise exception 'unknown usage field was not rejected safely';
  end if;

  foreach bad_object_key in array array[
    'https://bucket.example/private.jsonl.zst?signature=secret',
    's3://bucket/private.jsonl.zst',
    'file:///tmp/private.jsonl.zst',
    '/var/log/private.jsonl.zst',
    E'C:\\logs\\private.jsonl.zst',
    E'\\\\server\\share\\private.jsonl.zst',
    'logs/private.jsonl.zst?signature=secret',
    'logs/private.jsonl.zst#fragment',
    'logs/../private.jsonl.zst',
    '../private.jsonl.zst'
  ]
  loop
    rejection_seen := false;
    error_message := null;
    begin
      perform public.ingest_log_usage_v1(
        test_payload || pg_catalog.jsonb_build_object('object_key', bad_object_key),
        pg_catalog.repeat('2', 64)
      );
    exception
      when sqlstate '22023' then
        rejection_seen := true;
        error_message := sqlerrm;
    end;
    if not rejection_seen
      or error_message <> 'validation_error: object_key must be a safe relative object key'
      or pg_catalog.strpos(error_message, bad_object_key) > 0 then
      raise exception 'unsafe object key was not rejected safely';
    end if;
  end loop;

  foreach secret_key_name in array array[
    'sk-proj-abcdefghijklmnopqrstuvwxyz012345',
    'Bearer abcdefghijklmnopqrstuvwxyz012345',
    'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_value',
    'A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0'
  ]
  loop
    rejection_seen := false;
    error_message := null;
    begin
      perform public.ingest_log_usage_v1(
        pg_catalog.jsonb_set(
          test_payload,
          '{usage,0,key_name}',
          pg_catalog.to_jsonb(secret_key_name)
        ),
        pg_catalog.repeat('3', 64)
      );
    exception
      when sqlstate '22023' then
        rejection_seen := true;
        error_message := sqlerrm;
    end;
    if not rejection_seen
      or error_message <> 'validation_error: key_name must be a display label, not a secret'
      or pg_catalog.strpos(error_message, secret_key_name) > 0 then
      raise exception 'secret-like key name was not rejected safely';
    end if;
  end loop;

  perform public.ingest_log_usage_v1(
    test_payload || pg_catalog.jsonb_build_object(
      'event_id', 'sql-assert-name-48',
      'object_key', 'assertions/name-48.jsonl.zst',
      'hour_start', '2026-01-06T00:00:00Z',
      'timezone', 'UTC',
      'usage_date', '2026-01-06',
      'source_count', 1,
      'source_bytes', 1,
      'jsonl_bytes', 1,
      'usage', pg_catalog.jsonb_build_array(
        pg_catalog.jsonb_build_object(
          'key_name', pg_catalog.repeat('😀', 48),
          'provider', 'codex',
          'source_count', 1,
          'source_bytes', 1,
          'jsonl_bytes', 1
        )
      )
    ),
    pg_catalog.repeat('3', 64)
  );

  foreach secret_key_name in array array[
    pg_catalog.repeat('😀', 49),
    '  CpA_private-name  '
  ]
  loop
    rejection_seen := false;
    error_message := null;
    begin
      perform public.ingest_log_usage_v1(
        pg_catalog.jsonb_set(
          test_payload || pg_catalog.jsonb_build_object('event_id', 'sql-assert-rejected-name'),
          '{usage,0,key_name}',
          pg_catalog.to_jsonb(secret_key_name)
        ),
        pg_catalog.repeat('3', 64)
      );
    exception
      when sqlstate '22023' then
        rejection_seen := true;
        error_message := sqlerrm;
    end;
    if not rejection_seen or pg_catalog.strpos(error_message, secret_key_name) > 0 then
      raise exception 'restricted key name was not rejected safely';
    end if;
  end loop;

  validation_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('event_id', 123),
      pg_catalog.repeat('4', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'non-string event_id was accepted';
  end if;

  validation_seen := false;
  begin
    perform public.ingest_log_usage_v1(
      test_payload || pg_catalog.jsonb_build_object('hour_start', '2026-01-01T00:00:00'),
      pg_catalog.repeat('4', 64)
    );
  exception
    when sqlstate '22023' then
      validation_seen := true;
  end;
  if not validation_seen then
    raise exception 'offset-less hour_start was accepted';
  end if;

  second_payload := test_payload || pg_catalog.jsonb_build_object(
    'event_id', 'sql-assert-test-2',
    'source_count', 1,
    'source_bytes', 5,
    'jsonl_bytes', 50,
    'compressed_bytes', 20,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-test', 'provider', 'codex', 'source_count', 1, 'source_bytes', 5, 'jsonl_bytes', 50)
    )
  );
  perform public.ingest_log_usage_v1(second_payload, pg_catalog.repeat('5', 64));

  boundary_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-shanghai-boundary',
    'target_id', 'assertions',
    'object_key', 'assertions/shanghai-boundary.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('a', 64),
    'manifest_sha256', pg_catalog.repeat('b', 64),
    'hour_start', '2026-01-01T16:30:00Z',
    'timezone', 'Asia/Shanghai',
    'usage_date', '2026-01-02',
    'source_count', 1,
    'source_bytes', 10,
    'jsonl_bytes', 10,
    'compressed_bytes', 5,
    'test_mode', true,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-shanghai-boundary', 'provider', 'codex', 'source_count', 1, 'source_bytes', 10, 'jsonl_bytes', 10)
    )
  );
  perform public.ingest_log_usage_v1(boundary_payload, pg_catalog.repeat('6', 64));

  boundary_payload := boundary_payload || pg_catalog.jsonb_build_object(
    'event_id', 'sql-assert-los-angeles-boundary',
    'object_key', 'assertions/los-angeles-boundary.jsonl.zst',
    'hour_start', '2026-01-02T02:30:00Z',
    'timezone', 'America/Los_Angeles',
    'usage_date', '2026-01-01',
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-los-angeles-boundary', 'provider', 'grok45', 'source_count', 1, 'source_bytes', 10, 'jsonl_bytes', 10)
    )
  );
  perform public.ingest_log_usage_v1(boundary_payload, pg_catalog.repeat('7', 64));

  daily := public.get_public_daily_usage(
    date '2026-01-01',
    date '2026-01-02',
    '',
    1,
    20
  );
  if (daily ->> 'using_test_data')::boolean is distinct from true then
    raise exception 'expected using_test_data before live rows: %', daily;
  end if;
  if not daily ?& array[
    'metric_basis',
    'timezone',
    'from',
    'to',
    'using_test_data',
    'pagination',
    'names',
    'days',
    'cells',
    'summary',
    'daily_totals',
    'latest_sync_at'
  ] or (select pg_catalog.count(*) from pg_catalog.jsonb_object_keys(daily)) <> 12 then
    raise exception 'public response fields are incorrect: %', daily;
  end if;
  if daily ? 'total_names' or daily ? 'last_synced_at' then
    raise exception 'legacy response fields are still exposed: %', daily;
  end if;
  if daily -> 'pagination' <> '{"page": 1, "page_size": 20, "total": 3}'::jsonb then
    raise exception 'pagination metadata is incorrect: %', daily;
  end if;
  max_page_daily := public.get_public_daily_usage(
    date '2026-01-01',
    date '2026-01-02',
    '',
    2147483647,
    20
  );
  if max_page_daily #>> '{pagination,page}' is distinct from '2147483647'
    or max_page_daily -> 'names' is distinct from '[]'::jsonb
    or max_page_daily -> 'cells' is distinct from '[]'::jsonb then
    raise exception 'maximum page overflowed or returned data: %', max_page_daily;
  end if;
  if daily -> 'days' <> '["2026-01-01", "2026-01-02"]'::jsonb then
    raise exception 'dashboard days do not include the full range: %', daily;
  end if;
  if not (daily -> 'cells') @> '[{"date":"2026-01-02","key_name":"sql-shanghai-boundary"}]'::jsonb
    or not (daily -> 'cells') @> '[{"date":"2026-01-01","key_name":"sql-los-angeles-boundary"}]'::jsonb then
    raise exception 'timezone boundary cell dates are incorrect: %', daily;
  end if;

  select values.value
  into cell
  from pg_catalog.jsonb_array_elements(daily -> 'cells') as values(value)
  where values.value ->> 'key_name' = 'sql-test'
    and values.value ->> 'date' = '2026-01-01';
  if cell is null
    or cell -> 'source_bytes' is distinct from '"65"'::jsonb
    or cell -> 'gpt_source_bytes' is distinct from '"15"'::jsonb
    or cell -> 'claude_source_bytes' is distinct from '"20"'::jsonb
    or cell -> 'grok_source_bytes' is distinct from '"30"'::jsonb
    or cell -> 'source_count' is distinct from '"4"'::jsonb
    or cell -> 'usage_precision' is distinct from '"exact"'::jsonb
    or cell -> 'jsonl_bytes' is distinct from '"650"'::jsonb
    or cell -> 'gpt_bytes' is distinct from '"150"'::jsonb
    or cell -> 'claude_bytes' is distinct from '"200"'::jsonb
    or cell -> 'grok_bytes' is distinct from '"300"'::jsonb
    or cell -> 'batch_count' is distinct from '2'::jsonb then
    raise exception 'provider totals or distinct batch count are incorrect: %', daily;
  end if;
  if cell ? 'name' or not cell ?& array[
    'date',
    'key_name',
    'source_bytes',
    'gpt_source_bytes',
    'claude_source_bytes',
    'grok_source_bytes',
    'source_count',
    'usage_precision',
    'jsonl_bytes',
    'gpt_bytes',
    'claude_bytes',
    'grok_bytes',
    'batch_count'
  ] or (select pg_catalog.count(*) from pg_catalog.jsonb_object_keys(cell)) <> 13 then
    raise exception 'public cell fields are incorrect: %', cell;
  end if;

  daily := public.get_public_daily_usage(
    date '2026-01-05',
    date '2026-01-05',
    '',
    1,
    20
  );
  select values.value
  into cell
  from pg_catalog.jsonb_array_elements(daily -> 'cells') as values(value)
  where values.value ->> 'key_name' = 'sql-history';
  if cell is null
    or cell -> 'source_bytes' is distinct from '"60"'::jsonb
    or cell -> 'gpt_source_bytes' is distinct from '"10"'::jsonb
    or cell -> 'claude_source_bytes' is distinct from '"50"'::jsonb
    or cell -> 'grok_source_bytes' is distinct from '"0"'::jsonb
    or cell -> 'usage_precision' is distinct from '"batch_only"'::jsonb
    or cell -> 'jsonl_bytes' is distinct from 'null'::jsonb
    or cell -> 'gpt_bytes' is distinct from 'null'::jsonb
    or cell -> 'claude_bytes' is distinct from 'null'::jsonb
    or cell -> 'grok_bytes' is distinct from 'null'::jsonb then
    raise exception 'batch_only public cell fabricated per-name JSONL: %', daily;
  end if;

  large_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-large-1',
    'target_id', 'assertions',
    'object_key', 'assertions/large-1.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('a', 64),
    'manifest_sha256', pg_catalog.repeat('b', 64),
    'hour_start', '2026-01-03T00:00:00Z',
    'timezone', 'UTC',
    'usage_date', '2026-01-03',
    'source_count', 0,
    'source_bytes', 0,
    'jsonl_bytes', 4503599627370497,
    'compressed_bytes', 0,
    'test_mode', true,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-large-total', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 4503599627370497)
    )
  );
  perform public.ingest_log_usage_v1(large_payload, pg_catalog.repeat('8', 64));
  large_payload := large_payload || pg_catalog.jsonb_build_object(
    'event_id', 'sql-assert-large-2',
    'object_key', 'assertions/large-2.jsonl.zst',
    'jsonl_bytes', 4503599627370496,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-large-total', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 4503599627370496)
    )
  );
  perform public.ingest_log_usage_v1(large_payload, pg_catalog.repeat('9', 64));

  daily := public.get_public_daily_usage(
    date '2026-01-03',
    date '2026-01-03',
    '',
    1,
    20
  );
  select values.value
  into cell
  from pg_catalog.jsonb_array_elements(daily -> 'cells') as values(value)
  where values.value ->> 'key_name' = 'sql-large-total';
  if cell -> 'jsonl_bytes' is distinct from '"9007199254740993"'::jsonb
    or cell -> 'gpt_bytes' is distinct from '"9007199254740993"'::jsonb
    or cell -> 'claude_bytes' is distinct from '"0"'::jsonb
    or cell -> 'grok_bytes' is distinct from '"0"'::jsonb
    or cell -> 'batch_count' is distinct from '2'::jsonb then
    raise exception 'public aggregates lost exact decimal precision: %', daily;
  end if;

  perform public.ingest_log_usage_v1(
    test_payload || pg_catalog.jsonb_build_object(
      'event_id', 'sql-assert-search',
      'object_key', 'assertions/search.jsonl.zst',
      'hour_start', '2026-01-04T00:00:00Z',
      'timezone', 'UTC',
      'usage_date', '2026-01-04',
      'source_count', 0,
      'source_bytes', 0,
      'jsonl_bytes', 0,
      'compressed_bytes', 0,
      'usage', pg_catalog.jsonb_build_array(
        pg_catalog.jsonb_build_object('key_name', 'sql-search-percent%', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0),
        pg_catalog.jsonb_build_object('key_name', 'sql-search-underscore_', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0),
        pg_catalog.jsonb_build_object('key_name', E'sql-search-backslash\\', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0),
        pg_catalog.jsonb_build_object('key_name', 'sql-search-plain', 'provider', 'codex', 'source_count', 0, 'source_bytes', 0, 'jsonl_bytes', 0)
      )
    ),
    pg_catalog.repeat('0', 64)
  );

  for search_term, expected_search_name in
    select cases.search_term, cases.expected_name
    from (values
      ('%', 'sql-search-percent%'),
      ('_', 'sql-search-underscore_'),
      (E'\\', E'sql-search-backslash\\')
    ) as cases(search_term, expected_name)
  loop
    daily := public.get_public_daily_usage(
      date '2026-01-04',
      date '2026-01-04',
      search_term,
      1,
      20
    );
    if daily #>> '{pagination,total}' is distinct from '1'
      or daily -> 'names' is distinct from pg_catalog.jsonb_build_array(expected_search_name) then
      raise exception 'literal search failed for %: %', search_term, daily;
    end if;
  end loop;

  live_payload := pg_catalog.jsonb_build_object(
    'schema_version', 1,
    'event_id', 'sql-assert-live',
    'target_id', 'assertions',
    'object_key', 'assertions/live.jsonl.zst',
    'archive_sha256', pg_catalog.repeat('1', 64),
    'manifest_sha256', pg_catalog.repeat('2', 64),
    'hour_start', '2026-01-01T01:00:00+08:00',
    'timezone', 'Asia/Shanghai',
    'usage_date', '2026-01-01',
    'source_count', 1,
    'source_bytes', 40,
    'jsonl_bytes', 400,
    'compressed_bytes', 100,
    'usage', pg_catalog.jsonb_build_array(
      pg_catalog.jsonb_build_object('key_name', 'sql-live', 'provider', 'codex', 'source_count', 1, 'source_bytes', 40, 'jsonl_bytes', 400)
    )
  );
  perform public.ingest_log_usage_v1(live_payload, pg_catalog.repeat('a', 64));

  if not exists (
    select 1
    from public.log_upload_batches
    where event_id = 'sql-assert-live'
      and is_test = false
  ) then
    raise exception 'omitted test_mode did not default to is_test=false';
  end if;

  daily := public.get_public_daily_usage(
    date '2026-01-01',
    date '2026-01-02',
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
  if daily #>> '{pagination,total}' <> '1'
    or not (daily -> 'cells') @> '[{"date":"2026-01-01","key_name":"sql-live","source_bytes":"40","source_count":"1","usage_precision":"exact","jsonl_bytes":"400","batch_count":1}]'::jsonb then
    raise exception 'live public response is incorrect: %', daily;
  end if;
end;
$assert_behavior$;

do $assert_daily_summary$
declare
  fixture record;
  page_one jsonb;
  page_two jsonb;
  search_daily jsonb;
  alpha_cell jsonb;
  large_alpha_cell jsonb;
  hourly jsonb;
  missing_hourly jsonb;
  hour_value jsonb;
  summed_daily_source_bytes numeric;
  forbidden_field text;
  validation_seen boolean;
begin
  for fixture in
    select values.*
    from (values
      ('sql-daily-alpha-1', '2026-02-10T00:00:00Z', date '2026-02-10', 'alpha', 'codex', 3::bigint, 100::bigint, false, 'exact'),
      ('sql-daily-alpha-2', '2026-02-10T00:00:00Z', date '2026-02-10', 'alpha', 'fable5', 2::bigint, 200::bigint, false, 'exact'),
      ('sql-daily-alpha-3', '2026-02-10T00:00:00Z', date '2026-02-10', 'alpha', 'grok45', 4::bigint, 400::bigint, false, 'batch_only'),
      ('sql-daily-alpha-4', '2026-02-10T03:00:00Z', date '2026-02-10', 'alpha', 'codex', 1::bigint, 30::bigint, false, 'exact'),
      ('sql-daily-beta-1', '2026-02-10T02:00:00Z', date '2026-02-10', 'beta', 'codex', 4::bigint, 220::bigint, false, 'exact'),
      ('sql-daily-alpha-5', '2026-02-12T00:00:00Z', date '2026-02-12', 'alpha', 'codex', 9007199254740993::bigint, 600::bigint, false, 'exact'),
      ('sql-daily-hidden-live', '2026-02-10T03:00:00Z', date '2026-02-10', 'unauthenticated', 'codex', 6::bigint, 7000::bigint, false, 'exact'),
      ('sql-daily-hidden-test', '2026-02-10T04:00:00Z', date '2026-02-10', 'alpha', 'codex', 7::bigint, 9000::bigint, true, 'exact')
    ) as values(event_id, hour_start, usage_date, key_name, provider, source_count, source_bytes, is_test, usage_precision)
  loop
    perform public.ingest_log_usage_v1(
      pg_catalog.jsonb_build_object(
        'schema_version', 1,
        'event_id', fixture.event_id,
        'target_id', 'assertions',
        'object_key', 'assertions/' || fixture.event_id || '.jsonl.zst',
        'archive_sha256', pg_catalog.repeat('a', 64),
        'manifest_sha256', pg_catalog.repeat('b', 64),
        'hour_start', fixture.hour_start,
        'timezone', 'UTC',
        'usage_date', fixture.usage_date,
        'source_count', fixture.source_count,
        'source_bytes', fixture.source_bytes,
        'jsonl_bytes', fixture.source_bytes * 10,
        'compressed_bytes', fixture.source_bytes,
        'usage_precision', fixture.usage_precision,
        'test_mode', fixture.is_test,
        'usage', pg_catalog.jsonb_build_array(
          pg_catalog.jsonb_build_object(
            'key_name', fixture.key_name,
            'provider', fixture.provider,
            'source_count', fixture.source_count,
            'source_bytes', fixture.source_bytes,
            'jsonl_bytes', case
              when fixture.usage_precision = 'exact' then fixture.source_bytes * 10
              else null
            end
          )
        )
      ),
      pg_catalog.repeat('c', 64)
    );
  end loop;

  if (
    select pg_catalog.count(*)
    from public.log_upload_batches
    where event_id like 'sql-daily-%'
  ) <> 8 or (
    select pg_catalog.count(*)
    from public.log_upload_usage
    where event_id like 'sql-daily-%'
  ) <> 8 or not exists (
    select 1
    from public.log_upload_batches
    where event_id = 'sql-daily-hidden-live'
      and is_test = false
  ) or not exists (
    select 1
    from public.log_upload_batches
    where event_id = 'sql-daily-hidden-test'
      and is_test = true
  ) then
    raise exception 'filtered private fixture rows were modified';
  end if;

  page_one := public.get_public_daily_usage(
    date '2026-02-10',
    date '2026-02-12',
    '',
    1,
    1
  );
  page_two := public.get_public_daily_usage(
    date '2026-02-10',
    date '2026-02-12',
    '',
    2,
    1
  );

  if page_one -> 'summary' is distinct from '{"source_bytes":"1550","archive_count":6,"archive_hour_count":4,"active_key_count":2,"day_count":3}'::jsonb then
    raise exception 'daily summary is incorrect: %', page_one;
  end if;
  if page_one -> 'daily_totals' is distinct from '[{"date":"2026-02-10","source_bytes":"950","archive_count":5,"archive_hour_count":3,"active_key_count":2},{"date":"2026-02-11","source_bytes":"0","archive_count":0,"archive_hour_count":0,"active_key_count":0},{"date":"2026-02-12","source_bytes":"600","archive_count":1,"archive_hour_count":1,"active_key_count":1}]'::jsonb then
    raise exception 'daily totals are incorrect: %', page_one;
  end if;
  if page_two -> 'summary' is distinct from page_one -> 'summary'
    or page_two -> 'daily_totals' is distinct from page_one -> 'daily_totals' then
    raise exception 'daily rollups changed across pages: page one %, page two %', page_one, page_two;
  end if;

  select values.value
  into alpha_cell
  from pg_catalog.jsonb_array_elements(page_one -> 'cells') as values(value)
  where values.value ->> 'date' = '2026-02-10'
    and values.value ->> 'key_name' = 'alpha';
  if alpha_cell -> 'source_count' is distinct from '"10"'::jsonb then
    raise exception 'daily alpha source_count is incorrect: %', page_one;
  end if;

  select values.value
  into large_alpha_cell
  from pg_catalog.jsonb_array_elements(page_one -> 'cells') as values(value)
  where values.value ->> 'date' = '2026-02-12'
    and values.value ->> 'key_name' = 'alpha';
  if large_alpha_cell -> 'source_count' is distinct from '"9007199254740993"'::jsonb then
    raise exception 'daily source_count lost exact precision: %', page_one;
  end if;

  select pg_catalog.sum((values.value ->> 'source_bytes')::numeric)
  into summed_daily_source_bytes
  from pg_catalog.jsonb_array_elements(page_one -> 'daily_totals') as values(value);
  if summed_daily_source_bytes is distinct from (page_one #>> '{summary,source_bytes}')::numeric then
    raise exception 'daily source bytes do not sum to summary: %', page_one;
  end if;

  search_daily := public.get_public_daily_usage(
    date '2026-02-10',
    date '2026-02-12',
    'alpha',
    1,
    1
  );
  if search_daily -> 'summary' is distinct from '{"source_bytes":"1330","archive_count":5,"archive_hour_count":3,"active_key_count":1,"day_count":3}'::jsonb
    or search_daily -> 'daily_totals' is distinct from '[{"date":"2026-02-10","source_bytes":"730","archive_count":4,"archive_hour_count":2,"active_key_count":1},{"date":"2026-02-11","source_bytes":"0","archive_count":0,"archive_hour_count":0,"active_key_count":0},{"date":"2026-02-12","source_bytes":"600","archive_count":1,"archive_hour_count":1,"active_key_count":1}]'::jsonb then
    raise exception 'filtered daily summary is incorrect: %', search_daily;
  end if;

  hourly := public.get_public_hourly_usage(date '2026-02-10', 'alpha');
  if not hourly ?& array[
    'metric_basis',
    'timezone',
    'date',
    'key_name',
    'latest_sync_at',
    'hours'
  ] or (
    select pg_catalog.count(*)
    from pg_catalog.jsonb_object_keys(hourly)
  ) <> 6 then
    raise exception 'hourly top-level fields are incorrect: %', hourly;
  end if;
  if not hourly @> '{"metric_basis":"source_bytes","timezone":"UTC","date":"2026-02-10","key_name":"alpha"}'::jsonb
    or pg_catalog.jsonb_typeof(hourly -> 'latest_sync_at') is distinct from 'string' then
    raise exception 'hourly top-level values are incorrect: %', hourly;
  end if;
  if (
    select pg_catalog.jsonb_agg(values.value -> 'hour_start' order by values.ordinal)
    from pg_catalog.jsonb_array_elements(hourly -> 'hours')
      with ordinality as values(value, ordinal)
  ) is distinct from '["2026-02-10T00:00:00+00:00","2026-02-10T03:00:00+00:00"]'::jsonb then
    raise exception 'hourly rows are not sparse and ascending: %', hourly;
  end if;

  select values.value
  into hour_value
  from pg_catalog.jsonb_array_elements(hourly -> 'hours') as values(value)
  where values.value ->> 'hour_start' = '2026-02-10T00:00:00+00:00';
  if hour_value is distinct from '{"hour_start":"2026-02-10T00:00:00+00:00","source_count":"9","source_bytes":"700","gpt_source_bytes":"100","claude_source_bytes":"200","grok_source_bytes":"400","batch_count":3,"usage_precision":"batch_only"}'::jsonb then
    raise exception 'hourly batch-only provider totals are incorrect: %', hourly;
  end if;

  select values.value
  into hour_value
  from pg_catalog.jsonb_array_elements(hourly -> 'hours') as values(value)
  where values.value ->> 'hour_start' = '2026-02-10T03:00:00+00:00';
  if hour_value is distinct from '{"hour_start":"2026-02-10T03:00:00+00:00","source_count":"1","source_bytes":"30","gpt_source_bytes":"30","claude_source_bytes":"0","grok_source_bytes":"0","batch_count":1,"usage_precision":"exact"}'::jsonb then
    raise exception 'hourly exact provider totals are incorrect: %', hourly;
  end if;

  if exists (
    select 1
    from pg_catalog.jsonb_array_elements(hourly -> 'hours') as values(value)
    where (values.value ->> 'source_bytes')::numeric is distinct from
      (values.value ->> 'gpt_source_bytes')::numeric
      + (values.value ->> 'claude_source_bytes')::numeric
      + (values.value ->> 'grok_source_bytes')::numeric
  ) then
    raise exception 'hourly provider bytes do not sum to source_bytes: %', hourly;
  end if;

  foreach forbidden_field in array array[
    'jsonl_bytes',
    'compressed_bytes',
    'object_key',
    'archive_sha256',
    'manifest_sha256'
  ]
  loop
    if pg_catalog.strpos(hourly::text, '"' || forbidden_field || '"') > 0 then
      raise exception 'hourly response exposes restricted field %: %', forbidden_field, hourly;
    end if;
  end loop;

  missing_hourly := public.get_public_hourly_usage(date '2026-02-11', 'alpha');
  if missing_hourly -> 'hours' is distinct from '[]'::jsonb then
    raise exception 'missing hourly date fabricated rows: %', missing_hourly;
  end if;
  missing_hourly := public.get_public_hourly_usage(date '2026-02-10', 'unauthenticated');
  if missing_hourly -> 'hours' is distinct from '[]'::jsonb then
    raise exception 'hidden direct key leaked hourly rows: %', missing_hourly;
  end if;
  missing_hourly := public.get_public_hourly_usage(date '2026-02-10', ' alpha ');
  if missing_hourly -> 'hours' is distinct from '[]'::jsonb
    or missing_hourly -> 'key_name' is distinct from '" alpha "'::jsonb then
    raise exception 'hourly exact key matching was rewritten: %', missing_hourly;
  end if;

  validation_seen := false;
  begin
    perform public.get_public_hourly_usage(null::date, 'alpha');
  exception
    when sqlstate '22023' then
      validation_seen := sqlerrm like 'validation_error:%';
  end;
  if not validation_seen then
    raise exception 'hourly null date validation was not raised';
  end if;

  validation_seen := false;
  begin
    perform public.get_public_hourly_usage(date '2026-02-10', null::text);
  exception
    when sqlstate '22023' then
      validation_seen := sqlerrm like 'validation_error:%';
  end;
  if not validation_seen then
    raise exception 'hourly null key validation was not raised';
  end if;

  validation_seen := false;
  begin
    perform public.get_public_hourly_usage(date '2026-02-10', '   ');
  exception
    when sqlstate '22023' then
      validation_seen := sqlerrm like 'validation_error:%';
  end;
  if not validation_seen then
    raise exception 'hourly empty key validation was not raised';
  end if;

  validation_seen := false;
  begin
    perform public.get_public_hourly_usage(
      date '2026-02-10',
      pg_catalog.repeat('x', 49)
    );
  exception
    when sqlstate '22023' then
      validation_seen := sqlerrm like 'validation_error:%';
  end;
  if not validation_seen then
    raise exception 'hourly overlong key validation was not raised';
  end if;
end;
$assert_daily_summary$;

do $assert_hourly_mode_metadata$
declare
  fixture record;
  hourly jsonb;
  role_name text;
  table_name text;
begin
  update public.log_upload_batches
  set ingested_at = timestamptz '2000-01-01T00:00:00Z';

  for fixture in
    select values.*
    from (values
      ('sql-hourly-live-query', '2026-03-01T00:00:00Z', date '2026-03-01', 'UTC', 'mode-live-alpha', false, 11::bigint, timestamptz '2040-01-01T00:00:00Z'),
      ('sql-hourly-live-latest', '2026-03-02T00:00:00+09:00', date '2026-03-02', 'Asia/Tokyo', 'mode-live-latest', false, 12::bigint, timestamptz '2040-01-02T03:04:05Z'),
      ('sql-hourly-test-query', '2027-01-01T00:00:00Z', date '2027-01-01', 'UTC', 'mode-test-alpha', true, 21::bigint, timestamptz '2042-01-01T00:00:00Z'),
      ('sql-hourly-test-latest', '2027-01-02T00:00:00-08:00', date '2027-01-02', 'America/Los_Angeles', 'mode-test-latest', true, 22::bigint, timestamptz '2042-01-02T03:04:05Z')
    ) as values(event_id, hour_start, usage_date, timezone, key_name, is_test, source_bytes, ingested_at)
  loop
    perform public.ingest_log_usage_v1(
      pg_catalog.jsonb_build_object(
        'schema_version', 1,
        'event_id', fixture.event_id,
        'target_id', 'assertions',
        'object_key', 'assertions/' || fixture.event_id || '.jsonl.zst',
        'archive_sha256', pg_catalog.repeat('a', 64),
        'manifest_sha256', pg_catalog.repeat('b', 64),
        'hour_start', fixture.hour_start,
        'timezone', fixture.timezone,
        'usage_date', fixture.usage_date,
        'source_count', 1,
        'source_bytes', fixture.source_bytes,
        'jsonl_bytes', fixture.source_bytes * 10,
        'compressed_bytes', fixture.source_bytes,
        'test_mode', fixture.is_test,
        'usage', pg_catalog.jsonb_build_array(
          pg_catalog.jsonb_build_object(
            'key_name', fixture.key_name,
            'provider', 'codex',
            'source_count', 1,
            'source_bytes', fixture.source_bytes,
            'jsonl_bytes', fixture.source_bytes * 10
          )
        )
      ),
      pg_catalog.repeat('d', 64)
    );

    update public.log_upload_batches
    set ingested_at = fixture.ingested_at
    where event_id = fixture.event_id;
  end loop;

  hourly := public.get_public_hourly_usage(
    date '2026-03-01',
    'mode-live-alpha'
  );
  if hourly ->> 'latest_sync_at' is distinct from '2040-01-02T03:04:05+00:00'
    or hourly ->> 'timezone' is distinct from 'Asia/Tokyo'
    or not (hourly -> 'hours') @> '[{"source_bytes":"11","hour_start":"2026-03-01T00:00:00+00:00"}]'::jsonb then
    raise exception 'hourly live metadata is not current-mode global: %', hourly;
  end if;

  delete from public.log_upload_batches
  where is_test = false;

  if exists (
    select 1
    from public.log_upload_batches
    where is_test = false
  ) then
    raise exception 'live batches remain before hourly test fallback';
  end if;

  hourly := public.get_public_hourly_usage(
    date '2027-01-01',
    'mode-test-alpha'
  );
  if hourly ->> 'latest_sync_at' is distinct from '2042-01-02T03:04:05+00:00'
    or hourly ->> 'timezone' is distinct from 'America/Los_Angeles'
    or not (hourly -> 'hours') @> '[{"source_bytes":"21","hour_start":"2027-01-01T00:00:00+00:00"}]'::jsonb then
    raise exception 'hourly test fallback metadata is incorrect: %', hourly;
  end if;

  foreach role_name in array array['anon', 'authenticated']
  loop
    if not pg_catalog.has_function_privilege(
      role_name,
      'public.get_public_daily_usage(date,date,text,integer,integer)',
      'execute'
    ) or not pg_catalog.has_function_privilege(
      role_name,
      'public.get_public_hourly_usage(date,text)',
      'execute'
    ) then
      raise exception '% lost a public usage RPC grant', role_name;
    end if;

    foreach table_name in array array['log_upload_batches', 'log_upload_usage']
    loop
      if pg_catalog.has_table_privilege(
        role_name,
        'public.' || table_name,
        'select'
      ) then
        raise exception '% unexpectedly reads public.% after fallback', role_name, table_name;
      end if;
    end loop;
  end loop;

  if pg_catalog.has_function_privilege(
    'service_role',
    'public.get_public_daily_usage(date,date,text,integer,integer)',
    'execute'
  ) or pg_catalog.has_function_privilege(
    'service_role',
    'public.get_public_hourly_usage(date,text)',
    'execute'
  ) then
    raise exception 'service_role regained a public usage RPC grant';
  end if;
end;
$assert_hourly_mode_metadata$;

rollback;
