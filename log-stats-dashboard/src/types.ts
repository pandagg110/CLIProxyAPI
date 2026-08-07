export interface DashboardQuery {
  from: string;
  to: string;
  search: string;
  page: number;
}

export interface DailyUsageCell {
  date: string;
  key_name: string;
  jsonl_bytes: string;
  gpt_bytes: string;
  claude_bytes: string;
  grok_bytes: string;
  batch_count: number;
}

export interface DailyUsageResponse {
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
