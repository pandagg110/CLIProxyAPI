import { describe, expect, test } from "vitest";
import {
  encodeDashboardQuery,
  parseDashboardQuery,
  quickRange,
} from "../src/url-state";

const TODAY = "2026-08-08";

describe("quick ranges", () => {
  test("computes a local seven-day range including today", () => {
    expect(quickRange(7, TODAY)).toEqual({
      from: "2026-08-02",
      to: "2026-08-08",
    });
  });

  test("computes a local thirty-day range including today", () => {
    expect(quickRange(30, TODAY)).toEqual({
      from: "2026-07-10",
      to: "2026-08-08",
    });
  });
});

describe("URL state codec", () => {
  test("round-trips custom dates, search, and page for refresh/back state", () => {
    const query = {
      from: "2026-01-01",
      to: "2026-03-31",
      search: "运营 A",
      page: 3,
    };
    const encoded = encodeDashboardQuery(query);

    expect(parseDashboardQuery(`?${encoded}`, TODAY)).toEqual(query);
  });

  test("normalizes missing state to the seven-day first page", () => {
    expect(parseDashboardQuery("", TODAY)).toEqual({
      from: "2026-08-02",
      to: "2026-08-08",
      search: "",
      page: 1,
    });
  });

  test("accepts an inclusive 366-day range", () => {
    expect(
      parseDashboardQuery(
        "?from=2025-08-08&to=2026-08-08&search=&page=1",
        TODAY,
      ),
    ).toMatchObject({ from: "2025-08-08", to: "2026-08-08" });
  });

  test.each([
    "?from=2026-02-30&to=2026-08-08&page=1",
    "?from=2026-08-09&to=2026-08-08&page=1",
    "?from=2025-08-07&to=2026-08-08&page=1",
    "?from=2026-08-02&to=2026-08-08&page=0",
    "?from=2026-08-02&to=2026-08-08&page=2147483648",
  ])("normalizes invalid dates, ranges, and pages: %s", (search) => {
    expect(parseDashboardQuery(search, TODAY)).toEqual({
      from: "2026-08-02",
      to: "2026-08-08",
      search: "",
      page: 1,
    });
  });
});
