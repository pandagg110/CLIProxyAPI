create table public.log_upload_batches (
  event_id text primary key,
  target_id text not null,
  object_key text not null,
  archive_sha256 text not null,
  manifest_sha256 text not null,
  hour_start timestamptz not null,
  timezone text not null,
  usage_date date not null,
  source_count bigint not null,
  source_bytes bigint not null,
  jsonl_bytes bigint not null,
  compressed_bytes bigint not null,
  payload_sha256 text not null,
  is_test boolean not null,
  ingested_at timestamptz not null default now(),
  constraint log_upload_batches_event_id_not_blank check (btrim(event_id) <> ''),
  constraint log_upload_batches_target_id_not_blank check (btrim(target_id) <> ''),
  constraint log_upload_batches_object_key_not_blank check (btrim(object_key) <> ''),
  constraint log_upload_batches_archive_sha256_format check (archive_sha256 ~ '^[0-9a-f]{64}$'),
  constraint log_upload_batches_manifest_sha256_format check (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  constraint log_upload_batches_payload_sha256_format check (payload_sha256 ~ '^[0-9a-f]{64}$'),
  constraint log_upload_batches_timezone_not_blank check (btrim(timezone) <> ''),
  constraint log_upload_batches_source_count_nonnegative check (source_count >= 0),
  constraint log_upload_batches_source_bytes_nonnegative check (source_bytes >= 0),
  constraint log_upload_batches_jsonl_bytes_nonnegative check (jsonl_bytes >= 0),
  constraint log_upload_batches_compressed_bytes_nonnegative check (compressed_bytes >= 0)
);

create table public.log_upload_usage (
  event_id text not null,
  key_name text not null,
  provider text not null,
  source_count bigint not null,
  source_bytes bigint not null,
  jsonl_bytes bigint not null,
  constraint log_upload_usage_primary_key primary key (event_id, key_name, provider),
  constraint log_upload_usage_event_id_foreign_key
    foreign key (event_id) references public.log_upload_batches (event_id) on delete cascade,
  constraint log_upload_usage_key_name_not_blank check (btrim(key_name) <> ''),
  constraint log_upload_usage_provider_allowed check (provider in ('codex', 'fable5', 'grok45')),
  constraint log_upload_usage_source_count_nonnegative check (source_count >= 0),
  constraint log_upload_usage_source_bytes_nonnegative check (source_bytes >= 0),
  constraint log_upload_usage_jsonl_bytes_nonnegative check (jsonl_bytes >= 0)
);

create index log_upload_batches_mode_date_idx
  on public.log_upload_batches (is_test, usage_date);

create index log_upload_usage_key_name_idx
  on public.log_upload_usage (key_name);

alter table public.log_upload_batches enable row level security;
alter table public.log_upload_usage enable row level security;

revoke all on table public.log_upload_batches from public;
revoke all on table public.log_upload_batches from anon, authenticated, service_role;
revoke all on table public.log_upload_usage from public;
revoke all on table public.log_upload_usage from anon, authenticated, service_role;

create or replace function public.ingest_log_usage_v1(payload jsonb, payload_sha256 text)
returns jsonb
language plpgsql
security definer
set search_path = pg_catalog, public
as $$
declare
  _event_id text;
  _target_id text;
  _object_key text;
  _archive_sha256 text;
  _manifest_sha256 text;
  _payload_sha256 text;
  _hour_start timestamptz;
  _timezone text;
  _usage_date date;
  _is_test boolean;
  _batch_source_count numeric;
  _batch_source_bytes numeric;
  _batch_jsonl_bytes numeric;
  _batch_compressed_bytes numeric;
  _usage_source_count numeric := 0;
  _usage_source_bytes numeric := 0;
  _usage_jsonl_bytes numeric := 0;
  _usage_item jsonb;
  _key_name text;
  _trimmed_key_name text;
  _provider text;
  _item_source_count numeric;
  _item_source_bytes numeric;
  _item_jsonl_bytes numeric;
  _usage_count bigint;
  _unique_usage_count bigint;
  _inserted integer;
  _existing_payload_sha256 text;
begin
  if pg_catalog.jsonb_typeof(payload) is distinct from 'object' then
    raise exception using errcode = '22023', message = 'validation_error: payload must be a JSON object';
  end if;
  if exists (
    select 1
    from pg_catalog.jsonb_object_keys(payload) as fields(name)
    where fields.name not in (
      'schema_version',
      'event_id',
      'target_id',
      'object_key',
      'archive_sha256',
      'manifest_sha256',
      'hour_start',
      'timezone',
      'usage_date',
      'source_count',
      'source_bytes',
      'jsonl_bytes',
      'compressed_bytes',
      'test_mode',
      'usage'
    )
  ) then
    raise exception using errcode = '22023', message = 'validation_error: payload contains unsupported fields';
  end if;
  if payload ->> 'schema_version' is distinct from '1'
    or pg_catalog.jsonb_typeof(payload -> 'schema_version') is distinct from 'number' then
    raise exception using errcode = '22023', message = 'validation_error: schema_version must be 1';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'event_id') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: event_id must be non-empty text';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'target_id') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: target_id must be non-empty text';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'object_key') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: object_key must be non-empty text';
  end if;
  _event_id := payload ->> 'event_id';
  _target_id := payload ->> 'target_id';
  _object_key := payload ->> 'object_key';
  if pg_catalog.btrim(_event_id) = '' or pg_catalog.char_length(_event_id) > 200 then
    raise exception using errcode = '22023', message = 'validation_error: event_id must be non-empty text';
  end if;
  if pg_catalog.btrim(_target_id) = '' or pg_catalog.char_length(_target_id) > 200 then
    raise exception using errcode = '22023', message = 'validation_error: target_id must be non-empty text';
  end if;
  if pg_catalog.btrim(_object_key) = '' or pg_catalog.char_length(_object_key) > 2048 then
    raise exception using errcode = '22023', message = 'validation_error: object_key must be non-empty text';
  end if;
  if pg_catalog.left(pg_catalog.btrim(_object_key), 1) = '/'
    or pg_catalog.strpos(pg_catalog.btrim(_object_key), E'\\') > 0
    or pg_catalog.strpos(pg_catalog.btrim(_object_key), '?') > 0
    or pg_catalog.strpos(pg_catalog.btrim(_object_key), '#') > 0
    or pg_catalog.btrim(_object_key) ~* '^[a-z][a-z0-9+.-]*:'
    or exists (
      select 1
      from pg_catalog.regexp_split_to_table(
        pg_catalog.btrim(_object_key),
        '/'
      ) as segments(value)
      where segments.value ~* '^(\.|%2e){2}$'
    ) then
    raise exception using errcode = '22023', message = 'validation_error: object_key must be a safe relative object key';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'archive_sha256') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: archive_sha256 must be a SHA-256 hex digest';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'manifest_sha256') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: manifest_sha256 must be a SHA-256 hex digest';
  end if;
  _archive_sha256 := pg_catalog.lower(payload ->> 'archive_sha256');
  _manifest_sha256 := pg_catalog.lower(payload ->> 'manifest_sha256');
  _payload_sha256 := pg_catalog.lower($2);
  if _archive_sha256 !~ '^[0-9a-f]{64}$' then
    raise exception using errcode = '22023', message = 'validation_error: archive_sha256 must be a SHA-256 hex digest';
  end if;
  if _manifest_sha256 !~ '^[0-9a-f]{64}$' then
    raise exception using errcode = '22023', message = 'validation_error: manifest_sha256 must be a SHA-256 hex digest';
  end if;
  if _payload_sha256 is null or _payload_sha256 !~ '^[0-9a-f]{64}$' then
    raise exception using errcode = '22023', message = 'validation_error: payload_sha256 must be a SHA-256 hex digest';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'timezone') is distinct from 'string' then
    raise exception using errcode = '22023', message = 'validation_error: timezone must be non-empty text';
  end if;
  _timezone := payload ->> 'timezone';
  if pg_catalog.btrim(_timezone) = '' or pg_catalog.char_length(_timezone) > 100 then
    raise exception using errcode = '22023', message = 'validation_error: timezone must be non-empty text';
  end if;
  if not exists (
    select 1
    from pg_catalog.pg_timezone_names
    where name = _timezone
  ) then
    raise exception using errcode = '22023', message = 'validation_error: timezone must be a valid IANA timezone';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'hour_start') is distinct from 'string'
    or payload ->> 'hour_start' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$' then
    raise exception using errcode = '22023', message = 'validation_error: hour_start must be an ISO-8601 timestamp with an offset';
  end if;
  begin
    _hour_start := (payload ->> 'hour_start')::timestamptz;
  exception
    when others then
      raise exception using errcode = '22023', message = 'validation_error: hour_start must be an ISO-8601 timestamp with an offset';
  end;
  if not pg_catalog.isfinite(_hour_start) then
    raise exception using errcode = '22023', message = 'validation_error: hour_start must be an ISO-8601 timestamp with an offset';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'usage_date') is distinct from 'string'
    or payload ->> 'usage_date' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' then
    raise exception using errcode = '22023', message = 'validation_error: usage_date must be a valid YYYY-MM-DD date';
  end if;
  begin
    _usage_date := (payload ->> 'usage_date')::date;
  exception
    when others then
      raise exception using errcode = '22023', message = 'validation_error: usage_date must be a valid YYYY-MM-DD date';
  end;
  if pg_catalog.to_char(_usage_date, 'YYYY-MM-DD') <> payload ->> 'usage_date' then
    raise exception using errcode = '22023', message = 'validation_error: usage_date must be a valid YYYY-MM-DD date';
  end if;
  if (_hour_start at time zone _timezone)::date <> _usage_date then
    raise exception using errcode = '22023', message = 'validation_error: usage_date must match hour_start in timezone';
  end if;

  if payload ? 'test_mode' then
    if pg_catalog.jsonb_typeof(payload -> 'test_mode') is distinct from 'boolean' then
      raise exception using errcode = '22023', message = 'validation_error: test_mode must be a boolean';
    end if;
    _is_test := (payload ->> 'test_mode')::boolean;
  else
    _is_test := false;
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'source_count') is distinct from 'number'
    or payload ->> 'source_count' !~ '^(0|[1-9][0-9]*)$' then
    raise exception using errcode = '22023', message = 'validation_error: source_count must be a nonnegative integer';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'source_bytes') is distinct from 'number'
    or payload ->> 'source_bytes' !~ '^(0|[1-9][0-9]*)$' then
    raise exception using errcode = '22023', message = 'validation_error: source_bytes must be a nonnegative integer';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'jsonl_bytes') is distinct from 'number'
    or payload ->> 'jsonl_bytes' !~ '^(0|[1-9][0-9]*)$' then
    raise exception using errcode = '22023', message = 'validation_error: jsonl_bytes must be a nonnegative integer';
  end if;
  if pg_catalog.jsonb_typeof(payload -> 'compressed_bytes') is distinct from 'number'
    or payload ->> 'compressed_bytes' !~ '^(0|[1-9][0-9]*)$' then
    raise exception using errcode = '22023', message = 'validation_error: compressed_bytes must be a nonnegative integer';
  end if;

  _batch_source_count := (payload ->> 'source_count')::numeric;
  _batch_source_bytes := (payload ->> 'source_bytes')::numeric;
  _batch_jsonl_bytes := (payload ->> 'jsonl_bytes')::numeric;
  _batch_compressed_bytes := (payload ->> 'compressed_bytes')::numeric;
  if _batch_source_count > 9223372036854775807
    or _batch_source_bytes > 9223372036854775807
    or _batch_jsonl_bytes > 9223372036854775807
    or _batch_compressed_bytes > 9223372036854775807 then
    raise exception using errcode = '22023', message = 'validation_error: numeric value exceeds bigint range';
  end if;

  if pg_catalog.jsonb_typeof(payload -> 'usage') is distinct from 'array' then
    raise exception using errcode = '22023', message = 'validation_error: usage must be an array';
  end if;
  if pg_catalog.jsonb_array_length(payload -> 'usage') > 10000 then
    raise exception using errcode = '22023', message = 'validation_error: usage has too many entries';
  end if;

  for _usage_item in
    select value
    from pg_catalog.jsonb_array_elements(payload -> 'usage') as entries(value)
  loop
    if pg_catalog.jsonb_typeof(_usage_item) is distinct from 'object' then
      raise exception using errcode = '22023', message = 'validation_error: usage entries must be objects';
    end if;
    if exists (
      select 1
      from pg_catalog.jsonb_object_keys(_usage_item) as fields(name)
      where fields.name not in (
        'key_name',
        'provider',
        'source_count',
        'source_bytes',
        'jsonl_bytes'
      )
    ) then
      raise exception using errcode = '22023', message = 'validation_error: usage entries contain unsupported fields';
    end if;
    if pg_catalog.jsonb_typeof(_usage_item -> 'key_name') is distinct from 'string' then
      raise exception using errcode = '22023', message = 'validation_error: key_name must be non-empty text';
    end if;
    if pg_catalog.jsonb_typeof(_usage_item -> 'provider') is distinct from 'string' then
      raise exception using errcode = '22023', message = 'validation_error: provider must be codex, fable5, or grok45';
    end if;

    _key_name := _usage_item ->> 'key_name';
    _trimmed_key_name := pg_catalog.btrim(_key_name);
    _provider := _usage_item ->> 'provider';
    if _trimmed_key_name = '' or pg_catalog.char_length(_key_name) > 256 then
      raise exception using errcode = '22023', message = 'validation_error: key_name must be non-empty text';
    end if;
    if _trimmed_key_name ~* '^sk-[^[:space:]]+'
      or _trimmed_key_name ~* '^bearer[[:space:]]+[^[:space:]]+'
      or _trimmed_key_name ~* '^[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}$'
      or (
        pg_catalog.char_length(_trimmed_key_name) >= 32
        and _trimmed_key_name ~ '^[-A-Za-z0-9_+/=.]+$'
        and (
          pg_catalog.char_length(_trimmed_key_name) >= 48
          or (
            (case when _trimmed_key_name ~ '[a-z]' then 1 else 0 end)
            + (case when _trimmed_key_name ~ '[A-Z]' then 1 else 0 end)
            + (case when _trimmed_key_name ~ '[0-9]' then 1 else 0 end)
            + (case when _trimmed_key_name ~ '[-_+/=.]' then 1 else 0 end)
          ) >= 3
        )
      ) then
      raise exception using errcode = '22023', message = 'validation_error: key_name must be a display label, not a secret';
    end if;
    if _provider not in ('codex', 'fable5', 'grok45') then
      raise exception using errcode = '22023', message = 'validation_error: provider must be codex, fable5, or grok45';
    end if;

    if pg_catalog.jsonb_typeof(_usage_item -> 'source_count') is distinct from 'number'
      or _usage_item ->> 'source_count' !~ '^(0|[1-9][0-9]*)$' then
      raise exception using errcode = '22023', message = 'validation_error: usage source_count must be a nonnegative integer';
    end if;
    if pg_catalog.jsonb_typeof(_usage_item -> 'source_bytes') is distinct from 'number'
      or _usage_item ->> 'source_bytes' !~ '^(0|[1-9][0-9]*)$' then
      raise exception using errcode = '22023', message = 'validation_error: usage source_bytes must be a nonnegative integer';
    end if;
    if pg_catalog.jsonb_typeof(_usage_item -> 'jsonl_bytes') is distinct from 'number'
      or _usage_item ->> 'jsonl_bytes' !~ '^(0|[1-9][0-9]*)$' then
      raise exception using errcode = '22023', message = 'validation_error: usage jsonl_bytes must be a nonnegative integer';
    end if;

    _item_source_count := (_usage_item ->> 'source_count')::numeric;
    _item_source_bytes := (_usage_item ->> 'source_bytes')::numeric;
    _item_jsonl_bytes := (_usage_item ->> 'jsonl_bytes')::numeric;
    if _item_source_count > 9223372036854775807
      or _item_source_bytes > 9223372036854775807
      or _item_jsonl_bytes > 9223372036854775807 then
      raise exception using errcode = '22023', message = 'validation_error: usage numeric value exceeds bigint range';
    end if;

    _usage_source_count := _usage_source_count + _item_source_count;
    _usage_source_bytes := _usage_source_bytes + _item_source_bytes;
    _usage_jsonl_bytes := _usage_jsonl_bytes + _item_jsonl_bytes;
    if _usage_source_count > 9223372036854775807
      or _usage_source_bytes > 9223372036854775807
      or _usage_jsonl_bytes > 9223372036854775807 then
      raise exception using errcode = '22023', message = 'validation_error: usage totals exceed bigint range';
    end if;
  end loop;

  select
    pg_catalog.count(*),
    pg_catalog.count(distinct (entry ->> 'key_name', entry ->> 'provider'))
  into _usage_count, _unique_usage_count
  from pg_catalog.jsonb_array_elements(payload -> 'usage') as usage_entries(entry);
  if _usage_count <> _unique_usage_count then
    raise exception using errcode = '22023', message = 'validation_error: usage entries must have unique key_name and provider pairs';
  end if;
  if _batch_source_count <> _usage_source_count
    or _batch_source_bytes <> _usage_source_bytes
    or _batch_jsonl_bytes <> _usage_jsonl_bytes then
    raise exception using errcode = '22023', message = 'validation_error: batch totals must equal usage totals';
  end if;

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
    is_test
  ) values (
    _event_id,
    _target_id,
    _object_key,
    _archive_sha256,
    _manifest_sha256,
    _hour_start,
    _timezone,
    _usage_date,
    _batch_source_count::bigint,
    _batch_source_bytes::bigint,
    _batch_jsonl_bytes::bigint,
    _batch_compressed_bytes::bigint,
    _payload_sha256,
    _is_test
  )
  on conflict (event_id) do nothing;

  get diagnostics _inserted = row_count;
  if _inserted = 0 then
    select batches.payload_sha256
    into _existing_payload_sha256
    from public.log_upload_batches as batches
    where batches.event_id = _event_id;

    if _existing_payload_sha256 = _payload_sha256 then
      return pg_catalog.jsonb_build_object('status', 'duplicate', 'event_id', _event_id);
    end if;
    raise exception using errcode = 'P0001', message = 'event_id_conflict';
  end if;

  insert into public.log_upload_usage (
    event_id,
    key_name,
    provider,
    source_count,
    source_bytes,
    jsonl_bytes
  )
  select
    _event_id,
    entry ->> 'key_name',
    entry ->> 'provider',
    (entry ->> 'source_count')::bigint,
    (entry ->> 'source_bytes')::bigint,
    (entry ->> 'jsonl_bytes')::bigint
  from pg_catalog.jsonb_array_elements(payload -> 'usage') as usage_entries(entry);

  return pg_catalog.jsonb_build_object('status', 'inserted', 'event_id', _event_id);
end;
$$;

revoke all on function public.ingest_log_usage_v1(jsonb, text) from public, anon, authenticated;
grant execute on function public.ingest_log_usage_v1(jsonb, text) to service_role;

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
    select b.event_id, b.usage_date, b.ingested_at
    from public.log_upload_batches as b
    where b.is_test = _using_test_data
  ),
  name_totals as (
    select
      u.key_name,
      pg_catalog.sum(u.jsonl_bytes) as total_jsonl_bytes
    from public.log_upload_usage as u
    join eligible_batches as b on b.event_id = u.event_id
    where b.usage_date between p_from and p_to
      and (_search = '' or u.key_name ilike '%' || _search || '%')
    group by u.key_name
  ),
  paged_names as (
    select
      names.key_name,
      names.total_jsonl_bytes,
      pg_catalog.row_number() over (
        order by names.total_jsonl_bytes desc, names.key_name
      ) as ordinal
    from name_totals as names
    order by names.total_jsonl_bytes desc, names.key_name
    limit p_page_size
    offset ((p_page - 1)::bigint * p_page_size::bigint)
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
      coalesce(pg_catalog.sum(u.jsonl_bytes), 0::numeric) as jsonl_bytes,
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
            'jsonl_bytes', cells.jsonl_bytes,
            'gpt_bytes', cells.gpt_bytes,
            'claude_bytes', cells.claude_bytes,
            'grok_bytes', cells.grok_bytes,
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
