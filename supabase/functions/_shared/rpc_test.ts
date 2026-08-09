import assert from "node:assert/strict";

import { callSupabaseRpc } from "./rpc.ts";

Deno.test("callSupabaseRpc posts JSON to the named RPC with Supabase auth headers", async () => {
  let captured: { input?: string; init?: RequestInit } = {};
  const result = await callSupabaseRpc(
    {
      url: "https://project.supabase.co/",
      key: "anon-key",
      functionName: "get_public_daily_usage",
      args: { p_page: 1 },
    },
    (input, init) => {
      captured = { input: String(input), init };
      return Promise.resolve(Response.json({ names: [] }));
    },
  );

  assert.deepEqual(result, { data: { names: [] }, error: null });
  assert.equal(
    captured.input,
    "https://project.supabase.co/rest/v1/rpc/get_public_daily_usage",
  );
  assert.equal(captured.init?.method, "POST");
  assert.equal(new Headers(captured.init?.headers).get("apikey"), "anon-key");
  assert.equal(
    new Headers(captured.init?.headers).get("authorization"),
    "Bearer anon-key",
  );
  assert.equal(captured.init?.body, JSON.stringify({ p_page: 1 }));
});

Deno.test("callSupabaseRpc returns structured PostgREST errors", async () => {
  const result = await callSupabaseRpc(
    {
      url: "https://project.supabase.co",
      key: "service-key",
      functionName: "ingest_log_usage_v1",
      args: {},
    },
    () =>
      Promise.resolve(Response.json(
        { code: "22023", message: "validation_error: bad payload" },
        { status: 400 },
      )),
  );

  assert.deepEqual(result, {
    data: null,
    error: { code: "22023", message: "validation_error: bad payload" },
  });
});

Deno.test("callSupabaseRpc preserves source-byte strings and nullable history JSONL", async () => {
  const result = await callSupabaseRpc(
    {
      url: "https://project.supabase.co",
      key: "anon-key",
      functionName: "get_public_daily_usage",
      args: {},
    },
    () =>
      Promise.resolve(
        new Response(
          '{"metric_basis":"source_bytes","cells":[{"source_bytes":"9007199254740993","usage_precision":"batch_only","jsonl_bytes":null}]}',
          { headers: { "content-type": "application/json" } },
        ),
      ),
  );

  assert.deepEqual(result, {
    data: {
      metric_basis: "source_bytes",
      cells: [{
        source_bytes: "9007199254740993",
        usage_precision: "batch_only",
        jsonl_bytes: null,
      }],
    },
    error: null,
  });
});
