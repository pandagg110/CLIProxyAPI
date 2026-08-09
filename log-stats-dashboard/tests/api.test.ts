import { afterEach, describe, expect, test, vi } from "vitest";
import { fetchDailyUsage, validateDailyResponse } from "../src/api";
import type { DailyUsageResponse, DashboardQuery } from "../src/types";

const query: DashboardQuery = {
  from: "2026-08-02",
  to: "2026-08-08",
  search: "",
  page: 1,
};

const days = [
  "2026-08-02",
  "2026-08-03",
  "2026-08-04",
  "2026-08-05",
  "2026-08-06",
  "2026-08-07",
  "2026-08-08",
];

function validResponse(): DailyUsageResponse {
  return {
    metric_basis: "source_bytes",
    timezone: "Asia/Hong_Kong",
    from: query.from,
    to: query.to,
    using_test_data: false,
    pagination: { page: query.page, page_size: 5, total: 2 },
    names: ["运营一组", "零用量"],
    days,
    cells: [
      {
        date: "2026-08-08",
        key_name: "运营一组",
        source_bytes: "7007199254740993",
        gpt_source_bytes: "7007199254740000",
        claude_source_bytes: "993",
        grok_source_bytes: "0",
        usage_precision: "exact",
        jsonl_bytes: "9007199254740993",
        gpt_bytes: "9007199254740000",
        claude_bytes: "993",
        grok_bytes: "0",
        batch_count: 2,
      },
      {
        date: "2026-08-07",
        key_name: "零用量",
        source_bytes: "0",
        gpt_source_bytes: "0",
        claude_source_bytes: "0",
        grok_source_bytes: "0",
        usage_precision: "exact",
        jsonl_bytes: "0",
        gpt_bytes: "0",
        claude_bytes: "0",
        grok_bytes: "0",
        batch_count: 1,
      },
    ],
    latest_sync_at: "2026-08-08T03:00:00.123+08:00",
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("daily API response validation", () => {
  test("accepts missing cells, explicit zero, and decimal strings above 2^53", () => {
    const result = validateDailyResponse(validResponse(), query);

    expect(result?.cells).toHaveLength(2);
    expect(result?.metric_basis).toBe("source_bytes");
    expect(result?.cells[0].source_bytes).toBe("7007199254740993");
    expect(result?.cells[0].jsonl_bytes).toBe("9007199254740993");
    expect(result?.cells[1].jsonl_bytes).toBe("0");
  });

  test("accepts batch-only cells with exact source bytes and unknown per-name JSONL", () => {
    const response = validResponse();
    response.cells[0] = {
      ...response.cells[0],
      usage_precision: "batch_only",
      jsonl_bytes: null,
      gpt_bytes: null,
      claude_bytes: null,
      grok_bytes: null,
    };

    const result = validateDailyResponse(response, query);

    expect(result?.cells[0].source_bytes).toBe("7007199254740993");
    expect(result?.cells[0].jsonl_bytes).toBeNull();
  });

  test.each([
    ["response from differs from request", () => ({ ...validResponse(), from: "2026-08-03" })],
    ["response to differs from request", () => ({ ...validResponse(), to: "2026-08-07" })],
    ["response range is reversed", () => ({ ...validResponse(), from: "2026-08-09" })],
    ["pagination page differs from request", () => ({ ...validResponse(), pagination: { page: 2, page_size: 5, total: 2 } })],
    ["pagination page size differs from request", () => ({ ...validResponse(), pagination: { page: 1, page_size: 4, total: 2 } })],
    ["days are descending", () => ({ ...validResponse(), days: [...days].reverse() })],
    ["days contain a duplicate", () => ({ ...validResponse(), days: [...days.slice(0, 6), "2026-08-07"] })],
    ["days omit a date", () => ({ ...validResponse(), days: days.filter((day) => day !== "2026-08-04") })],
    ["names contain a duplicate", () => ({ ...validResponse(), names: ["运营一组", "运营一组"] })],
    ["names exceed page size", () => ({ ...validResponse(), names: ["一", "二", "三", "四", "五", "六"] })],
    ["cell date is outside days", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], date: "2026-08-09" }] })],
    ["cell name is outside current names", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], key_name: "其他" }] })],
    ["cell date and name pair is duplicated", () => ({ ...validResponse(), cells: [validResponse().cells[0], validResponse().cells[0]] })],
    ["provider source bytes do not sum to source bytes", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], grok_source_bytes: "1" }] })],
    ["exact JSONL provider bytes do not sum to JSONL bytes", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], grok_bytes: "1" }] })],
    ["batch-only precision exposes JSONL bytes", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], usage_precision: "batch_only" }] })],
    ["exact precision omits JSONL bytes", () => ({ ...validResponse(), cells: [{ ...validResponse().cells[0], jsonl_bytes: null, gpt_bytes: null, claude_bytes: null, grok_bytes: null }] })],
    ["timestamp has no RFC3339 offset", () => ({ ...validResponse(), latest_sync_at: "2026-08-08T03:00:00" })],
    ["timestamp has an invalid calendar date", () => ({ ...validResponse(), latest_sync_at: "2026-02-30T03:00:00Z" })],
    ["timestamp has an invalid time", () => ({ ...validResponse(), latest_sync_at: "2026-08-08T24:00:00Z" })],
  ])("rejects when %s", (_name, buildResponse) => {
    expect(validateDailyResponse(buildResponse(), query)).toBeNull();
  });

  test("fetch rejects a structurally valid payload for a different query", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({
      ...validResponse(),
      from: "2026-08-03",
    }), { status: 200, headers: { "content-type": "application/json" } })));

    await expect(fetchDailyUsage(query)).rejects.toThrow("invalid response");
  });
});
