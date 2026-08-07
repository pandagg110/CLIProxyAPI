import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { expect, test } from "vitest";

const css = readFileSync(resolve(process.cwd(), "src/styles.css"), "utf8");

function colorToken(name: string): string {
  const match = new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`).exec(css);
  expect(match, `CSS token --${name} must be defined as a six-digit hex color`).not.toBeNull();
  return match![1];
}

function linearChannel(value: number): number {
  const channel = value / 255;
  return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
}

function luminance(hex: string): number {
  const values = [1, 3, 5].map((offset) => Number.parseInt(hex.slice(offset, offset + 2), 16));
  return 0.2126 * linearChannel(values[0]) + 0.7152 * linearChannel(values[1]) + 0.0722 * linearChannel(values[2]);
}

function contrast(left: string, right: string): number {
  const [lighter, darker] = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
}

test("focus ring has at least 3:1 contrast against adjacent dashboard backgrounds", () => {
  const focus = colorToken("focus-ring");
  expect(css).toMatch(/:focus-visible[^\{]*\{[^}]*var\(--focus-ring\)/s);
  for (const background of ["surface", "page-background", "heat-4"]) {
    expect(contrast(focus, colorToken(background)), `${background} focus contrast`).toBeGreaterThanOrEqual(3);
  }
});

test("heat cell metadata has at least 4.5:1 contrast on the darkest heat background", () => {
  expect(css).toMatch(/\.intensity-4\s*\{[^}]*var\(--heat-4\)/s);
  expect(css).toMatch(/\.cell-button small\s*\{[^}]*var\(--heat-meta\)/s);
  expect(contrast(colorToken("heat-meta"), colorToken("heat-4"))).toBeGreaterThanOrEqual(4.5);
});
