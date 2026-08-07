import { expect, test } from "vitest";
import { deriveViewState } from "../src/view-state";

test("reports initial loading without stale data", () => {
  expect(deriveViewState({ loading: true, hasData: false, error: false, online: true, empty: false, usingTestData: false })).toEqual({
    loading: true,
    stale: false,
    error: false,
    offline: false,
    empty: false,
    testData: false,
  });
});

test("keeps stale data visible after a refresh error", () => {
  expect(deriveViewState({ loading: false, hasData: true, error: true, online: true, empty: false, usingTestData: false }).stale).toBe(true);
});

test("marks offline, empty, error, and test-data states independently", () => {
  expect(deriveViewState({ loading: false, hasData: false, error: true, online: false, empty: true, usingTestData: true })).toEqual({
    loading: false,
    stale: false,
    error: true,
    offline: true,
    empty: true,
    testData: true,
  });
});
