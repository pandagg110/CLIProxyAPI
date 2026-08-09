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

  with eligible_batches as (
    select b.event_id, b.usage_date, b.ingested_at, b.usage_precision
    from public.log_upload_batches as b
    where b.is_test = _using_test_data
  ),
  name_totals as (
    select
      u.key_name,
      pg_catalog.sum(u.source_bytes) as total_source_bytes
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
    group by u.key_name
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
  day_values as (
    select series.value::date as usage_date
    from pg_catalog.generate_series(
      p_from::timestamp,
      p_to::timestamp,
      interval '1 day'
    ) as series(value)
  ),
  cell_values as (
    select
      b.usage_date,
      u.key_name,
      names.ordinal,
      coalesce(pg_catalog.sum(u.source_bytes), 0::numeric) as source_bytes,
      coalesce(
        pg_catalog.sum(u.source_bytes) filter (where u.provider = 'codex'),
        0::numeric
      ) as gpt_source_bytes,
      coalesce(
        pg_catalog.sum(u.source_bytes) filter (where u.provider = 'fable5'),
        0::numeric
      ) as claude_source_bytes,
      coalesce(
        pg_catalog.sum(u.source_bytes) filter (where u.provider = 'grok45'),
        0::numeric
      ) as grok_source_bytes,
      pg_catalog.bool_and(
        b.usage_precision = 'exact' and u.jsonl_bytes is not null
      ) as all_exact,
      pg_catalog.sum(u.jsonl_bytes) as jsonl_bytes,
      coalesce(
        pg_catalog.sum(u.jsonl_bytes) filter (where u.provider = 'codex'),
        0::numeric
      ) as gpt_bytes,
      coalesce(
        pg_catalog.sum(u.jsonl_bytes) filter (where u.provider = 'fable5'),
        0::numeric
      ) as claude_bytes,
      coalesce(
        pg_catalog.sum(u.jsonl_bytes) filter (where u.provider = 'grok45'),
        0::numeric
      ) as grok_bytes,
      pg_catalog.count(distinct b.event_id) as batch_count
    from public.log_upload_usage as u
    join eligible_batches as b on b.event_id = u.event_id
    join paged_names as names on names.key_name = u.key_name
    where b.usage_date between p_from and p_to
    group by b.usage_date, u.key_name, names.ordinal
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
