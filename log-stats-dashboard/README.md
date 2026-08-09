# Log Usage Dashboard

This directory contains the standalone Vite and Vanilla TypeScript frontend for the public log usage statistics Edge Function. It intentionally uses semantic HTML and CSS instead of a charting library so exact byte values remain readable and accessible.

## Development

Install the locked dependencies and start Vite:

```bash
npm ci
npm run dev
```

The development page calls the same-origin URL with `api=daily`. Use a local proxy or Playwright route mocks when the Edge Function is not serving the Vite page.

## Tests

Run unit tests with Vitest and jsdom:

```bash
npm test
```

Install the Playwright browser once, then run the mocked end-to-end suite:

```bash
npx playwright install chromium
npm run test:e2e
```

The end-to-end tests do not require a deployed Supabase project.

## Builds

Create the regular Vite build:

```bash
npm run build
```

Create a single-file build and update the Edge-importable TypeScript string module:

```bash
npm run build:edge
```

`build:edge` verifies that JavaScript and CSS are inline, then writes `../supabase/functions/log-usage-dashboard/dashboard_html.ts`. Commit that generated module with the frontend source.

## API contract

The page sends a same-origin request with these query parameters:

```text
api=daily&from=YYYY-MM-DD&to=YYYY-MM-DD&search=...&page=1&page_size=5
```

The public response is:

```ts
interface PublicDailyUsageResponse {
  metric_basis: "source_bytes";
  timezone: string;
  from: string;
  to: string;
  using_test_data: boolean;
  pagination: { page: number; page_size: 5; total: number };
  names: string[];
  days: string[];
  cells: Array<{
    date: string;
    key_name: string;
    source_bytes: string;
    gpt_source_bytes: string;
    claude_source_bytes: string;
    grok_source_bytes: string;
    usage_precision: "exact" | "batch_only";
    jsonl_bytes: string | null;
    gpt_bytes: string | null;
    claude_bytes: string | null;
    grok_bytes: string | null;
    batch_count: number;
  }>;
  latest_sync_at: string | null;
}
```

The dashboard matrix, intensity, and provider breakdown use the four exact
source-byte fields. Available byte values are nonnegative base-10 strings. The
frontend validates them at runtime and formats them as strings or `BigInt`; it
never converts byte values to `Number`, so values above `2^53` stay exact.

`batch_only` marks locally reconstructed history where exact per-name JSONL was
never recorded. Those JSONL fields must be `null`, and the details dialog says
“历史无逐人精确 JSONL” instead of presenting an estimate. Exact live cells may
show the additional normalized JSONL total.

## Deployment order

1. Run `npm ci`, `npm test`, and `npm run test:e2e`.
2. Run `npm run build:edge` and review the generated TypeScript module.
3. Deploy the database/RPC contract before the Edge Function.
4. Deploy the Edge Function with the generated dashboard module.
