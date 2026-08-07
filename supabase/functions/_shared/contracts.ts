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
  target: string;
  object_key: string;
  archive_sha256: string;
  manifest_sha256: string;
  hour: string;
  timezone: string;
  usage_date: string;
  source_count: number;
  source_bytes: number;
  jsonl_bytes: number;
  compressed_bytes: number;
  is_test: boolean;
  usage: LogUsageEntry[];
}

export interface IngestRpcResponse {
  status: "inserted" | "duplicate";
  event_id: string;
}

export interface DailyUsageCell {
  date: string;
  name: string;
  gpt_bytes: number;
  claude_bytes: number;
  grok_bytes: number;
}

export interface PublicDailyUsageResponse {
  timezone: string;
  from: string;
  to: string;
  using_test_data: boolean;
  total_names: number;
  names: string[];
  days: string[];
  cells: DailyUsageCell[];
  last_synced_at: string | null;
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

export function validateIngestPayload(
  input: unknown,
): ValidationResult<LogUsageEvent> {
  if (!isRecord(input)) {
    return { ok: false, error: "payload must be a JSON object" };
  }
  if (input.schema_version !== 1) {
    return { ok: false, error: "schema_version must be 1" };
  }

  const textFields = [
    ["event_id", 200],
    ["target", 200],
    ["object_key", 2048],
  ] as const;
  for (const [field, maxLength] of textFields) {
    if (!isRequiredText(input[field], maxLength)) {
      return { ok: false, error: `${field} must be non-empty text` };
    }
  }
  for (const field of ["archive_sha256", "manifest_sha256"] as const) {
    if (!isSha256(input[field])) {
      return {
        ok: false,
        error: `${field} must be a 64-character SHA-256 hex digest`,
      };
    }
  }
  if (
    typeof input.hour !== "string" ||
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/i
      .test(input.hour) ||
    Number.isNaN(Date.parse(input.hour))
  ) {
    return {
      ok: false,
      error: "hour must be an ISO-8601 timestamp with an offset",
    };
  }
  if (!isTimezone(input.timezone)) {
    return { ok: false, error: "timezone must be a valid IANA timezone" };
  }
  if (!isDateString(input.usage_date)) {
    return { ok: false, error: "usage_date must be a valid YYYY-MM-DD date" };
  }
  if (
    dateInTimezone(new Date(input.hour), input.timezone) !== input.usage_date
  ) {
    return { ok: false, error: "usage_date must match hour in timezone" };
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
  if (typeof input.is_test !== "boolean") {
    return { ok: false, error: "is_test must be a boolean" };
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
    if (!isRequiredText(rawEntry.key_name, 256)) {
      return {
        ok: false,
        error: `usage[${index}].key_name must be non-empty text`,
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

  return { ok: true, value: { ...input, usage } as unknown as LogUsageEvent };
}

export function readBearerToken(header: string | null): string | null {
  if (header === null) {
    return null;
  }
  const match = /^\s*Bearer\s+(.+?)\s*$/i.exec(header);
  const token = match?.[1]?.trim() ?? "";
  return token.length > 0 ? token : null;
}

async function sha256Bytes(value: string): Promise<Uint8Array> {
  return new Uint8Array(
    await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value)),
  );
}

export async function constantTimeEqual(
  left: string,
  right: string,
): Promise<boolean> {
  const [leftDigest, rightDigest] = await Promise.all([
    sha256Bytes(left),
    sha256Bytes(right),
  ]);
  let difference = 0;
  for (let index = 0; index < leftDigest.length; index += 1) {
    difference |= leftDigest[index] ^ rightDigest[index];
  }
  return difference === 0;
}

export async function sha256Hex(value: string): Promise<string> {
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
    Number.MAX_SAFE_INTEGER,
  );
  if (page === null) {
    return {
      ok: false,
      error: "page must be an integer greater than or equal to 1",
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
