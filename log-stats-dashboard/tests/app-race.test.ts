import { expect, test } from "vitest";
import { DashboardApp, type DailyFetcher } from "../src/app";
import type { DailyUsageResponse, DashboardQuery } from "../src/types";

interface Deferred<T> {
  promise: Promise<T>;
  resolve(value: T): void;
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  return { promise: new Promise<T>((done) => { resolve = done; }), resolve };
}

function responseFor(query: DashboardQuery, name: string): DailyUsageResponse {
  const days: string[] = [];
  for (let cursor = Date.parse(`${query.from}T00:00:00Z`); cursor <= Date.parse(`${query.to}T00:00:00Z`); cursor += 86_400_000) {
    days.push(new Date(cursor).toISOString().slice(0, 10));
  }
  return {
    metric_basis: "source_bytes",
    timezone: "Asia/Hong_Kong",
    from: query.from,
    to: query.to,
    using_test_data: false,
    pagination: { page: query.page, page_size: 5, total: 1 },
    names: [name],
    days,
    cells: [{
      date: query.to,
      key_name: name,
      source_bytes: "1",
      gpt_source_bytes: "1",
      claude_source_bytes: "0",
      grok_source_bytes: "0",
      usage_precision: "exact",
      jsonl_bytes: "1",
      gpt_bytes: "1",
      claude_bytes: "0",
      grok_bytes: "0",
      batch_count: 1,
    }],
    latest_sync_at: null,
  };
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

test("an obsolete request cannot clear loading or overwrite data after popstate navigation", async () => {
  window.history.replaceState(null, "", "/?from=2026-08-02&to=2026-08-08&search=old&page=1");
  const requests: Array<{ query: DashboardQuery; deferred: Deferred<DailyUsageResponse> }> = [];
  const fetcher: DailyFetcher = (requestQuery) => {
    const pending = deferred<DailyUsageResponse>();
    requests.push({ query: { ...requestQuery }, deferred: pending });
    return pending.promise;
  };
  const root = document.createElement("div");
  document.body.replaceChildren(root);
  new DashboardApp(root, fetcher).start();
  expect(requests).toHaveLength(1);

  window.history.pushState(null, "", "/?from=2026-07-10&to=2026-08-08&search=new&page=1");
  window.dispatchEvent(new PopStateEvent("popstate"));
  expect(requests).toHaveLength(2);

  requests[0].deferred.resolve(responseFor(requests[0].query, "旧请求"));
  await flush();
  expect(root.textContent).toContain("正在加载数据");
  expect(root.textContent).not.toContain("旧请求");

  requests[1].deferred.resolve(responseFor(requests[1].query, "新请求"));
  await flush();
  expect(root.textContent).toContain("新请求");
  expect(root.textContent).not.toContain("旧请求");
  expect(root.textContent).toContain("数据已更新");
});
