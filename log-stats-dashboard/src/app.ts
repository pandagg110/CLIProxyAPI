import { fetchDailyUsage } from "./api";
import { buildMatrix, formatDecimalBytes, intensityLevel, providerBreakdown } from "./domain";
import type { DailyUsageCell, DailyUsageResponse, DashboardQuery } from "./types";
import {
  encodeDashboardQuery,
  localToday,
  parseDashboardQuery,
  quickRange,
  validateCustomRange,
} from "./url-state";
import { deriveViewState } from "./view-state";

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  })[character]!);
}

function pageCount(total: number): number {
  return Math.max(1, Math.ceil(total / 5));
}

export type DailyFetcher = (query: DashboardQuery, signal?: AbortSignal) => Promise<DailyUsageResponse>;

interface DraftState {
  from: string;
  to: string;
  search: string;
}

interface InteractionSnapshot {
  focusKey: string | null;
  selection: { start: number; end: number; direction: "forward" | "backward" | "none" } | null;
  dialogOpen: boolean;
}

export class DashboardApp {
  private query: DashboardQuery;
  private drafts: DraftState;
  private data: DailyUsageResponse | null = null;
  private loading = false;
  private failed = false;
  private validationMessage = "";
  private controller: AbortController | null = null;
  private openCell: Pick<DailyUsageCell, "date" | "key_name"> | null = null;
  private dialogTriggerKey: string | null = null;

  constructor(
    private readonly root: HTMLElement,
    private readonly fetcher: DailyFetcher = fetchDailyUsage,
  ) {
    this.query = parseDashboardQuery(window.location.search, localToday());
    this.drafts = { from: this.query.from, to: this.query.to, search: this.query.search };
  }

  start(): void {
    this.writeUrl("replace");
    window.addEventListener("popstate", () => {
      this.query = parseDashboardQuery(window.location.search, localToday());
      this.drafts = { from: this.query.from, to: this.query.to, search: this.query.search };
      this.validationMessage = "";
      void this.load();
    });
    window.addEventListener("online", () => {
      this.render();
      if (this.failed) void this.load();
    });
    window.addEventListener("offline", () => this.render());
    this.render();
    void this.load();
  }

  private writeUrl(mode: "push" | "replace"): void {
    const url = new URL(window.location.href);
    url.search = encodeDashboardQuery(this.query);
    if (mode === "push") window.history.pushState(null, "", url);
    else window.history.replaceState(null, "", url);
  }

  private updateQuery(next: DashboardQuery, historyMode: "push" | "replace" = "push"): void {
    this.query = next;
    this.validationMessage = "";
    this.writeUrl(historyMode);
    void this.load();
  }

  private async load(): Promise<void> {
    this.controller?.abort();
    const controller = new AbortController();
    const query = { ...this.query };
    this.controller = controller;
    this.loading = true;
    this.failed = false;
    this.render();
    try {
      const data = await this.fetcher(query, controller.signal);
      if (this.controller !== controller) return;
      this.data = data;
      this.failed = false;
      const lastPage = pageCount(data.pagination.total);
      if (data.pagination.total > 0 && query.page > lastPage) {
        this.updateQuery({ ...this.query, page: lastPage }, "replace");
        return;
      }
    } catch (error) {
      if (this.controller !== controller) return;
      if (error instanceof DOMException && error.name === "AbortError") return;
      this.failed = true;
    } finally {
      if (this.controller === controller) {
        this.loading = false;
        this.render();
      }
    }
  }

  private render(): void {
    const interaction = this.captureInteraction();
    const view = deriveViewState({
      loading: this.loading,
      hasData: this.data !== null,
      error: this.failed,
      online: navigator.onLine,
      empty: this.data?.pagination.total === 0,
      usingTestData: this.data?.using_test_data ?? false,
    });
    const banners = [
      view.testData ? '<p class="banner banner-warning"><strong>测试数据</strong>：当前结果包含测试批次，请勿用于正式结算。</p>' : "",
      view.offline ? '<p class="banner">当前处于离线状态，显示的数据可能不是最新结果。</p>' : "",
      view.stale ? '<p class="banner">刷新失败，正在显示上一次成功加载的数据。</p>' : "",
    ].join("");
    const statusText = view.loading
      ? (this.data === null ? "正在加载数据…" : "正在更新数据…")
      : view.error
        ? (view.stale ? "数据更新失败，已保留原有结果。" : "数据暂时无法加载，请稍后重试。")
        : this.data === null ? "" : "数据已更新。";

    this.root.innerHTML = `
      <main class="shell">
        <header class="page-header">
          <p class="eyebrow">运营统计</p>
          <h1>日志用量统计</h1>
          <p class="intro">按日期和 API Key Name 查看 JSONL 精确字节，并下钻到各模型供应商。</p>
        </header>

        <section class="filters" aria-label="筛选条件">
          <div class="quick-ranges" aria-label="快捷日期范围">
            <button type="button" data-range="7" data-focus-key="range-7">近 7 天</button>
            <button type="button" data-range="30" data-focus-key="range-30">近 30 天</button>
          </div>
          <form id="date-form" class="date-form">
            <label>开始日期<input name="from" type="date" value="${escapeHtml(this.drafts.from)}" data-focus-key="filter-from" required></label>
            <span aria-hidden="true">至</span>
            <label>结束日期<input name="to" type="date" value="${escapeHtml(this.drafts.to)}" data-focus-key="filter-to" required></label>
            <button type="submit" data-focus-key="apply-date">应用日期</button>
          </form>
          <form id="search-form" class="search-form" role="search">
            <label>按名称搜索<input name="search" type="search" maxlength="100" value="${escapeHtml(this.drafts.search)}" data-focus-key="filter-search" placeholder="API Key Name"></label>
            <button type="submit" data-focus-key="apply-search">搜索</button>
          </form>
          ${this.validationMessage ? `<p class="validation" role="alert">${escapeHtml(this.validationMessage)}</p>` : ""}
        </section>

        <section id="announcements" class="announcements" role="status" aria-live="polite" aria-atomic="true">
          ${banners}
          <p class="load-status">${statusText}</p>
        </section>

        ${this.renderContent(view.error && !view.stale, view.empty)}
      </main>
      <dialog id="details-dialog" aria-labelledby="details-title">
        <div class="dialog-head">
          <h2 id="details-title">日志明细</h2>
          <button type="button" class="icon-button" data-close data-focus-key="dialog-close" aria-label="关闭明细">关闭</button>
        </div>
        <div id="details-content"></div>
      </dialog>
    `;
    this.bindEvents();
    this.restoreInteraction(interaction);
  }

  private renderContent(fatalError: boolean, empty: boolean): string {
    if (fatalError) {
      return '<section class="state-card"><h2>暂时无法获取统计数据</h2><p>数据暂时无法加载，请稍后重试。</p><button type="button" data-retry data-focus-key="retry">重试</button></section>';
    }
    if (this.data === null) {
      return '<section class="state-card" aria-hidden="true"><div class="skeleton"></div><div class="skeleton short"></div></section>';
    }
    if (empty) {
      return '<section class="state-card"><h2>暂无数据</h2><p>当前条件下没有日志记录。</p></section>';
    }
    return `${this.renderSummary()}${this.renderTable()}${this.renderPagination()}`;
  }

  private renderSummary(): string {
    const latest = this.data?.latest_sync_at;
    let syncText = "尚未同步";
    if (latest !== null && latest !== undefined) {
      const parsed = new Date(latest);
      syncText = Number.isNaN(parsed.getTime()) ? "时间未知" : new Intl.DateTimeFormat("zh-CN", {
        dateStyle: "medium",
        timeStyle: "medium",
        timeZone: this.data?.timezone,
      }).format(parsed);
    }
    return `<div class="summary"><p><span>统计时区</span><strong>${escapeHtml(this.data!.timezone)}</strong></p><p><span>最近同步</span><strong>${escapeHtml(syncText)}</strong></p></div>`;
  }

  private renderTable(): string {
    const data = this.data!;
    const matrix = buildMatrix(data.cells);
    const maximum = data.cells.reduce((current, entry) => {
      const value = BigInt(entry.jsonl_bytes);
      return value > current ? value : current;
    }, 0n);
    const headers = data.names.map((name) => `<th scope="col">${escapeHtml(name)}</th>`).join("");
    const rows = data.days.map((day) => {
      const cells = data.names.map((name) => {
        const entry = matrix.get(day, name);
        if (entry === undefined) {
          return `<td class="missing"><span aria-label="${escapeHtml(name)}，${escapeHtml(day)}，无记录">—<small>无记录</small></span></td>`;
        }
        const formatted = formatDecimalBytes(entry.jsonl_bytes);
        const encoded = encodeURIComponent(JSON.stringify([entry.date, entry.key_name]));
        return `<td class="intensity-${intensityLevel(entry.jsonl_bytes, maximum)}"><button type="button" class="cell-button" data-cell="${encoded}" data-focus-key="cell-${encoded}" aria-label="${escapeHtml(name)}，${escapeHtml(day)}，${escapeHtml(formatted)}"><strong>${escapeHtml(formatted)}</strong><small>${entry.batch_count} 批</small></button></td>`;
      }).join("");
      return `<tr><th scope="row">${escapeHtml(day)}</th>${cells}</tr>`;
    }).join("");
    return `<section class="matrix-section" aria-labelledby="matrix-heading"><div class="section-heading"><div><h2 id="matrix-heading">每日 JSONL 字节</h2><p>数值为精确字节；“无记录”与 0 B 分开显示。</p></div><p class="legend" aria-label="蓝色深浅仅辅助表示相对用量">浅色 → 深色：相对用量</p></div><div class="table-scroll" data-testid="matrix-scroll" data-focus-key="matrix-scroll" tabindex="0"><table aria-label="每日 API Key 日志字节矩阵"><caption class="sr-only">日期为行，API Key Name 为列的 JSONL 字节矩阵</caption><thead><tr><th scope="col">日期</th>${headers}</tr></thead><tbody>${rows}</tbody></table></div></section>`;
  }

  private renderPagination(): string {
    const pagination = this.data!.pagination;
    const pages = pageCount(pagination.total);
    return `<nav class="pagination" aria-label="人员分页"><button type="button" data-page="${pagination.page - 1}" data-focus-key="page-previous" ${pagination.page <= 1 ? "disabled" : ""}>上一页</button><p>第 ${pagination.page} / ${pages} 页，共 ${pagination.total} 个名称</p><button type="button" data-page="${pagination.page + 1}" data-focus-key="page-next" ${pagination.page >= pages ? "disabled" : ""}>下一页</button></nav>`;
  }

  private bindEvents(): void {
    this.root.querySelector<HTMLInputElement>('input[name="from"]')?.addEventListener("input", (event) => {
      this.drafts.from = (event.currentTarget as HTMLInputElement).value;
    });
    this.root.querySelector<HTMLInputElement>('input[name="to"]')?.addEventListener("input", (event) => {
      this.drafts.to = (event.currentTarget as HTMLInputElement).value;
    });
    this.root.querySelector<HTMLInputElement>('input[name="search"]')?.addEventListener("input", (event) => {
      this.drafts.search = (event.currentTarget as HTMLInputElement).value;
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-range]").forEach((button) => {
      button.addEventListener("click", () => {
        const days = Number(button.dataset.range) as 7 | 30;
        const range = quickRange(days, localToday());
        this.drafts.from = range.from;
        this.drafts.to = range.to;
        this.updateQuery({ ...this.query, ...range, page: 1 });
      });
    });
    this.root.querySelector<HTMLFormElement>("#date-form")?.addEventListener("submit", (event) => {
      event.preventDefault();
      const values = new FormData(event.currentTarget as HTMLFormElement);
      const from = String(values.get("from") ?? "");
      const to = String(values.get("to") ?? "");
      const message = validateCustomRange(from, to);
      if (message !== null) {
        this.validationMessage = message;
        this.render();
        return;
      }
      this.updateQuery({ ...this.query, from, to, page: 1 });
    });
    this.root.querySelector<HTMLFormElement>("#search-form")?.addEventListener("submit", (event) => {
      event.preventDefault();
      const values = new FormData(event.currentTarget as HTMLFormElement);
      this.updateQuery({ ...this.query, search: String(values.get("search") ?? "").trim(), page: 1 });
    });
    this.root.querySelectorAll<HTMLButtonElement>("[data-page]").forEach((button) => {
      button.addEventListener("click", () => this.updateQuery({ ...this.query, page: Number(button.dataset.page) }));
    });
    this.root.querySelector<HTMLButtonElement>("[data-retry]")?.addEventListener("click", () => void this.load());
    this.root.querySelectorAll<HTMLButtonElement>("[data-cell]").forEach((button) => {
      button.addEventListener("click", () => {
        const [date, name] = JSON.parse(decodeURIComponent(button.dataset.cell!)) as [string, string];
        const entry = this.data?.cells.find((candidate) => candidate.date === date && candidate.key_name === name);
        if (entry !== undefined) this.openDetails(entry, button);
      });
    });
    const dialog = this.root.parentElement?.querySelector<HTMLDialogElement>("#details-dialog") ?? document.querySelector<HTMLDialogElement>("#details-dialog");
    dialog?.querySelector<HTMLButtonElement>("[data-close]")?.addEventListener("click", () => dialog.close());
    dialog?.addEventListener("close", () => {
      this.openCell = null;
      const trigger = this.findFocusElement(this.dialogTriggerKey);
      this.dialogTriggerKey = null;
      trigger?.focus();
    });
  }

  private openDetails(cell: DailyUsageCell, trigger: HTMLElement): void {
    this.openCell = { date: cell.date, key_name: cell.key_name };
    this.dialogTriggerKey = trigger.dataset.focusKey ?? null;
    this.showDialog(cell);
  }

  private captureInteraction(): InteractionSnapshot {
    const active = document.activeElement instanceof HTMLElement && this.root.contains(document.activeElement)
      ? document.activeElement
      : null;
    const selection = active instanceof HTMLInputElement && active.selectionStart !== null && active.selectionEnd !== null
      ? {
          start: active.selectionStart,
          end: active.selectionEnd,
          direction: active.selectionDirection ?? "none",
        }
      : null;
    return {
      focusKey: active?.dataset.focusKey ?? null,
      selection,
      dialogOpen: this.root.querySelector<HTMLDialogElement>("#details-dialog")?.open ?? false,
    };
  }

  private restoreInteraction(snapshot: InteractionSnapshot): void {
    if (snapshot.dialogOpen && this.openCell !== null) {
      const cell = this.data?.cells.find((candidate) =>
        candidate.date === this.openCell?.date && candidate.key_name === this.openCell?.key_name
      );
      if (cell !== undefined) this.showDialog(cell, false);
      else this.openCell = null;
    }

    const target = this.findFocusElement(snapshot.focusKey);
    target?.focus();
    if (target instanceof HTMLInputElement && snapshot.selection !== null) {
      target.setSelectionRange(snapshot.selection.start, snapshot.selection.end, snapshot.selection.direction);
    }
  }

  private findFocusElement(key: string | null): HTMLElement | null {
    if (key === null) return null;
    return Array.from(this.root.querySelectorAll<HTMLElement>("[data-focus-key]"))
      .find((element) => element.dataset.focusKey === key) ?? null;
  }

  private showDialog(cell: DailyUsageCell, focusClose = true): void {
    const dialog = this.root.querySelector<HTMLDialogElement>("#details-dialog");
    const content = dialog?.querySelector<HTMLElement>("#details-content") ?? null;
    if (dialog === null || content === null) return;
    content.innerHTML = `<dl class="details"><div><dt>日期</dt><dd>${escapeHtml(cell.date)}</dd></div><div><dt>API Key Name</dt><dd>${escapeHtml(cell.key_name)}</dd></div><div><dt>JSONL 总字节</dt><dd>${escapeHtml(formatDecimalBytes(cell.jsonl_bytes))}</dd></div>${providerBreakdown(cell).map((provider) => `<div><dt>${provider.label}</dt><dd>${escapeHtml(formatDecimalBytes(provider.bytes))}</dd></div>`).join("")}<div><dt>批次</dt><dd>批次数：${cell.batch_count}</dd></div></dl>`;
    if (!dialog.open) dialog.showModal();
    if (focusClose) dialog.querySelector<HTMLButtonElement>("[data-close]")?.focus();
  }
}
