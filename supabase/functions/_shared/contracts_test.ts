import assert from "node:assert/strict";

import {
  constantTimeEqual,
  parseDailyQuery,
  readBearerToken,
  sha256Hex,
  validateIngestPayload,
} from "./contracts.ts";

function validEvent() {
  return {
    schema_version: 1,
    event_id: "2026-08-01T01:00:00+08:00-codex-001",
    target: "tos-primary",
    object_key: "logs/2026/08/01/01-codex.jsonl.zst",
    archive_sha256: "a".repeat(64),
    manifest_sha256: "b".repeat(64),
    hour: "2026-08-01T01:00:00+08:00",
    timezone: "Asia/Shanghai",
    usage_date: "2026-08-01",
    source_count: 3,
    source_bytes: 300,
    jsonl_bytes: 360,
    compressed_bytes: 120,
    is_test: false,
    usage: [
      {
        key_name: " 张三 ",
        provider: "codex",
        source_count: 1,
        source_bytes: 100,
        jsonl_bytes: 120,
      },
      {
        key_name: "李四",
        provider: "fable5",
        source_count: 2,
        source_bytes: 200,
        jsonl_bytes: 240,
      },
    ],
  };
}

Deno.test("validateIngestPayload accepts a valid event and preserves key_name exactly", () => {
  const result = validateIngestPayload(validEvent());

  assert.equal(result.ok, true);
  if (result.ok) {
    assert.equal(result.value.usage[0].key_name, " 张三 ");
  }
});

Deno.test("validateIngestPayload rejects duplicate key_name and provider pairs", () => {
  const event = validEvent();
  event.usage.push({ ...event.usage[0] });
  event.source_count += 1;
  event.source_bytes += 100;
  event.jsonl_bytes += 120;

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "usage entries must have unique key_name and provider pairs",
  });
});

Deno.test("validateIngestPayload rejects unsupported providers", () => {
  const event = validEvent();
  event.usage[0].provider = "openai";

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "usage[0].provider must be codex, fable5, or grok45",
  });
});

Deno.test("validateIngestPayload rejects totals that do not equal usage sums", () => {
  const event = validEvent();
  event.jsonl_bytes += 1;

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "batch totals must equal usage totals",
  });
});

Deno.test("validateIngestPayload rejects unsafe integer values", () => {
  const event = validEvent();
  event.source_bytes = Number.MAX_SAFE_INTEGER + 1;

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "source_bytes must be a nonnegative safe integer",
  });
});

Deno.test("readBearerToken parses only a non-empty Bearer token", () => {
  assert.equal(readBearerToken("Bearer secret-value"), "secret-value");
  assert.equal(readBearerToken("bearer secret-value"), "secret-value");
  assert.equal(readBearerToken("Basic secret-value"), null);
  assert.equal(readBearerToken("Bearer   "), null);
  assert.equal(readBearerToken(null), null);
});

Deno.test("constantTimeEqual compares tokens through fixed-length digests", async () => {
  assert.equal(await constantTimeEqual("secret-value", "secret-value"), true);
  assert.equal(await constantTimeEqual("secret-value", "wrong"), false);
  assert.equal(await constantTimeEqual("", ""), true);
});

Deno.test("sha256Hex hashes the exact raw body bytes", async () => {
  assert.equal(
    await sha256Hex('{"schema_version":1}\n'),
    "c5d130e87e377f65c0f77eda3629f01de137de88cd3b0a181aa1b6fda001afdc",
  );
});

Deno.test("parseDailyQuery accepts an inclusive 366-day range and defaults pagination", () => {
  const result = parseDailyQuery(
    new URL(
      "https://example.test/?api=daily&from=2025-01-01&to=2026-01-01&search=%E5%BC%A0%E4%B8%89",
    ),
  );

  assert.deepEqual(result, {
    ok: true,
    value: {
      from: "2025-01-01",
      to: "2026-01-01",
      search: "张三",
      page: 1,
      pageSize: 20,
    },
  });
});

Deno.test("parseDailyQuery rejects invalid dates, ranges, pages, and page sizes", () => {
  const cases = [
    [
      "from=2026-02-30&to=2026-03-01",
      "from and to must be valid YYYY-MM-DD dates",
    ],
    ["from=2026-03-02&to=2026-03-01", "from must not be after to"],
    ["from=2025-01-01&to=2026-01-02", "date range must not exceed 366 days"],
    [
      "from=2026-03-01&to=2026-03-02&page=0",
      "page must be an integer greater than or equal to 1",
    ],
    [
      "from=2026-03-01&to=2026-03-02&page_size=21",
      "page_size must be an integer from 1 to 20",
    ],
  ] as const;

  for (const [query, error] of cases) {
    assert.deepEqual(
      parseDailyQuery(new URL(`https://example.test/?api=daily&${query}`)),
      { ok: false, error },
    );
  }
});
