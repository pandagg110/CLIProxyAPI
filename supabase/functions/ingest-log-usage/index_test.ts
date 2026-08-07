import assert from "node:assert/strict";

import { createIngestHandler } from "./index.ts";

function validEvent() {
  return {
    schema_version: 1,
    event_id: "edge-test-event",
    target_id: "tos-primary",
    object_key: "logs/edge-test.jsonl.zst",
    archive_sha256: "a".repeat(64),
    manifest_sha256: "b".repeat(64),
    hour_start: "2026-08-01T01:00:00+08:00",
    timezone: "Asia/Shanghai",
    usage_date: "2026-08-01",
    source_count: 1,
    source_bytes: 100,
    jsonl_bytes: 120,
    compressed_bytes: 40,
    usage: [{
      key_name: "张三",
      provider: "codex",
      source_count: 1,
      source_bytes: 100,
      jsonl_bytes: 120,
    }],
  };
}

function env(name: string): string | undefined {
  return {
    LOG_STATS_INGEST_TOKEN: "expected-token",
    SUPABASE_URL: "https://project.supabase.co",
    SUPABASE_SERVICE_ROLE_KEY: "service-role-key",
  }[name];
}

function request(body: BodyInit, token = "expected-token"): Request {
  return new Request("https://edge.test/ingest-log-usage", {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
    },
    body,
  });
}

async function sha256HexForBytes(value: Uint8Array): Promise<string> {
  const bytes = new Uint8Array(value.byteLength);
  bytes.set(value);
  const digest = new Uint8Array(
    await crypto.subtle.digest("SHA-256", bytes.buffer),
  );
  return Array.from(digest, (byte) => byte.toString(16).padStart(2, "0")).join(
    "",
  );
}

Deno.test("ingest handler answers CORS preflight without invoking RPC", async () => {
  let calls = 0;
  const handler = createIngestHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });

  const response = await handler(
    new Request("https://edge.test/", { method: "OPTIONS" }),
  );

  assert.equal(response.status, 204);
  assert.equal(response.headers.get("access-control-allow-origin"), "*");
  assert.equal(calls, 0);
});

Deno.test("ingest handler rejects missing and wrong tokens without invoking RPC", async () => {
  let calls = 0;
  const handler = createIngestHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });
  const body = JSON.stringify(validEvent());
  const missing = new Request("https://edge.test/", { method: "POST", body });

  const missingResponse = await handler(missing);
  const wrongResponse = await handler(request(body, "wrong-token"));

  assert.equal(missingResponse.status, 401);
  assert.deepEqual(await missingResponse.json(), { error: "unauthorized" });
  assert.equal(wrongResponse.status, 401);
  assert.deepEqual(await wrongResponse.json(), { error: "unauthorized" });
  assert.equal(calls, 0);
});

Deno.test("ingest handler rejects invalid JSON with status 400", async () => {
  let calls = 0;
  const handler = createIngestHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });

  const invalidBody = '{"secret":"do-not-echo"';
  const response = await handler(request(invalidBody));
  const responseBody = await response.text();

  assert.equal(response.status, 400);
  assert.deepEqual(JSON.parse(responseBody), { error: "invalid_json" });
  assert.doesNotMatch(responseBody, /do-not-echo/);
  assert.equal(calls, 0);
});

Deno.test("ingest handler rejects invalid UTF-8 without leaking the body", async () => {
  let calls = 0;
  const handler = createIngestHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });
  const prefix = new TextEncoder().encode('{"secret":"do-not-echo');
  const invalidBody = new Uint8Array(prefix.length + 1);
  invalidBody.set(prefix);
  invalidBody[invalidBody.length - 1] = 0xff;

  const response = await handler(request(invalidBody));
  const responseBody = await response.text();

  assert.equal(response.status, 400);
  assert.deepEqual(JSON.parse(responseBody), { error: "invalid_utf8" });
  assert.doesNotMatch(responseBody, /do-not-echo/);
  assert.equal(calls, 0);
});

Deno.test("ingest handler rejects unsupported and sensitive fields before RPC", async () => {
  let calls = 0;
  const handler = createIngestHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });
  const rejectedValue = "sk-proj-do-not-echo-this-value";
  const invalidPayloads = [
    { ...validEvent(), is_test: false },
    { ...validEvent(), raw_api_key: rejectedValue },
    {
      ...validEvent(),
      object_key: `https://example.test/log?token=${rejectedValue}`,
    },
    {
      ...validEvent(),
      usage: [{ ...validEvent().usage[0], key_name: rejectedValue }],
    },
    { ...validEvent(), event_id: rejectedValue },
    {
      ...validEvent(),
      target_id: `https://example.test/log?X-Tos-Signature=${rejectedValue}`,
    },
    { ...validEvent(), target_id: `C:\\logs\\${rejectedValue}` },
  ];

  for (const payload of invalidPayloads) {
    const response = await handler(request(JSON.stringify(payload)));
    const responseBody = await response.text();

    assert.equal(response.status, 422);
    assert.doesNotMatch(responseBody, new RegExp(rejectedValue));
  }
  assert.equal(calls, 0);
});

Deno.test("ingest handler hashes the exact body and calls the service-role RPC", async () => {
  const body = `${JSON.stringify(validEvent())}\n`;
  const bodyBytes = new TextEncoder().encode(body);
  let captured: unknown;
  const handler = createIngestHandler({
    env,
    rpc: (call) => {
      captured = call;
      return Promise.resolve({
        data: { status: "inserted", event_id: "edge-test-event" },
        error: null,
      });
    },
  });

  const response = await handler(request(bodyBytes));

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    status: "inserted",
    event_id: "edge-test-event",
  });
  assert.deepEqual(captured, {
    url: "https://project.supabase.co",
    key: "service-role-key",
    functionName: "ingest_log_usage_v1",
    args: {
      payload: { ...validEvent(), test_mode: false },
      payload_sha256: await sha256HexForBytes(bodyBytes),
    },
  });
});

Deno.test("ingest handler hashes UTF-8 BOM bytes instead of decoded text", async () => {
  const jsonBytes = new TextEncoder().encode(JSON.stringify(validEvent()));
  const bodyBytes = new Uint8Array(jsonBytes.length + 3);
  bodyBytes.set([0xef, 0xbb, 0xbf]);
  bodyBytes.set(jsonBytes, 3);
  const decodedBytes = new TextEncoder().encode(
    new TextDecoder().decode(bodyBytes),
  );
  let captured: unknown;
  const handler = createIngestHandler({
    env,
    rpc: (call) => {
      captured = call;
      return Promise.resolve({
        data: { status: "inserted", event_id: "edge-test-event" },
        error: null,
      });
    },
  });

  const response = await handler(request(bodyBytes));
  const rawDigest = await sha256HexForBytes(bodyBytes);
  const decodedDigest = await sha256HexForBytes(decodedBytes);

  assert.equal(response.status, 200);
  assert.notEqual(rawDigest, decodedDigest);
  assert.deepEqual(captured, {
    url: "https://project.supabase.co",
    key: "service-role-key",
    functionName: "ingest_log_usage_v1",
    args: {
      payload: { ...validEvent(), test_mode: false },
      payload_sha256: rawDigest,
    },
  });
});

Deno.test("ingest handler returns duplicate RPC results unchanged", async () => {
  const handler = createIngestHandler({
    env,
    rpc: () =>
      Promise.resolve({
        data: { status: "duplicate", event_id: "edge-test-event" },
        error: null,
      }),
  });

  const response = await handler(request(JSON.stringify(validEvent())));

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    status: "duplicate",
    event_id: "edge-test-event",
  });
});

Deno.test("ingest handler maps event ID conflicts to status 409", async () => {
  const handler = createIngestHandler({
    env,
    rpc: () =>
      Promise.resolve({
        data: null,
        error: { code: "P0001", message: "event_id_conflict" },
      }),
  });

  const response = await handler(request(JSON.stringify(validEvent())));

  assert.equal(response.status, 409);
  assert.deepEqual(await response.json(), { error: "event_id_conflict" });
});

Deno.test("ingest handler maps database validation errors to status 422", async () => {
  const handler = createIngestHandler({
    env,
    rpc: () =>
      Promise.resolve({
        data: null,
        error: { code: "22023", message: "validation_error: invalid timezone" },
      }),
  });

  const response = await handler(request(JSON.stringify(validEvent())));

  assert.equal(response.status, 422);
  assert.deepEqual(await response.json(), {
    error: "validation_error",
    message: "payload failed database validation",
  });
});

Deno.test("ingest handler allows POST only", async () => {
  const handler = createIngestHandler({
    env,
    rpc: () => Promise.resolve({ data: null, error: null }),
  });

  const response = await handler(
    new Request("https://edge.test/", { method: "GET" }),
  );

  assert.equal(response.status, 405);
  assert.equal(response.headers.get("allow"), "POST, OPTIONS");
});
