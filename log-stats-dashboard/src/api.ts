import type { DailyUsageCell, DailyUsageResponse, DashboardQuery } from "./types";

const DATE = /^\d{4}-\d{2}-\d{2}$/;
const DECIMAL = /^(?:0|[1-9]\d*)$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isInteger(value: unknown, minimum = 0): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum;
}

function isDateString(value: unknown): value is string {
  if (typeof value !== "string" || !DATE.test(value)) return false;
  const [year, month, day] = value.split("-").map(Number);
  const parsed = new Date(Date.UTC(year, month - 1, day));
  return parsed.getUTCFullYear() === year && parsed.getUTCMonth() === month - 1 && parsed.getUTCDate() === day;
}

function isTimezone(value: unknown): value is string {
  if (typeof value !== "string" || value.length === 0 || value.length > 100) return false;
  try {
    new Intl.DateTimeFormat("zh-CN", { timeZone: value }).format();
    return true;
  } catch {
    return false;
  }
}

function isTimestampOrNull(value: unknown): value is string | null {
  if (value === null) return true;
  if (typeof value !== "string") return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|[+-](\d{2}):(\d{2}))$/.exec(value);
  if (match === null) return false;
  const [, year, month, day, hour, minute, second, offsetHour, offsetMinute] = match;
  if (!isDateString(`${year}-${month}-${day}`) || Number(hour) > 23 || Number(minute) > 59 || Number(second) > 59) {
    return false;
  }
  if (offsetHour !== undefined && (Number(offsetHour) > 23 || Number(offsetMinute) > 59)) return false;
  return !Number.isNaN(Date.parse(value));
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((entry) => typeof entry === "string");
}

function isDecimalString(value: unknown): value is string {
  return typeof value === "string" && DECIMAL.test(value);
}

function isCell(value: unknown): value is DailyUsageCell {
  if (!isRecord(value)) return false;
  if (!isDateString(value.date)) return false;
  if (typeof value.key_name !== "string" || value.key_name.length === 0) return false;
  if (!["source_bytes", "gpt_source_bytes", "claude_source_bytes", "grok_source_bytes"].every(
    (field) => isDecimalString(value[field]),
  ) || !isInteger(value.batch_count)) return false;
  if (value.usage_precision === "exact") {
    return ["jsonl_bytes", "gpt_bytes", "claude_bytes", "grok_bytes"].every(
      (field) => isDecimalString(value[field]),
    );
  }
  if (value.usage_precision === "batch_only") {
    return ["jsonl_bytes", "gpt_bytes", "claude_bytes", "grok_bytes"].every(
      (field) => value[field] === null,
    );
  }
  return false;
}

function expectedDays(from: string, to: string): string[] {
  const result: string[] = [];
  for (let cursor = Date.parse(`${from}T00:00:00Z`); cursor <= Date.parse(`${to}T00:00:00Z`); cursor += 86_400_000) {
    result.push(new Date(cursor).toISOString().slice(0, 10));
  }
  return result;
}

export function validateDailyResponse(value: unknown, query: DashboardQuery): DailyUsageResponse | null {
  if (!isRecord(value) || value.metric_basis !== "source_bytes" || !isTimezone(value.timezone) ||
    !isDateString(value.from) || !isDateString(value.to) ||
    typeof value.using_test_data !== "boolean" ||
    !isStringArray(value.names) || !isStringArray(value.days) ||
    !Array.isArray(value.cells) || !value.cells.every(isCell) ||
    !isTimestampOrNull(value.latest_sync_at) ||
    !isRecord(value.pagination) || !isInteger(value.pagination.page, 1) ||
    value.pagination.page_size !== 5 || !isInteger(value.pagination.total)) {
    return null;
  }
  if (!isDateString(query.from) || !isDateString(query.to) || query.from > query.to ||
    value.from !== query.from || value.to !== query.to || value.from > value.to ||
    value.pagination.page !== query.page || value.pagination.page_size !== 5) {
    return null;
  }

  const requiredDays = expectedDays(query.from, query.to);
  if (value.days.length !== requiredDays.length ||
    !value.days.every((day, index) => isDateString(day) && day === requiredDays[index])) {
    return null;
  }
  if (value.names.length > 5 || value.names.some((name) => name.length === 0) ||
    new Set(value.names).size !== value.names.length) {
    return null;
  }

  const allowedDays = new Set(value.days);
  const allowedNames = new Set(value.names);
  const cellPairs = new Set<string>();
  for (const cell of value.cells) {
    if (!allowedDays.has(cell.date) || !allowedNames.has(cell.key_name)) return null;
    const pair = JSON.stringify([cell.date, cell.key_name]);
    if (cellPairs.has(pair)) return null;
    cellPairs.add(pair);
    if (BigInt(cell.gpt_source_bytes) + BigInt(cell.claude_source_bytes) + BigInt(cell.grok_source_bytes) !== BigInt(cell.source_bytes)) {
      return null;
    }
    if (cell.usage_precision === "exact" &&
      BigInt(cell.gpt_bytes!) + BigInt(cell.claude_bytes!) + BigInt(cell.grok_bytes!) !== BigInt(cell.jsonl_bytes!)) return null;
  }
  return value as unknown as DailyUsageResponse;
}

export async function fetchDailyUsage(query: DashboardQuery, signal?: AbortSignal): Promise<DailyUsageResponse> {
  const url = new URL(window.location.href);
  url.search = new URLSearchParams({
    api: "daily",
    from: query.from,
    to: query.to,
    search: query.search,
    page: String(query.page),
    page_size: "5",
  }).toString();
  const response = await fetch(url, { headers: { accept: "application/json" }, signal });
  if (!response.ok) throw new Error("request failed");
  let raw: unknown;
  try {
    raw = await response.json();
  } catch {
    throw new Error("invalid response");
  }
  const data = validateDailyResponse(raw, query);
  if (data === null) throw new Error("invalid response");
  return data;
}
