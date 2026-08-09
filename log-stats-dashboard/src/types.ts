export interface DashboardQuery {
  from: string;
  to: string;
  search: string;
  page: number;
}

export type UsagePrecision = "exact" | "batch_only";

export interface DailyUsageCell {
  date: string;
  key_name: string;
  source_bytes: string;
  gpt_source_bytes: string;
  claude_source_bytes: string;
  grok_source_bytes: string;
  usage_precision: UsagePrecision;
  jsonl_bytes: string | null;
  gpt_bytes: string | null;
  claude_bytes: string | null;
  grok_bytes: string | null;
  batch_count: number;
}

export interface DailyUsageResponse {
  metric_basis: "source_bytes";
  timezone: string;
  from: string;
  to: string;
  using_test_data: boolean;
  pagination: {
    page: number;
    page_size: number;
    total: number;
  };
  names: string[];
  days: string[];
  cells: DailyUsageCell[];
  latest_sync_at: string | null;
}
