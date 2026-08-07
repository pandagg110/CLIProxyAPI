export const providers = ["codex", "fable5", "grok45"] as const;

export type Provider = (typeof providers)[number];

export interface LogUsageEntry {
  key_name: string;
  provider: Provider;
  source_count: number;
  source_bytes: number;
  jsonl_bytes: number;
}

export interface LogUsageEvent {
  schema_version: 1;
  event_id: string;
  target_id: string;
  object_key: string;
  archive_sha256: string;
  manifest_sha256: string;
  hour_start: string;
  timezone: string;
  usage_date: string;
  source_count: number;
  source_bytes: number;
  jsonl_bytes: number;
  compressed_bytes: number;
  test_mode: boolean;
  usage: LogUsageEntry[];
}

export interface IngestRpcResponse {
  status: "inserted" | "duplicate";
  event_id: string;
}

export interface DailyUsageCell {
  date: string;
  key_name: string;
  jsonl_bytes: string;
  gpt_bytes: string;
  claude_bytes: string;
  grok_bytes: string;
  batch_count: number;
}

export interface PublicDailyUsageResponse {
  timezone: string;
  from: string;
  to: string;
  using_test_data: boolean;
  pagination: {
    page: number;
    page_size: number;
    total: number;
  };
  names: string[];
  days: string[];
  cells: DailyUsageCell[];
  latest_sync_at: string | null;
}

export interface DailyQuery {
  from: string;
  to: string;
  search: string;
  page: number;
  pageSize: number;
}

export type ValidationResult<T> =
  | { ok: true; value: T }
  | { ok: false; error: string };

const MAX_PAGE = 2_147_483_647;

const ingestPayloadFields = new Set([
  "schema_version",
  "event_id",
  "target_id",
  "object_key",
  "archive_sha256",
  "manifest_sha256",
  "hour_start",
  "timezone",
  "usage_date",
  "source_count",
  "source_bytes",
  "jsonl_bytes",
  "compressed_bytes",
  "test_mode",
  "usage",
]);

const usageEntryFields = new Set([
  "key_name",
  "provider",
  "source_count",
  "source_bytes",
  "jsonl_bytes",
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRequiredText(value: unknown, maxLength: number): value is string {
  return typeof value === "string" && value.trim().length > 0 &&
    value.length <= maxLength;
}

function isSha256(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{64}$/i.test(value);
}

function isNonnegativeSafeInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function hasOnlyFields(
  value: Record<string, unknown>,
  allowedFields: Set<string>,
): boolean {
  return Object.keys(value).every((field) => allowedFields.has(field));
}

function isSafeObjectKey(value: string): boolean {
  const candidate = value.trim();
  if (
    candidate.startsWith("/") ||
    candidate.includes("\\") ||
    candidate.includes("?") ||
    candidate.includes("#") ||
    /^[a-z][a-z0-9+.-]*:/i.test(candidate)
  ) {
    return false;
  }
  return candidate.split("/").every(
    (segment) => !/^(?:\.|%2e){2}$/i.test(segment),
  );
}

function isSecretLikeKeyName(value: string): boolean {
  const candidate = value.trim();
  if (
    /^sk-\S+/i.test(candidate) ||
    /^bearer\s+\S+/i.test(candidate) ||
    /^[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}\.[a-z0-9_-]{8,}$/i.test(
      candidate,
    )
  ) {
    return true;
  }
  if (
    candidate.length < 32 ||
    !/^[a-z0-9_+/=.-]+$/i.test(candidate)
  ) {
    return false;
  }

  const characterClasses = [
    /[a-z]/.test(candidate),
    /[A-Z]/.test(candidate),
    /[0-9]/.test(candidate),
    /[_+/=.-]/.test(candidate),
  ].filter(Boolean).length;
  return candidate.length >= 48 || characterClasses >= 3;
}

function isSafeNonSecretIdentifier(
  value: unknown,
  maxLength: number,
): value is string {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > maxLength ||
    !/^[A-Za-z0-9][A-Za-z0-9._:-]*$/.test(value)
  ) {
    return false;
  }
  if (
    /^sk-/i.test(value) ||
    /^bearer(?:[-._:]|$)/i.test(value) ||
    /^(?:AIza|AKIA|ASIA|gh[pousr]_|github_pat_|xox[bpar]-|xapp-|(?:sk|pk|rk)_(?:live|test)_|whsec_)/i
      .test(value) ||
    /^[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}$/.test(
      value,
    ) ||
    (value.length >= 32 && /^[A-Za-z0-9]+$/.test(value)) ||
    (value.length >= 48 && /^[A-Za-z0-9_-]+$/.test(value))
  ) {
    return false;
  }
  return true;
}

function isDateString(value: unknown): value is string {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) {
    return false;
  }
  const [year, month, day] = value.split("-").map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day;
}

function isTimezone(value: unknown): value is string {
  if (!isRequiredText(value, 100)) {
    return false;
  }
  try {
    new Intl.DateTimeFormat("en", { timeZone: value }).format();
    return true;
  } catch {
    return false;
  }
}

function dateInTimezone(value: Date, timezone: string): string {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(value);
  const values = Object.fromEntries(
    parts.map((part) => [part.type, part.value]),
  );
  return `${values.year}-${values.month}-${values.day}`;
}

function parseOffsetTimestamp(value: unknown): Date | null {
  if (typeof value !== "string") {
    return null;
  }
  const match =
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-](\d{2}):(\d{2}))$/i
      .exec(value);
  if (match === null) {
    return null;
  }

  const [, year, month, day, hour, minute, second, offsetHour, offsetMinute] =
    match;
  if (
    !isDateString(`${year}-${month}-${day}`) ||
    Number(hour) > 23 ||
    Number(minute) > 59 ||
    Number(second) > 59 ||
    (offsetHour !== undefined && Number(offsetHour) > 23) ||
    (offsetMinute !== undefined && Number(offsetMinute) > 59)
  ) {
    return null;
  }

  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

export function validateIngestPayload(
  input: unknown,
): ValidationResult<LogUsageEvent> {
  if (!isRecord(input)) {
    return { ok: false, error: "payload must be a JSON object" };
  }
  if (!hasOnlyFields(input, ingestPayloadFields)) {
    return { ok: false, error: "payload contains unsupported fields" };
  }
  if (input.schema_version !== 1) {
    return { ok: false, error: "schema_version must be 1" };
  }

  if (!isSafeNonSecretIdentifier(input.event_id, 200)) {
    return {
      ok: false,
      error: "event_id must be a safe non-secret identifier",
    };
  }
  if (!isSafeNonSecretIdentifier(input.target_id, 200)) {
    return {
      ok: false,
      error: "target_id must be a safe non-secret identifier",
    };
  }
  if (!isRequiredText(input.object_key, 2048)) {
    return { ok: false, error: "object_key must be non-empty text" };
  }
  if (!isSafeObjectKey(input.object_key)) {
    return {
      ok: false,
      error: "object_key must be a safe relative object key",
    };
  }
  for (const field of ["archive_sha256", "manifest_sha256"] as const) {
    if (!isSha256(input[field])) {
      return {
        ok: false,
        error: `${field} must be a 64-character SHA-256 hex digest`,
      };
    }
  }
  const hourStart = parseOffsetTimestamp(input.hour_start);
  if (hourStart === null) {
    return {
      ok: false,
      error: "hour_start must be an ISO-8601 timestamp with an offset",
    };
  }
  if (!isTimezone(input.timezone)) {
    return { ok: false, error: "timezone must be a valid IANA timezone" };
  }
  if (!isDateString(input.usage_date)) {
    return { ok: false, error: "usage_date must be a valid YYYY-MM-DD date" };
  }
  if (
    dateInTimezone(hourStart, input.timezone) !== input.usage_date
  ) {
    return {
      ok: false,
      error: "usage_date must match hour_start in timezone",
    };
  }

  for (
    const field of [
      "source_count",
      "source_bytes",
      "jsonl_bytes",
      "compressed_bytes",
    ] as const
  ) {
    if (!isNonnegativeSafeInteger(input[field])) {
      return {
        ok: false,
        error: `${field} must be a nonnegative safe integer`,
      };
    }
  }
  if (
    Object.hasOwn(input, "test_mode") && typeof input.test_mode !== "boolean"
  ) {
    return { ok: false, error: "test_mode must be a boolean" };
  }
  if (!Array.isArray(input.usage)) {
    return { ok: false, error: "usage must be an array" };
  }

  const usage: LogUsageEntry[] = [];
  const pairs = new Set<string>();
  let sourceCount = 0;
  let sourceBytes = 0;
  let jsonlBytes = 0;
  for (const [index, rawEntry] of input.usage.entries()) {
    if (!isRecord(rawEntry)) {
      return { ok: false, error: `usage[${index}] must be an object` };
    }
    if (!hasOnlyFields(rawEntry, usageEntryFields)) {
      return {
        ok: false,
        error: "usage entries contain unsupported fields",
      };
    }
    if (!isRequiredText(rawEntry.key_name, 256)) {
      return {
        ok: false,
        error: `usage[${index}].key_name must be non-empty text`,
      };
    }
    if (isSecretLikeKeyName(rawEntry.key_name)) {
      return {
        ok: false,
        error: "key_name must be a display label, not a secret",
      };
    }
    if (
      typeof rawEntry.provider !== "string" ||
      !providers.includes(rawEntry.provider as Provider)
    ) {
      return {
        ok: false,
        error: `usage[${index}].provider must be codex, fable5, or grok45`,
      };
    }
    for (
      const field of ["source_count", "source_bytes", "jsonl_bytes"] as const
    ) {
      if (!isNonnegativeSafeInteger(rawEntry[field])) {
        return {
          ok: false,
          error: `usage[${index}].${field} must be a nonnegative safe integer`,
        };
      }
    }

    const pair = JSON.stringify([rawEntry.key_name, rawEntry.provider]);
    if (pairs.has(pair)) {
      return {
        ok: false,
        error: "usage entries must have unique key_name and provider pairs",
      };
    }
    pairs.add(pair);
    const entrySourceCount = rawEntry.source_count as number;
    const entrySourceBytes = rawEntry.source_bytes as number;
    const entryJSONLBytes = rawEntry.jsonl_bytes as number;
    sourceCount += entrySourceCount;
    sourceBytes += entrySourceBytes;
    jsonlBytes += entryJSONLBytes;
    if (![sourceCount, sourceBytes, jsonlBytes].every(Number.isSafeInteger)) {
      return { ok: false, error: "usage totals exceed the safe integer range" };
    }
    usage.push({
      key_name: rawEntry.key_name,
      provider: rawEntry.provider as Provider,
      source_count: entrySourceCount,
      source_bytes: entrySourceBytes,
      jsonl_bytes: entryJSONLBytes,
    });
  }

  if (
    sourceCount !== input.source_count ||
    sourceBytes !== input.source_bytes ||
    jsonlBytes !== input.jsonl_bytes
  ) {
    return { ok: false, error: "batch totals must equal usage totals" };
  }

  return {
    ok: true,
    value: {
      schema_version: 1,
      event_id: input.event_id as string,
      target_id: input.target_id as string,
      object_key: input.object_key as string,
      archive_sha256: input.archive_sha256 as string,
      manifest_sha256: input.manifest_sha256 as string,
      hour_start: input.hour_start as string,
      timezone: input.timezone,
      usage_date: input.usage_date,
      source_count: input.source_count,
      source_bytes: input.source_bytes,
      jsonl_bytes: input.jsonl_bytes,
      compressed_bytes: input.compressed_bytes as number,
      test_mode: typeof input.test_mode === "boolean" ? input.test_mode : false,
      usage,
    },
  };
}

export function readBearerToken(header: string | null): string | null {
  if (header === null) {
    return null;
  }
  const match = /^\s*Bearer\s+(.+?)\s*$/i.exec(header);
  const token = match?.[1]?.trim() ?? "";
  return token.length > 0 ? token : null;
}

async function sha256Bytes(value: Uint8Array): Promise<Uint8Array> {
  const bytes = new Uint8Array(value.byteLength);
  bytes.set(value);
  return new Uint8Array(
    await crypto.subtle.digest("SHA-256", bytes.buffer),
  );
}

export async function constantTimeEqual(
  left: string,
  right: string,
): Promise<boolean> {
  const encoder = new TextEncoder();
  const [leftDigest, rightDigest] = await Promise.all([
    sha256Bytes(encoder.encode(left)),
    sha256Bytes(encoder.encode(right)),
  ]);
  let difference = 0;
  for (let index = 0; index < leftDigest.length; index += 1) {
    difference |= leftDigest[index] ^ rightDigest[index];
  }
  return difference === 0;
}

export async function sha256Hex(value: Uint8Array): Promise<string> {
  const digest = await sha256Bytes(value);
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join(
    "",
  );
}

function parsePositiveInteger(
  raw: string | null,
  fallback: number,
  minimum: number,
  maximum: number,
): number | null {
  if (raw === null || raw === "") {
    return fallback;
  }
  if (!/^\d+$/.test(raw)) {
    return null;
  }
  const parsed = Number(raw);
  return Number.isSafeInteger(parsed) && parsed >= minimum && parsed <= maximum
    ? parsed
    : null;
}

export function parseDailyQuery(url: URL): ValidationResult<DailyQuery> {
  const from = url.searchParams.get("from");
  const to = url.searchParams.get("to");
  if (!isDateString(from) || !isDateString(to)) {
    return { ok: false, error: "from and to must be valid YYYY-MM-DD dates" };
  }

  const fromTime = Date.parse(`${from}T00:00:00Z`);
  const toTime = Date.parse(`${to}T00:00:00Z`);
  if (fromTime > toTime) {
    return { ok: false, error: "from must not be after to" };
  }
  const inclusiveDays = Math.floor((toTime - fromTime) / 86_400_000) + 1;
  if (inclusiveDays > 366) {
    return { ok: false, error: "date range must not exceed 366 days" };
  }

  const page = parsePositiveInteger(
    url.searchParams.get("page"),
    1,
    1,
    MAX_PAGE,
  );
  if (page === null) {
    return {
      ok: false,
      error: `page must be an integer from 1 to ${MAX_PAGE}`,
    };
  }
  const pageSize = parsePositiveInteger(
    url.searchParams.get("page_size"),
    20,
    1,
    20,
  );
  if (pageSize === null) {
    return { ok: false, error: "page_size must be an integer from 1 to 20" };
  }

  const search = (url.searchParams.get("search") ?? "").trim();
  if (search.length > 100) {
    return { ok: false, error: "search must not exceed 100 characters" };
  }

  return { ok: true, value: { from, to, search, page, pageSize } };
}
