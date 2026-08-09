import { describe, expect, test } from "vitest";
import {
  buildMatrix,
  formatDecimalBytes,
  providerBreakdown,
} from "../src/domain";

describe("decimal byte handling", () => {
  test("formats values larger than 2^53 without Number conversion", () => {
    expect(formatDecimalBytes("9007199254740993")).toBe(
      "9,007,199,254,740,993 B",
    );
  });

  test("keeps explicit zero visible", () => {
    expect(formatDecimalBytes("0")).toBe("0 B");
  });
});

describe("matrix", () => {
  test("distinguishes a missing cell from an explicit zero record", () => {
    const zeroCell = {
      date: "2026-08-08",
      key_name: "运营一组",
      source_bytes: "0",
      gpt_source_bytes: "0",
      claude_source_bytes: "0",
      grok_source_bytes: "0",
      usage_precision: "exact" as const,
      jsonl_bytes: "0",
      gpt_bytes: "0",
      claude_bytes: "0",
      grok_bytes: "0",
      batch_count: 1,
    };
    const matrix = buildMatrix([zeroCell]);

    expect(matrix.get("2026-08-08", "运营一组")).toEqual(zeroCell);
    expect(matrix.get("2026-08-07", "运营一组")).toBeUndefined();
  });
});

test("maps provider byte fields to user-facing provider labels", () => {
  const result = providerBreakdown({
    date: "2026-08-08",
    key_name: "运营一组",
    source_bytes: "12",
    gpt_source_bytes: "3",
    claude_source_bytes: "4",
    grok_source_bytes: "5",
    usage_precision: "batch_only",
    jsonl_bytes: null,
    gpt_bytes: null,
    claude_bytes: null,
    grok_bytes: null,
    batch_count: 2,
  });

  expect(result).toEqual([
    { label: "GPT", bytes: "3" },
    { label: "Claude", bytes: "4" },
    { label: "Grok", bytes: "5" },
  ]);
});
