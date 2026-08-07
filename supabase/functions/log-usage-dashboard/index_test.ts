import assert from "node:assert/strict";

import { createDashboardHandler } from "./index.ts";

function env(name: string): string | undefined {
  return {
    SUPABASE_URL: "https://project.supabase.co",
    SUPABASE_ANON_KEY: "anon-key",
  }[name];
}

Deno.test("dashboard handler serves the safe placeholder HTML for a normal GET", async () => {
  const handler = createDashboardHandler({
    env,
    rpc: () => Promise.resolve({ data: null, error: null }),
  });

  const response = await handler(
    new Request("https://edge.test/log-usage-dashboard"),
  );
  const html = await response.text();

  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html/);
  assert.match(html, /dashboard frontend has not been built/i);
});

Deno.test("dashboard handler rejects invalid daily range and pagination before RPC", async () => {
  let calls = 0;
  const handler = createDashboardHandler({
    env,
    rpc: () => {
      calls += 1;
      return Promise.resolve({ data: null, error: null });
    },
  });
  const invalidUrls = [
    "?api=daily&from=2025-01-01&to=2026-01-02",
    "?api=daily&from=2026-08-01&to=2026-08-02&page=0",
    "?api=daily&from=2026-08-01&to=2026-08-02&page_size=21",
  ];

  for (const suffix of invalidUrls) {
    const response = await handler(new Request(`https://edge.test/${suffix}`));
    assert.equal(response.status, 400);
    assert.equal((await response.json()).error, "invalid_query");
  }
  assert.equal(calls, 0);
});

Deno.test("dashboard handler calls the anon RPC and returns the compact daily shape", async () => {
  const payload = {
    timezone: "Asia/Shanghai",
    from: "2026-08-01",
    to: "2026-08-02",
    using_test_data: true,
    total_names: 1,
    names: ["张三"],
    days: ["2026-08-01", "2026-08-02"],
    cells: [{
      date: "2026-08-01",
      name: "张三",
      gpt_bytes: 120,
      claude_bytes: 0,
      grok_bytes: 0,
    }],
    last_synced_at: "2026-08-08T00:00:00Z",
  };
  let captured: unknown;
  const handler = createDashboardHandler({
    env,
    rpc: (call) => {
      captured = call;
      return Promise.resolve({ data: payload, error: null });
    },
  });

  const response = await handler(
    new Request(
      "https://edge.test/?api=daily&from=2026-08-01&to=2026-08-02&search=%E5%BC%A0&page=2&page_size=5",
    ),
  );

  assert.equal(response.status, 200);
  assert.equal(
    response.headers.get("cache-control"),
    "public, max-age=30, stale-while-revalidate=60",
  );
  assert.deepEqual(await response.json(), payload);
  assert.deepEqual(captured, {
    url: "https://project.supabase.co",
    key: "anon-key",
    functionName: "get_public_daily_usage",
    args: {
      p_from: "2026-08-01",
      p_to: "2026-08-02",
      p_search: "张",
      p_page: 2,
      p_page_size: 5,
    },
  });
});

Deno.test("dashboard handler exposes CORS and maps RPC failures without leaking details", async () => {
  const handler = createDashboardHandler({
    env,
    rpc: () =>
      Promise.resolve({
        data: null,
        error: { code: "XX000", message: "private database detail" },
      }),
  });

  const response = await handler(
    new Request("https://edge.test/?api=daily&from=2026-08-01&to=2026-08-02"),
  );

  assert.equal(response.status, 502);
  assert.equal(response.headers.get("access-control-allow-origin"), "*");
  assert.deepEqual(await response.json(), { error: "dashboard_unavailable" });
});

Deno.test("dashboard handler allows GET only", async () => {
  const handler = createDashboardHandler({
    env,
    rpc: () => Promise.resolve({ data: null, error: null }),
  });

  const response = await handler(
    new Request("https://edge.test/", { method: "POST" }),
  );

  assert.equal(response.status, 405);
  assert.equal(response.headers.get("allow"), "GET, OPTIONS");
});
