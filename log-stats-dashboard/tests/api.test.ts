import { describe, expect, test } from "vitest";
import { validateDailyResponse } from "../src/api";

const validResponse = {
  timezone: "Asia/Hong_Kong",
  from: "2026-08-02",
  to: "2026-08-08",
  using_test_data: false,
  pagination: { page: 1, page_size: 5, total: 1 },
  names: ["运营一组"],
  days: ["2026-08-08"],
  cells: [
    {
      date: "2026-08-08",
      key_name: "运营一组",
      jsonl_bytes: "9007199254740993",
      gpt_bytes: "9007199254740993",
      claude_bytes: "0",
      grok_bytes: "0",
      batch_count: 2,
    },
  ],
  latest_sync_at: "2026-08-08T03:00:00Z",
};

describe("daily API response validation", () => {
  test("accepts the public response contract and preserves decimal strings", () => {
    const result = validateDailyResponse(validResponse);
    expect(result?.cells[0].jsonl_bytes).toBe("9007199254740993");
  });

  test.each([
    { ...validResponse, timezone: 3 },
    { ...validResponse, pagination: { page: 1, page_size: 20, total: 1 } },
    {
      ...validResponse,
      cells: [{ ...validResponse.cells[0], jsonl_bytes: 9007199254740993 }],
    },
    {
      ...validResponse,
      cells: [{ ...validResponse.cells[0], claude_bytes: "-1" }],
    },
    { ...validResponse, latest_sync_at: 42 },
    { ...validResponse, timezone: "Not/A_Timezone" },
    { ...validResponse, from: "2026-02-30" },
    { ...validResponse, latest_sync_at: "not-a-timestamp" },
  ])("rejects malformed public data", (response) => {
    expect(validateDailyResponse(response)).toBeNull();
  });
});
