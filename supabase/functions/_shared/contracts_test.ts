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
    event_id: "2026-08-01T01:00:00-0800-codex-001",
    target_id: "tos-primary",
    object_key: "logs/2026/08/01/01-codex.jsonl.zst",
    archive_sha256: "a".repeat(64),
    manifest_sha256: "b".repeat(64),
    hour_start: "2026-08-01T01:00:00+08:00",
    timezone: "Asia/Shanghai",
    usage_date: "2026-08-01",
    source_count: 3,
    source_bytes: 300,
    jsonl_bytes: 360,
    compressed_bytes: 120,
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

Deno.test("validateIngestPayload accepts the exact external schema and defaults test_mode", () => {
  const result = validateIngestPayload(validEvent());

  assert.equal(result.ok, true);
  if (result.ok) {
    const value = result.value as unknown as Record<string, unknown>;
    assert.equal(value.test_mode, false);
    assert.equal(value.usage_precision, "exact");
    assert.equal(result.value.usage[0].key_name, " 张三 ");
    assert.deepEqual(Object.keys(value).sort(), [
      "archive_sha256",
      "compressed_bytes",
      "event_id",
      "hour_start",
      "jsonl_bytes",
      "manifest_sha256",
      "object_key",
      "schema_version",
      "source_bytes",
      "source_count",
      "target_id",
      "test_mode",
      "timezone",
      "usage",
      "usage_date",
      "usage_precision",
    ]);
  }
});

Deno.test("validateIngestPayload accepts batch-only history and normalizes usage JSONL to null", () => {
  const event = validEvent();
  const historyEvent = {
    ...event,
    usage_precision: "batch_only",
    usage: event.usage.map(({ jsonl_bytes: _jsonlBytes, ...entry }) => entry),
  };

  const result = validateIngestPayload(historyEvent);

  assert.equal(result.ok, true);
  if (result.ok) {
    assert.equal(result.value.usage_precision, "batch_only");
    assert.deepEqual(
      result.value.usage.map((entry) => entry.jsonl_bytes),
      [null, null],
    );
  }
});

Deno.test("validateIngestPayload enforces JSONL precision rules", () => {
  const event = validEvent();
  const { jsonl_bytes: _jsonlBytes, ...withoutJSONL } = event.usage[0];
  const exactWithoutJSONL = {
    ...event,
    usage: [withoutJSONL, event.usage[1]],
  };
  const batchOnlyWithJSONL = {
    ...event,
    usage_precision: "batch_only",
  };

  assert.deepEqual(validateIngestPayload(exactWithoutJSONL), {
    ok: false,
    error: "usage[0].jsonl_bytes must be a nonnegative safe integer",
  });
  assert.deepEqual(validateIngestPayload(batchOnlyWithJSONL), {
    ok: false,
    error: "batch_only usage jsonl_bytes must be null or omitted",
  });
  assert.deepEqual(
    validateIngestPayload({ ...event, usage_precision: "estimated" }),
    { ok: false, error: "usage_precision must be exact or batch_only" },
  );
});

Deno.test("validateIngestPayload accepts an explicit test_mode", () => {
  const event = { ...validEvent(), test_mode: true };

  const result = validateIngestPayload(event);

  assert.equal(result.ok, true);
  if (result.ok) {
    assert.equal(
      (result.value as unknown as Record<string, unknown>).test_mode,
      true,
    );
  }
});

Deno.test("validateIngestPayload rejects unknown top-level fields generically", () => {
  const secretValue = "do-not-echo-this-secret";
  const cases = [
    { ...validEvent(), raw_api_key: secretValue },
    { ...validEvent(), is_test: true },
  ];

  for (const event of cases) {
    const result = validateIngestPayload(event);

    assert.deepEqual(result, {
      ok: false,
      error: "payload contains unsupported fields",
    });
    assert.doesNotMatch(JSON.stringify(result), /raw_api_key|is_test/);
    assert.doesNotMatch(JSON.stringify(result), new RegExp(secretValue));
  }
});

Deno.test("validateIngestPayload rejects unknown usage fields generically", () => {
  const event = validEvent();
  const secretValue = "do-not-echo-this-secret";
  event.usage[0] = {
    ...event.usage[0],
    access_token: secretValue,
  } as typeof event.usage[0];

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "usage entries contain unsupported fields",
  });
  assert.doesNotMatch(JSON.stringify(result), /access_token/);
  assert.doesNotMatch(JSON.stringify(result), new RegExp(secretValue));
});

Deno.test("validateIngestPayload accepts ordinary relative object keys", () => {
  const event = validEvent();
  event.object_key = "logs/2026/08/file.jsonl.zst";

  assert.equal(validateIngestPayload(event).ok, true);
});

Deno.test("validateIngestPayload rejects unsafe event identifiers without echoing them", () => {
  const rejectedIdentifiers = [
    "sk-proj-abcdefghijklmnopqrstuvwxyz012345",
    `AIza${"A".repeat(35)}`,
    `AKIA${"A".repeat(16)}`,
    "A1b2".repeat(10),
    "A1b2_".repeat(10),
    `ghp_${"A".repeat(36)}`,
    `xoxb-${"A".repeat(32)}`,
    `sk_live_${"A".repeat(32)}`,
    "Bearer-secret-material",
    "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_value",
    "https://example.test/events/123",
    "C:\\logs\\event.jsonl",
    "event id with spaces",
    '{"level":"info","message":"raw log body"}',
  ];

  for (const eventID of rejectedIdentifiers) {
    const event = validEvent();
    event.event_id = eventID;

    const result = validateIngestPayload(event);

    assert.deepEqual(result, {
      ok: false,
      error: "event_id must be a safe non-secret identifier",
    });
    assert.doesNotMatch(
      JSON.stringify(result),
      new RegExp(eventID.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
  }
});

Deno.test("validateIngestPayload rejects unsafe target identifiers without echoing them", () => {
  const rejectedIdentifiers = [
    `AIza${"A".repeat(35)}`,
    `ASIA${"A".repeat(16)}`,
    "Z9y8".repeat(10),
    "Z9y8-".repeat(10),
    `github_pat_${"A".repeat(32)}`,
    `xapp-${"A".repeat(32)}`,
    `rk_test_${"A".repeat(32)}`,
    "https://bucket.example/logs?X-Tos-Signature=secret",
    "C:\\logs\\target",
    "\\\\server\\share\\target",
    "sk-proj-abcdefghijklmnopqrstuvwxyz012345",
  ];

  for (const targetID of rejectedIdentifiers) {
    const event = validEvent();
    event.target_id = targetID;

    const result = validateIngestPayload(event);

    assert.deepEqual(result, {
      ok: false,
      error: "target_id must be a safe non-secret identifier",
    });
    assert.doesNotMatch(
      JSON.stringify(result),
      new RegExp(targetID.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
  }
});

Deno.test("validateIngestPayload accepts normal slugs and UUID identifiers", () => {
  const event = validEvent();
  event.event_id = "550e8400-e29b-41d4-a716-446655440000";
  event.target_id = "production-target-01";

  assert.equal(validateIngestPayload(event).ok, true);
});

Deno.test("validateIngestPayload rejects impossible hour_start calendar dates", () => {
  const event = validEvent();
  event.hour_start = "2026-02-30T00:00:00Z";
  event.usage_date = "2026-03-02";

  assert.deepEqual(validateIngestPayload(event), {
    ok: false,
    error: "hour_start must be an ISO-8601 timestamp with an offset",
  });
});

Deno.test("validateIngestPayload rejects unsafe object keys without echoing them", () => {
  const unsafeKeys = [
    "https://bucket.example/log.jsonl.zst?X-Tos-Signature=secret",
    "s3://bucket/log.jsonl.zst",
    "file:///tmp/log.jsonl.zst",
    "/var/log/private.jsonl.zst",
    "C:\\logs\\private.jsonl.zst",
    "\\\\server\\share\\private.jsonl.zst",
    "logs/private.jsonl.zst?signature=secret",
    "logs/private.jsonl.zst#fragment",
    "logs/../private.jsonl.zst",
    "../private.jsonl.zst",
  ];

  for (const objectKey of unsafeKeys) {
    const event = validEvent();
    event.object_key = objectKey;

    const result = validateIngestPayload(event);

    assert.deepEqual(result, {
      ok: false,
      error: "object_key must be a safe relative object key",
    });
    assert.doesNotMatch(
      JSON.stringify(result),
      new RegExp(objectKey.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
  }
});

Deno.test("validateIngestPayload rejects secret-like key names without echoing them", () => {
  const secretLikeNames = [
    "sk-proj-abcdefghijklmnopqrstuvwxyz012345",
    "Bearer abcdefghijklmnopqrstuvwxyz012345",
    "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature_value",
    "A1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q7r8S9t0",
  ];

  for (const keyName of secretLikeNames) {
    const event = validEvent();
    event.usage[0].key_name = keyName;

    const result = validateIngestPayload(event);

    assert.deepEqual(result, {
      ok: false,
      error: "key_name must be a display label, not a secret",
    });
    assert.doesNotMatch(
      JSON.stringify(result),
      new RegExp(keyName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")),
    );
  }
});

Deno.test("validateIngestPayload applies the 48-code-point name limit", () => {
  const accepted = validEvent();
  accepted.usage[0].key_name = "😀".repeat(48);
  assert.equal(validateIngestPayload(accepted).ok, true);

  const rejectedName = "😀".repeat(49);
  const rejected = validEvent();
  rejected.usage[0].key_name = rejectedName;
  const result = validateIngestPayload(rejected);

  assert.deepEqual(result, {
    ok: false,
    error: "key_name must contain from 1 to 48 characters",
  });
  assert.doesNotMatch(JSON.stringify(result), new RegExp(rejectedName));
});

Deno.test("validateIngestPayload rejects trimmed cpa_ names case-insensitively", () => {
  const rejectedName = "  CpA_private-name  ";
  const event = validEvent();
  event.usage[0].key_name = rejectedName;

  const result = validateIngestPayload(event);

  assert.deepEqual(result, {
    ok: false,
    error: "key_name must be a display label, not a secret",
  });
  assert.doesNotMatch(JSON.stringify(result), new RegExp(rejectedName));
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
    await sha256Hex(new TextEncoder().encode('{"schema_version":1}\n')),
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

Deno.test("parseDailyQuery accepts the maximum PostgreSQL integer page", () => {
  const result = parseDailyQuery(
    new URL(
      "https://example.test/?api=daily&from=2026-03-01&to=2026-03-02&page=2147483647",
    ),
  );

  assert.equal(result.ok, true);
  if (result.ok) {
    assert.equal(result.value.page, 2_147_483_647);
  }
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
      "page must be an integer from 1 to 2147483647",
    ],
    [
      "from=2026-03-01&to=2026-03-02&page=2147483648",
      "page must be an integer from 1 to 2147483647",
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
