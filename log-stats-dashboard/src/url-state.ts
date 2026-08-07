import type { DashboardQuery } from "./types";

const DAY_MS = 86_400_000;
const MAX_PAGE = 2_147_483_647;

function parseDate(value: string | null): Date | null {
  if (value === null || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const [year, month, day] = value.split("-").map(Number);
  const parsed = new Date(Date.UTC(year, month - 1, day));
  return parsed.getUTCFullYear() === year &&
      parsed.getUTCMonth() === month - 1 &&
      parsed.getUTCDate() === day
    ? parsed
    : null;
}

function formatDate(value: Date): string {
  return value.toISOString().slice(0, 10);
}

export function localToday(now = new Date()): string {
  const year = now.getFullYear();
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}-${month}-${day}`;
}

export function quickRange(days: 7 | 30, today: string): Pick<DashboardQuery, "from" | "to"> {
  const end = parseDate(today);
  if (end === null) throw new Error("Invalid local date");
  return {
    from: formatDate(new Date(end.getTime() - (days - 1) * DAY_MS)),
    to: today,
  };
}

function defaultQuery(today: string): DashboardQuery {
  return { ...quickRange(7, today), search: "", page: 1 };
}

export function parseDashboardQuery(search: string, today: string): DashboardQuery {
  const fallback = defaultQuery(today);
  const params = new URLSearchParams(search);
  const fromRaw = params.get("from");
  const toRaw = params.get("to");
  const from = parseDate(fromRaw);
  const to = parseDate(toRaw);
  const pageRaw = params.get("page") ?? "1";
  const searchRaw = (params.get("search") ?? "").trim();
  if (from === null || to === null || from > to) return fallback;
  const inclusiveDays = Math.floor((to.getTime() - from.getTime()) / DAY_MS) + 1;
  if (inclusiveDays > 366 || !/^\d+$/.test(pageRaw)) return fallback;
  const page = Number(pageRaw);
  if (!Number.isSafeInteger(page) || page < 1 || page > MAX_PAGE || searchRaw.length > 100) return fallback;
  return { from: fromRaw!, to: toRaw!, search: searchRaw, page };
}

export function encodeDashboardQuery(query: DashboardQuery): string {
  const params = new URLSearchParams({
    from: query.from,
    to: query.to,
    search: query.search,
    page: String(query.page),
  });
  return params.toString();
}

export function validateCustomRange(from: string, to: string): string | null {
  const parsedFrom = parseDate(from);
  const parsedTo = parseDate(to);
  if (parsedFrom === null || parsedTo === null) return "请输入有效日期。";
  if (parsedFrom > parsedTo) return "开始日期不能晚于结束日期。";
  const inclusiveDays = Math.floor((parsedTo.getTime() - parsedFrom.getTime()) / DAY_MS) + 1;
  return inclusiveDays > 366 ? "日期范围不能超过 366 天。" : null;
}
