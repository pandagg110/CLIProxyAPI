create index log_upload_batches_mode_date_hour_event_idx
  on public.log_upload_batches (is_test, usage_date, hour_start, event_id);

create index log_upload_batches_mode_ingested_at_idx
  on public.log_upload_batches (is_test, ingested_at desc);

create index log_upload_batches_mode_hour_event_timezone_idx
  on public.log_upload_batches (is_test, hour_start desc, event_id desc)
  include (timezone);

create or replace function public.get_public_daily_usage(
  p_from date,
  p_to date,
  p_search text,
  p_page integer,
  p_page_size integer
)
returns jsonb
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
  _using_test_data boolean;
  _timezone text;
  _search text := pg_catalog.btrim(coalesce(p_search, ''));
  _result jsonb;
begin
  if p_from is null or p_to is null then
    raise exception using errcode = '22023', message = 'validation_error: from and to are required';
  end if;
  if p_from > p_to then
    raise exception using errcode = '22023', message = 'validation_error: from must not be after to';
  end if;
  if p_to - p_from > 365 then
    raise exception using errcode = '22023', message = 'validation_error: date range must not exceed 366 days';
  end if;
  if p_page is null or p_page < 1 then
    raise exception using errcode = '22023', message = 'validation_error: page must be at least 1';
  end if;
  if p_page_size is null or p_page_size < 1 or p_page_size > 20 then
    raise exception using errcode = '22023', message = 'validation_error: page_size must be from 1 to 20';
  end if;
  if pg_catalog.char_length(_search) > 100 then
    raise exception using errcode = '22023', message = 'validation_error: search must not exceed 100 characters';
  end if;

  _using_test_data := not exists (
    select 1
    from public.log_upload_batches
    where is_test = false
  );

  select b.timezone
  into _timezone
  from public.log_upload_batches as b
  where b.is_test = _using_test_data
  order by b.hour_start desc, b.event_id desc
  limit 1;
  _timezone := coalesce(_timezone, 'UTC');

  with eligible_batches as not materialized (
    select
      b.event_id,
      b.usage_date,
      b.hour_start,
      b.ingested_at,
      b.usage_precision
    from public.log_upload_batches as b
    where b.is_test = _using_test_data
  ),
  filtered_usage as (
    select
      b.event_id,
      b.usage_date,
      b.hour_start,
      b.ingested_at,
      b.usage_precision,
      u.key_name,
      u.provider,
      u.source_count,
      u.source_bytes,
      u.jsonl_bytes
    from public.log_upload_usage as u
    join eligible_batches as b on b.event_id = u.event_id
    where b.usage_date between p_from and p_to
      and u.key_name <> 'unauthenticated'
      and u.key_name !~* '^key-[0-9a-f]{12}$'
      and pg_catalog.btrim(u.key_name) <> ''
      and (
        _search = ''
        or u.key_name ilike (
          '%' ||
          pg_catalog.replace(
            pg_catalog.replace(
              pg_catalog.replace(_search, E'\\', E'\\\\'),
              '%', E'\\%'
            ),
            '_', E'\\_'
          ) ||
          '%'
        ) escape E'\\'
      )
  ),
  summary_values as (
    select
      coalesce(pg_catalog.sum(filtered.source_bytes), 0::numeric) as source_bytes,
      pg_catalog.count(distinct filtered.event_id) as archive_count,
      pg_catalog.count(distinct filtered.hour_start) as archive_hour_count,
      pg_catalog.count(distinct filtered.key_name) as active_key_count,
      (p_to - p_from + 1)::integer as day_count
    from filtered_usage as filtered
  ),
  day_values as (
    select series.value::date as usage_date
    from pg_catalog.generate_series(
      p_from::timestamp,
      p_to::timestamp,
      interval '1 day'
    ) as series(value)
  ),
  daily_total_values as (
    select
      days.usage_date,
      coalesce(pg_catalog.sum(filtered.source_bytes), 0::numeric) as source_bytes,
      pg_catalog.count(distinct filtered.event_id) as archive_count,
      pg_catalog.count(distinct filtered.hour_start) as archive_hour_count,
      pg_catalog.count(distinct filtered.key_name) as active_key_count
    from day_values as days
    left join filtered_usage as filtered on filtered.usage_date = days.usage_date
    group by days.usage_date
  ),
  name_totals as (
    select
      filtered.key_name,
      pg_catalog.sum(filtered.source_bytes) as total_source_bytes
    from filtered_usage as filtered
    group by filtered.key_name
  ),
  paged_names as (
    select
      names.key_name,
      names.total_source_bytes,
      pg_catalog.row_number() over (
        order by names.total_source_bytes desc, names.key_name
      ) as ordinal
    from name_totals as names
    order by names.total_source_bytes desc, names.key_name
    limit p_page_size
    offset ((p_page::bigint - 1::bigint) * p_page_size::bigint)
  ),
  cell_values as (
    select
      filtered.usage_date,
      filtered.key_name,
      names.ordinal,
      coalesce(pg_catalog.sum(filtered.source_bytes), 0::numeric) as source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'codex'),
        0::numeric
      ) as gpt_source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'fable5'),
        0::numeric
      ) as claude_source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'grok45'),
        0::numeric
      ) as grok_source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_count),
        0::numeric
      ) as source_count,
      pg_catalog.bool_and(
        filtered.usage_precision = 'exact' and filtered.jsonl_bytes is not null
      ) as all_exact,
      pg_catalog.sum(filtered.jsonl_bytes) as jsonl_bytes,
      coalesce(
        pg_catalog.sum(filtered.jsonl_bytes) filter (where filtered.provider = 'codex'),
        0::numeric
      ) as gpt_bytes,
      coalesce(
        pg_catalog.sum(filtered.jsonl_bytes) filter (where filtered.provider = 'fable5'),
        0::numeric
      ) as claude_bytes,
      coalesce(
        pg_catalog.sum(filtered.jsonl_bytes) filter (where filtered.provider = 'grok45'),
        0::numeric
      ) as grok_bytes,
      pg_catalog.count(distinct filtered.event_id) as batch_count
    from filtered_usage as filtered
    join paged_names as names on names.key_name = filtered.key_name
    group by filtered.usage_date, filtered.key_name, names.ordinal
  )
  select pg_catalog.jsonb_build_object(
    'metric_basis', 'source_bytes',
    'timezone', _timezone,
    'from', p_from,
    'to', p_to,
    'using_test_data', _using_test_data,
    'pagination', pg_catalog.jsonb_build_object(
      'page', p_page,
      'page_size', p_page_size,
      'total', (select pg_catalog.count(*) from name_totals)
    ),
    'names', coalesce(
      (
        select pg_catalog.jsonb_agg(names.key_name order by names.ordinal)
        from paged_names as names
      ),
      '[]'::jsonb
    ),
    'days', coalesce(
      (
        select pg_catalog.jsonb_agg(days.usage_date order by days.usage_date)
        from day_values as days
      ),
      '[]'::jsonb
    ),
    'cells', coalesce(
      (
        select pg_catalog.jsonb_agg(
          pg_catalog.jsonb_build_object(
            'date', cells.usage_date,
            'key_name', cells.key_name,
            'source_bytes', cells.source_bytes::text,
            'gpt_source_bytes', cells.gpt_source_bytes::text,
            'claude_source_bytes', cells.claude_source_bytes::text,
            'grok_source_bytes', cells.grok_source_bytes::text,
            'source_count', cells.source_count::text,
            'usage_precision', case
              when cells.all_exact then 'exact'
              else 'batch_only'
            end,
            'jsonl_bytes', case
              when cells.all_exact then cells.jsonl_bytes::text
              else null
            end,
            'gpt_bytes', case
              when cells.all_exact then cells.gpt_bytes::text
              else null
            end,
            'claude_bytes', case
              when cells.all_exact then cells.claude_bytes::text
              else null
            end,
            'grok_bytes', case
              when cells.all_exact then cells.grok_bytes::text
              else null
            end,
            'batch_count', cells.batch_count
          )
          order by cells.usage_date, cells.ordinal
        )
        from cell_values as cells
      ),
      '[]'::jsonb
    ),
    'summary', (
      select pg_catalog.jsonb_build_object(
        'source_bytes', summary.source_bytes::text,
        'archive_count', summary.archive_count,
        'archive_hour_count', summary.archive_hour_count,
        'active_key_count', summary.active_key_count,
        'day_count', summary.day_count
      )
      from summary_values as summary
    ),
    'daily_totals', coalesce(
      (
        select pg_catalog.jsonb_agg(
          pg_catalog.jsonb_build_object(
            'date', totals.usage_date,
            'source_bytes', totals.source_bytes::text,
            'archive_count', totals.archive_count,
            'archive_hour_count', totals.archive_hour_count,
            'active_key_count', totals.active_key_count
          )
          order by totals.usage_date
        )
        from daily_total_values as totals
      ),
      '[]'::jsonb
    ),
    'latest_sync_at', (
      select pg_catalog.max(batches.ingested_at)
      from eligible_batches as batches
    )
  )
  into _result;

  return _result;
end;
$$;

revoke all on function public.get_public_daily_usage(date, date, text, integer, integer) from public, service_role;
revoke all on function public.get_public_daily_usage(date, date, text, integer, integer) from anon, authenticated;
grant execute on function public.get_public_daily_usage(date, date, text, integer, integer) to anon, authenticated;

create or replace function public.get_public_hourly_usage(
  p_date date,
  p_key_name text
)
returns jsonb
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
  _using_test_data boolean;
  _timezone text;
  _result jsonb;
begin
  if p_date is null then
    raise exception using errcode = '22023', message = 'validation_error: date is required';
  end if;
  if p_key_name is null or pg_catalog.btrim(p_key_name) = '' then
    raise exception using errcode = '22023', message = 'validation_error: key_name is required';
  end if;
  if pg_catalog.char_length(pg_catalog.btrim(p_key_name)) > 48 then
    raise exception using errcode = '22023', message = 'validation_error: key_name must not exceed 48 characters';
  end if;

  _using_test_data := not exists (
    select 1
    from public.log_upload_batches
    where is_test = false
  );

  select b.timezone
  into _timezone
  from public.log_upload_batches as b
  where b.is_test = _using_test_data
  order by b.hour_start desc, b.event_id desc
  limit 1;
  _timezone := coalesce(_timezone, 'UTC');

  with eligible_batches as not materialized (
    select
      b.event_id,
      b.usage_date,
      b.hour_start,
      b.ingested_at,
      b.usage_precision
    from public.log_upload_batches as b
    where b.is_test = _using_test_data
  ),
  filtered_usage as (
    select
      b.event_id,
      b.hour_start,
      b.usage_precision,
      u.provider,
      u.source_count,
      u.source_bytes,
      u.jsonl_bytes
    from public.log_upload_usage as u
    join eligible_batches as b on b.event_id = u.event_id
    where b.usage_date = p_date
      and u.key_name = p_key_name
      and u.key_name <> 'unauthenticated'
      and u.key_name !~* '^key-[0-9a-f]{12}$'
      and pg_catalog.btrim(u.key_name) <> ''
  ),
  hour_values as (
    select
      filtered.hour_start,
      coalesce(
        pg_catalog.sum(filtered.source_count),
        0::numeric
      ) as source_count,
      coalesce(
        pg_catalog.sum(filtered.source_bytes),
        0::numeric
      ) as source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'codex'),
        0::numeric
      ) as gpt_source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'fable5'),
        0::numeric
      ) as claude_source_bytes,
      coalesce(
        pg_catalog.sum(filtered.source_bytes) filter (where filtered.provider = 'grok45'),
        0::numeric
      ) as grok_source_bytes,
      pg_catalog.count(distinct filtered.event_id) as batch_count,
      pg_catalog.bool_and(
        filtered.usage_precision = 'exact' and filtered.jsonl_bytes is not null
      ) as all_exact
    from filtered_usage as filtered
    group by filtered.hour_start
  ),
  rendered_hours as (
    select
      hours.hour_start,
      pg_catalog.jsonb_build_object(
        'hour_start', pg_catalog.to_char(hours.hour_start, 'YYYY-MM-DD"T"HH24:MI:SSTZH:TZM'),
        'source_count', hours.source_count::text,
        'source_bytes', hours.source_bytes::text,
        'gpt_source_bytes', hours.gpt_source_bytes::text,
        'claude_source_bytes', hours.claude_source_bytes::text,
        'grok_source_bytes', hours.grok_source_bytes::text,
        'batch_count', hours.batch_count,
        'usage_precision', case when hours.all_exact then 'exact' else 'batch_only' end
      ) as hour_json
    from hour_values as hours
  )
  select pg_catalog.jsonb_build_object(
    'metric_basis', 'source_bytes',
    'timezone', _timezone,
    'date', p_date,
    'key_name', p_key_name,
    'latest_sync_at', (select pg_catalog.max(batches.ingested_at) from eligible_batches as batches),
    'hours', coalesce((select pg_catalog.jsonb_agg(hour_json order by hour_start) from rendered_hours), '[]'::jsonb)
  )
  into _result;

  return _result;
end;
$$;

revoke all on function public.get_public_hourly_usage(date, text) from public, service_role;
revoke all on function public.get_public_hourly_usage(date, text) from anon, authenticated;
grant execute on function public.get_public_hourly_usage(date, text) to anon, authenticated;
