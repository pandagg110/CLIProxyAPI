import type { DailyUsageCell } from "./types";

const DECIMAL = /^(?:0|[1-9]\d*)$/;

export function formatDecimalBytes(value: string): string {
  if (!DECIMAL.test(value)) throw new Error("Invalid decimal byte string");
  const groups: string[] = [];
  for (let end = value.length; end > 0; end -= 3) {
    groups.unshift(value.slice(Math.max(0, end - 3), end));
  }
  return `${groups.join(",")} B`;
}

function matrixKey(date: string, name: string): string {
  return JSON.stringify([date, name]);
}

export function buildMatrix(cells: DailyUsageCell[]) {
  const values = new Map(cells.map((cell) => [matrixKey(cell.date, cell.key_name), cell]));
  return {
    get(date: string, name: string): DailyUsageCell | undefined {
      return values.get(matrixKey(date, name));
    },
  };
}

export function providerBreakdown(cell: DailyUsageCell) {
  return [
    { label: "GPT", bytes: cell.gpt_source_bytes },
    { label: "Claude", bytes: cell.claude_source_bytes },
    { label: "Grok", bytes: cell.grok_source_bytes },
  ];
}

export function intensityLevel(value: string, maximum: bigint): number {
  const bytes = BigInt(value);
  if (bytes === 0n || maximum === 0n) return 0;
  const scaled = bytes * 4n;
  if (scaled <= maximum) return 1;
  if (scaled <= maximum * 2n) return 2;
  if (scaled <= maximum * 3n) return 3;
  return 4;
}
