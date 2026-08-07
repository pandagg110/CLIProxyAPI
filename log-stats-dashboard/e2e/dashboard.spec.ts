import { expect, test, type Page, type Route } from "@playwright/test";

const cell = {
  date: "2026-08-08",
  key_name: "运营一组",
  jsonl_bytes: "9007199254740993",
  gpt_bytes: "9007199254740000",
  claude_bytes: "993",
  grok_bytes: "0",
  batch_count: 2,
};

function response(overrides: Record<string, unknown> = {}) {
  return {
    timezone: "Asia/Hong_Kong",
    from: "2026-08-02",
    to: "2026-08-08",
    using_test_data: false,
    pagination: { page: 1, page_size: 5, total: 6 },
    names: ["运营一组", "运营二组", "研发", "客服", "市场"],
    days: ["2026-08-08", "2026-08-07", "2026-08-06", "2026-08-05", "2026-08-04", "2026-08-03", "2026-08-02"],
    cells: [cell, { ...cell, key_name: "运营二组", jsonl_bytes: "0", gpt_bytes: "0", claude_bytes: "0" }],
    latest_sync_at: "2026-08-08T03:00:00Z",
    ...overrides,
  };
}

async function mockDaily(page: Page, responder: (url: URL, route: Route) => Promise<void> | void = async (_url, route) => {
  await route.fulfill({ json: response() });
}) {
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("api") === "daily") {
      await responder(url, route);
      return;
    }
    await route.continue();
  });
}

test("shows the initial seven-day semantic matrix with exact bytes", async ({ page }) => {
  let requested: URL | undefined;
  await mockDaily(page, async (url, route) => {
    requested = url;
    await route.fulfill({ json: response() });
  });
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "日志用量统计" })).toBeVisible();
  await expect(page.getByRole("table", { name: "每日 API Key 日志字节矩阵" })).toBeVisible();
  await expect(page.getByRole("button", { name: /运营一组.*9,007,199,254,740,993 B/ })).toBeVisible();
  expect(requested?.searchParams.get("page_size")).toBe("5");
  const from = Date.parse(`${requested?.searchParams.get("from")}T00:00:00Z`);
  const to = Date.parse(`${requested?.searchParams.get("to")}T00:00:00Z`);
  expect((to - from) / 86_400_000 + 1).toBe(7);
});

test("persists thirty-day and custom ranges in the URL", async ({ page }) => {
  await mockDaily(page);
  await page.goto("/");
  await page.getByRole("button", { name: "近 30 天" }).click();
  await expect.poll(() => new URL(page.url()).searchParams.get("from")).not.toBeNull();
  let url = new URL(page.url());
  expect((Date.parse(`${url.searchParams.get("to")}T00:00:00Z`) - Date.parse(`${url.searchParams.get("from")}T00:00:00Z`)) / 86_400_000 + 1).toBe(30);

  await page.getByLabel("开始日期").fill("2026-01-01");
  await page.getByLabel("结束日期").fill("2026-02-15");
  await page.getByRole("button", { name: "应用日期" }).click();
  url = new URL(page.url());
  expect(url.searchParams.get("from")).toBe("2026-01-01");
  expect(url.searchParams.get("to")).toBe("2026-02-15");
});

test("persists search and moves through five-name pages", async ({ page }) => {
  await mockDaily(page, async (url, route) => {
    const requestedPage = Number(url.searchParams.get("page"));
    await route.fulfill({ json: response({
      pagination: { page: requestedPage, page_size: 5, total: 6 },
      names: requestedPage === 2 ? ["财务"] : response().names,
      cells: [],
    }) });
  });
  await page.goto("/");
  await page.getByLabel("按名称搜索").fill("财务");
  await page.getByRole("button", { name: "搜索" }).click();
  expect(new URL(page.url()).searchParams.get("search")).toBe("财务");
  await page.getByRole("button", { name: "下一页" }).click();
  await expect(page.getByRole("columnheader", { name: "财务" })).toBeVisible();
  expect(new URL(page.url()).searchParams.get("page")).toBe("2");
});

test("opens exact provider details by click and keyboard and closes with Escape", async ({ page }) => {
  await mockDaily(page);
  await page.goto("/");
  const usageCell = page.getByRole("button", { name: /运营一组.*9,007,199,254,740,993 B/ });
  await usageCell.focus();
  await usageCell.press("Enter");
  const dialog = page.getByRole("dialog", { name: "日志明细" });
  await expect(dialog).toContainText("2026-08-08");
  await expect(dialog).toContainText("运营一组");
  await expect(dialog).toContainText("GPT");
  await expect(dialog).toContainText("9,007,199,254,740,000 B");
  await expect(dialog).toContainText("批次数：2");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
  await expect(usageCell).toBeFocused();
  await usageCell.press("Enter");
  await page.getByRole("button", { name: "关闭明细" }).click();
  await expect(usageCell).toBeFocused();
});

test("shows a prominent test-data notice and an empty state", async ({ page }) => {
  await mockDaily(page, async (_url, route) => {
    await route.fulfill({ json: response({ using_test_data: true, names: [], cells: [], pagination: { page: 1, page_size: 5, total: 0 } }) });
  });
  await page.goto("/");
  await expect(page.getByRole("status")).toContainText("测试数据");
  await expect(page.getByText("当前条件下没有日志记录。", { exact: true })).toBeVisible();
});

test("hides backend details on RPC failure and retries", async ({ page }) => {
  let attempts = 0;
  await mockDaily(page, async (_url, route) => {
    attempts += 1;
    if (attempts === 1) {
      await route.fulfill({ status: 502, contentType: "application/json", body: JSON.stringify({ error: "secret rpc stack" }) });
    } else {
      await route.fulfill({ json: response() });
    }
  });
  await page.goto("/");
  await expect(page.getByRole("status")).toContainText("数据暂时无法加载，请稍后重试。");
  await expect(page.getByText("secret rpc stack")).toHaveCount(0);
  await page.getByRole("button", { name: "重试" }).click();
  await expect(page.getByRole("table", { name: "每日 API Key 日志字节矩阵" })).toBeVisible();
  expect(attempts).toBe(2);
});

test("supports mobile horizontal scrolling and keyboard cell activation", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockDaily(page);
  await page.goto("/");
  const region = page.getByTestId("matrix-scroll");
  await expect.poll(() => region.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
  const usageCell = page.getByRole("button", { name: /运营一组.*9,007,199,254,740,993 B/ });
  await usageCell.focus();
  await usageCell.press("Space");
  await expect(page.getByRole("dialog", { name: "日志明细" })).toBeVisible();
});
