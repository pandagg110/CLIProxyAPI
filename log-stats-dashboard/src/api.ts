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
  return value === null || (typeof value === "string" && value.length > 0 && !Number.isNaN(Date.parse(value)));
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((entry) => typeof entry === "string");
}

function isCell(value: unknown): value is DailyUsageCell {
  if (!isRecord(value)) return false;
  return isDateString(value.date) &&
    typeof value.key_name === "string" && value.key_name.length > 0 &&
    ["jsonl_bytes", "gpt_bytes", "claude_bytes", "grok_bytes"].every(
      (field) => typeof value[field] === "string" && DECIMAL.test(value[field]),
    ) && isInteger(value.batch_count);
}

export function validateDailyResponse(value: unknown): DailyUsageResponse | null {
  if (!isRecord(value) || !isTimezone(value.timezone) ||
    !isDateString(value.from) || !isDateString(value.to) ||
    typeof value.using_test_data !== "boolean" ||
    !isStringArray(value.names) || !isStringArray(value.days) ||
    !Array.isArray(value.cells) || !value.cells.every(isCell) ||
    !isTimestampOrNull(value.latest_sync_at) ||
    !isRecord(value.pagination) || !isInteger(value.pagination.page, 1) ||
    value.pagination.page_size !== 5 || !isInteger(value.pagination.total)) {
    return null;
  }
  if (!value.days.every(isDateString)) return null;
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
  const data = validateDailyResponse(raw);
  if (data === null) throw new Error("invalid response");
  return data;
}
